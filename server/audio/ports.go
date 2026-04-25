// AREA: audio · PORTS
// Outbound ports for the audio feature. The pipeline talks to other
// features (today: inference) only through interfaces declared here, so
// this file is the entire surface area of audio's dependencies on the
// rest of the server. Concrete implementations are injected at startup
// from main.go.
//
// SWAP: any of these interfaces can be replaced with a fake for tests
// or with a different backend (e.g. a remote inference client) without
// touching the pipeline.

package audio

// InferenceClient is the part of the inference subsystem that audio
// needs to know about. Kept narrow on purpose — a wider interface would
// re-couple audio to inference internals. Grow this only when the
// pipeline genuinely needs a new capability.
//
// The concrete implementation today is *inference.Manager, wired in
// main.go. Keeping it as an interface here means audio can be tested
// with a fake and the inference feature can grow new methods without
// audio caring.
type InferenceClient interface {
	// Reserved for future capabilities: PushFrame, Subscribe, etc. The
	// interface is intentionally empty in v0.1 because pipeline.go only
	// holds the reference for wiring — once it actually streams frames
	// we'll add the methods that the streaming path needs.
}
