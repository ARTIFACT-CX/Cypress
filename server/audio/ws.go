// AREA: audio · WS · INBOUND-ADAPTER
// The WebSocket endpoint the Tauri UI connects to. Upgrades an HTTP request
// into a bidirectional binary channel carrying audio frames in both
// directions. Transport-layer concerns only: framing, backpressure, connection
// lifecycle. Audio semantics live in pipeline.go.
//
// This is audio's *inbound adapter* — the way the outside world (the UI)
// gets frames into the pipeline. Lives in the audio feature because its
// reason to exist is entirely about audio I/O.
//
// SWAP: v0.1 uses WebSocket because everything runs on localhost and TCP
// latency is negligible. If/when we add a cloud inference tier, we'll likely
// introduce a second transport (WebRTC) that plugs into the same pipeline.

package audio

import (
	"net/http"
)

// WSHandler upgrades /ws into a binary audio channel.
type WSHandler struct {
	pipeline *Pipeline
}

// NewWSHandler wires the WS endpoint to the audio pipeline.
func NewWSHandler(p *Pipeline) *WSHandler {
	return &WSHandler{pipeline: p}
}

// ServeHTTP is the upgrade entry point. For v0.1 we don't have a WebSocket
// library wired in yet — this stub just rejects with 501 so the route exists
// and the server compiles cleanly. The real upgrade (using nhooyr.io/websocket
// or gorilla/websocket) lands in the audio-streaming task.
func (h *WSHandler) ServeHTTP(w http.ResponseWriter, _ *http.Request) {
	// TODO: upgrade the connection, then spawn a reader goroutine that
	// drains binary frames into h.pipeline.Ingest() and a writer goroutine
	// that pushes model-output frames back to the client.
	http.Error(w, "websocket upgrade not yet implemented", http.StatusNotImplemented)
}
