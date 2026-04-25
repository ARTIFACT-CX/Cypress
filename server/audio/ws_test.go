// AREA: audio · WS · TESTS
// Unit tests for the WS handler / wsTransport. We use httptest.Server +
// the same coder/websocket library on the client side to drive a real
// upgrade — that way the wire shape (binary vs text frames, JSON
// envelope schema, close codes) is pinned, not just the Transport
// interface contract.

package audio

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

func dialTest(t *testing.T, h *WSHandler) (*websocket.Conn, func()) {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	return conn, func() { _ = conn.Close(websocket.StatusNormalClosure, "") }
}

func TestWS_StartFailureSendsErrorEnvelope(t *testing.T) {
	// Drive a real upgrade against a pipeline whose inference layer
	// rejects StartStream. Client should see a control envelope of
	// type=error with a useful message before the close.
	pipe := NewPipeline(&fakeInference{startErr: errExampleNotLoaded})
	h := NewWSHandler(pipe)

	conn, done := dialTest(t, h)
	defer done()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	typ, data, err := conn.Read(ctx)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if typ != websocket.MessageText {
		t.Fatalf("first frame type = %v, want MessageText", typ)
	}
	var env controlEnvelope
	if err := json.Unmarshal(data, &env); err != nil {
		t.Fatalf("decode envelope: %v (raw=%q)", err, data)
	}
	if env.Type != "error" {
		t.Errorf("Type = %q, want %q", env.Type, "error")
	}
	if env.Message == "" {
		t.Error("Message is empty")
	}
}

func TestWS_OpenEnvelopeCarriesSampleRate(t *testing.T) {
	// Successful open: client gets a JSON {"type":"open","sample_rate":...}
	// before any binary audio. The UI relies on this envelope to set up
	// the audio context at the right rate.
	sess := newFakeSession()
	sess.sampleRate = 24000
	pipe := NewPipeline(&fakeInference{session: sess})
	h := NewWSHandler(pipe)

	conn, done := dialTest(t, h)
	defer done()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	typ, data, err := conn.Read(ctx)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if typ != websocket.MessageText {
		t.Fatalf("type = %v, want MessageText", typ)
	}
	var env controlEnvelope
	_ = json.Unmarshal(data, &env)
	if env.Type != "open" || env.SampleRate != 24000 {
		t.Errorf("envelope = %+v, want open/24000", env)
	}
}

func TestWS_RoundTripsBinaryAndText(t *testing.T) {
	// One end-to-end: client sends a binary mic frame, model emits a
	// chunk with both PCM and text, client sees the binary frame *and*
	// the text envelope. Pins the entire client-visible protocol shape.
	sess := newFakeSession()
	pipe := NewPipeline(&fakeInference{session: sess})
	h := NewWSHandler(pipe)

	conn, done := dialTest(t, h)
	defer done()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// Drain the open envelope.
	if _, _, err := conn.Read(ctx); err != nil {
		t.Fatalf("read open: %v", err)
	}

	// Send a binary mic frame.
	micFrame := []byte{0x01, 0x00, 0x02, 0x00}
	if err := conn.Write(ctx, websocket.MessageBinary, micFrame); err != nil {
		t.Fatalf("write mic: %v", err)
	}

	// Wait for the pipeline to deliver it to fakeSession.Feed.
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		sess.mu.Lock()
		n := len(sess.fed)
		sess.mu.Unlock()
		if n >= 1 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	sess.mu.Lock()
	if len(sess.fed) == 0 {
		sess.mu.Unlock()
		t.Fatal("mic frame never reached session.Feed")
	}
	sess.mu.Unlock()

	// Push a model output: PCM + text. Client should see both frames.
	sess.outputs <- StreamOutput{PCM: []byte{0xaa, 0xbb}, Text: "hi"}

	var sawBinary, sawText bool
	for !sawBinary || !sawText {
		typ, data, err := conn.Read(ctx)
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		switch typ {
		case websocket.MessageBinary:
			if string(data) == string([]byte{0xaa, 0xbb}) {
				sawBinary = true
			}
		case websocket.MessageText:
			var env controlEnvelope
			_ = json.Unmarshal(data, &env)
			if env.Type == "text" && env.Data == "hi" {
				sawText = true
			}
		}
	}
}

// errExampleNotLoaded is a sentinel error for the "no model" scenario.
// Declared at package scope so the test can match on its identity if
// it ever wants to (today it just relies on Error()).
var errExampleNotLoaded = exampleErr("no model loaded")

type exampleErr string

func (e exampleErr) Error() string { return string(e) }
