// AREA: inference · SUBPROCESS
// Owns the Python inference worker lifecycle. Responsible for launching,
// health-checking, and tearing down the Python process that loads models and
// does the actual generation. The Go side never imports PyTorch — this is the
// only bridge.
//
// SWAP: the worker backend. Today this shells out to a Python subprocess; a
// future implementation could target a remote gRPC inference server (cloud
// tier). Consumers should depend on the Manager interface, not on
// subprocess details.

package inference

import (
	"context"
	"sync"
)

// Manager is the Go-side handle to the Python inference worker.
//
// Lifecycle states:
//
//	idle     → no worker process, no model loaded
//	starting → subprocess launched, waiting for ready handshake
//	ready    → worker alive, no model loaded
//	loading  → worker alive, loading a model
//	serving  → worker alive, model loaded, accepting audio
type Manager struct {
	// SAFETY: mu guards all mutable state on Manager. All exported methods
	// that touch state must take mu or delegate to a method that does.
	mu sync.Mutex

	// TODO: add subprocess handle, unix socket client, model state, etc.
	// For now this is a skeleton — the HTTP server boots against it but no
	// commands are wired yet.
}

// NewManager builds a Manager in the idle state. Starting the worker is
// explicit (deferred until the UI asks for it) so the server can start up
// without blocking on Python initialization.
func NewManager() *Manager {
	return &Manager{}
}

// Shutdown terminates the worker subprocess if one is running. Safe to call
// even when idle. Honors ctx deadline for the kill timeout.
func (m *Manager) Shutdown(ctx context.Context) {
	m.mu.Lock()
	defer m.mu.Unlock()
	// TODO: send SIGTERM to the Python subprocess, wait up to ctx deadline,
	// then SIGKILL if still alive.
	_ = ctx
}
