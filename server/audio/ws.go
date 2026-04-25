// AREA: audio · WS · INBOUND-ADAPTER
// The WebSocket endpoint the Tauri UI connects to. Upgrades an HTTP
// request into a duplex audio channel and hands it to the pipeline.
//
// Protocol (v0.1):
//
//   - Connection lifetime = session lifetime. No explicit "open"
//     command — the upgrade itself opens a session against the loaded
//     model, the close handshake ends it.
//   - Client → server: WS *binary* frames carry int16 LE mono PCM at
//     the session sample rate. Any reasonable size (the inference
//     layer reframes to mimi's 1920-sample frames).
//   - Server → client: WS *binary* frames carry the same shape PCM
//     coming back from the model. WS *text* frames carry small JSON
//     control envelopes for events:
//
//     {"type":"open","sample_rate":24000}
//     {"type":"text","data":" hello"}
//     {"type":"error","message":"no model loaded"}
//
// SWAP: v0.1 uses WebSocket because everything runs on localhost and
// TCP latency is negligible. WebRTC plugs in here later for cloud.

package audio

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"time"

	"github.com/coder/websocket"
)

// readTimeout caps how long a ReadFrame call may park. Voice sessions
// have legitimately quiet stretches — a paused user, or (especially on
// CPU/MPS bf16) the client waiting for the model to catch up — so this
// has to outlast any plausible silence. The proper fix is a WS ping/
// pong heartbeat that detects wedged clients without forcing them to
// send audio; until then, a generous deadline keeps the pipeline alive.
// TODO: replace with ping/pong keepalive (#TBD).
const readTimeout = 30 * time.Minute

// writeTimeout bounds individual WS sends. Per-frame, not per-session.
// A slow client pegging this triggers a connection drop, which is the
// right behavior — better than backing up audio on the server.
const writeTimeout = 5 * time.Second

// WSHandler upgrades /ws into a binary audio channel and delegates the
// lifetime to the audio pipeline.
type WSHandler struct {
	pipeline *Pipeline
}

// NewWSHandler wires the WS endpoint to the audio pipeline.
func NewWSHandler(p *Pipeline) *WSHandler {
	return &WSHandler{pipeline: p}
}

// ServeHTTP is the upgrade entry point.
func (h *WSHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// REASON: InsecureSkipVerify is safe here because the server only
	// binds to 127.0.0.1 (see main.go). The Tauri webview connects
	// from a localhost origin we don't control the port of in dev, so
	// the strict same-origin check would reject every connection.
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		InsecureSkipVerify: true,
	})
	if err != nil {
		// Accept already wrote an HTTP error response; just log.
		log.Printf("ws accept: %v", err)
		return
	}
	// Default close: 1011 / "internal error" if Run errors out before
	// the handshake-style close paths run. Overwritten on clean exit.
	closeStatus := websocket.StatusInternalError
	closeReason := "server error"
	defer func() {
		// Best-effort close. If the client already disconnected this
		// returns an error we don't care about.
		_ = conn.Close(closeStatus, closeReason)
	}()

	// ServeHTTP's request context cancels when the *server* is shutting
	// down, which is exactly the lifetime we want for this session.
	t := newWSTransport(conn)
	if err := h.pipeline.Run(r.Context(), t); err != nil {
		// Pipeline-level errors (model not loaded, worker died) get
		// surfaced to the client via SendError before Run returns;
		// nothing more to send here. Log for the operator.
		log.Printf("ws session ended with error: %v", err)
		closeStatus = websocket.StatusInternalError
		closeReason = "session error"
		return
	}
	closeStatus = websocket.StatusNormalClosure
	closeReason = "bye"
}

// wsTransport adapts *websocket.Conn to the audio.Transport interface.
// Per-connection; not safe to share across goroutines outside the
// pipeline's reader/writer split (which is the only intended pattern).
type wsTransport struct {
	conn *websocket.Conn
}

func newWSTransport(c *websocket.Conn) *wsTransport {
	// PERF: lift the default read message limit. v0.1 doesn't expect
	// huge frames (typical mic chunk is a few KB) but we don't want a
	// burst of buffered samples to be rejected on a transient pause.
	c.SetReadLimit(1 << 20) // 1 MiB
	return &wsTransport{conn: c}
}

// ReadFrame returns the next *binary* frame from the client. Text
// frames (which the v0.1 protocol doesn't actually expect from the
// client direction) are skipped silently — being lenient lets us add
// future text-control messages without breaking older clients.
func (t *wsTransport) ReadFrame(ctx context.Context) ([]byte, error) {
	rctx, cancel := context.WithTimeout(ctx, readTimeout)
	defer cancel()
	for {
		typ, data, err := t.conn.Read(rctx)
		if err != nil {
			// CloseError → io.EOF lets the pipeline distinguish "client
			// went away cleanly" from real read failures.
			if isCloseError(err) {
				return nil, io.EOF
			}
			return nil, err
		}
		if typ == websocket.MessageBinary {
			return data, nil
		}
		// Text frame from client — ignore for v0.1. A future "pause"
		// control would parse here.
	}
}

// WriteFrame ships one PCM frame to the client.
func (t *wsTransport) WriteFrame(ctx context.Context, pcm []byte) error {
	wctx, cancel := context.WithTimeout(ctx, writeTimeout)
	defer cancel()
	return t.conn.Write(wctx, websocket.MessageBinary, pcm)
}

// controlEnvelope is the shape of every server→client text frame. One
// JSON struct keeps the wire definition centralized; clients can switch
// on Type without parsing each variant separately.
type controlEnvelope struct {
	Type       string `json:"type"`
	SampleRate int    `json:"sample_rate,omitempty"`
	Data       string `json:"data,omitempty"`
	Message    string `json:"message,omitempty"`
}

func (t *wsTransport) sendJSON(ctx context.Context, env controlEnvelope) error {
	payload, err := json.Marshal(env)
	if err != nil {
		return err
	}
	wctx, cancel := context.WithTimeout(ctx, writeTimeout)
	defer cancel()
	return t.conn.Write(wctx, websocket.MessageText, payload)
}

func (t *wsTransport) SendOpen(ctx context.Context, sampleRate int) error {
	return t.sendJSON(ctx, controlEnvelope{Type: "open", SampleRate: sampleRate})
}

func (t *wsTransport) SendText(ctx context.Context, text string) error {
	return t.sendJSON(ctx, controlEnvelope{Type: "text", Data: text})
}

func (t *wsTransport) SendError(ctx context.Context, message string) error {
	return t.sendJSON(ctx, controlEnvelope{Type: "error", Message: message})
}

// isCloseError flattens both clean and abnormal WS-close conditions
// into "the connection ended" so the pipeline's pump loops can map
// them to io.EOF.
func isCloseError(err error) bool {
	var ce websocket.CloseError
	return errors.As(err, &ce)
}
