// AREA: audio · PIPELINE
// The audio pipeline glues the UI-facing WebSocket transport to the model
// worker. Mic frames come in from the UI, get queued, forwarded to the Python
// worker; model output (audio + text) flows back out to the UI.
//
// This file is intentionally transport- and model-agnostic. The WS handler
// pushes frames in via Ingest(); the inference manager pulls frames out via
// Read(). Either end can be swapped (WebRTC transport, remote inference)
// without touching the pipeline.

package audio

import (
	"github.com/ARTIFACT-CX/cypress/server/inference"
)

// Frame is one chunk of audio. We use raw PCM 16-bit mono @ 24kHz in v0.1
// because that's Moshi's native rate and avoids encode/decode latency on
// localhost. Opus encoding becomes useful when we add a cloud transport.
type Frame struct {
	// PCM is little-endian int16 samples. Length is not fixed — the UI
	// chooses the chunk size (typically 20–40ms worth of samples).
	PCM []byte
}

// Pipeline routes frames between the transport and inference layers.
//
// SWAP: Pipeline is the single join point between transports and models.
// Keep it thin — no model logic, no codec logic, just plumbing.
type Pipeline struct {
	inference *inference.Manager

	// TODO: ring buffer for mic frames, subscriber list for model output
	// frames, cancellation plumbing.
}

// NewPipeline wires a new pipeline to the given inference manager. The
// pipeline does not start the manager — callers decide when to spin up the
// Python worker.
func NewPipeline(mgr *inference.Manager) *Pipeline {
	return &Pipeline{inference: mgr}
}

// Ingest receives a mic frame from the transport and forwards it toward the
// model worker. Returns fast; any queuing or backpressure is handled inside.
func (p *Pipeline) Ingest(_ Frame) {
	// TODO: enqueue onto the mic ring buffer, drop oldest if full (PERF:
	// dropping is preferable to blocking the WS reader under heavy load).
}
