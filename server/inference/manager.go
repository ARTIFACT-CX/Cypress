// AREA: inference · SUBPROCESS
// Owns the Python inference worker lifecycle. Responsible for launching,
// health-checking, and tearing down the Python process that loads models and
// does the actual generation. The Go side never imports PyTorch — this is the
// only bridge.
//
// SWAP: the worker backend. Today this shells out to a Python subprocess; a
// future implementation could target a remote gRPC inference server (cloud
// tier). Consumers should depend on the Manager interface, not on subprocess
// details.

package inference

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
)

// State is the public lifecycle of the inference subsystem. The UI polls
// this to render "loading…", enable/disable buttons, etc.
type State string

const (
	StateIdle     State = "idle"     // no worker process, no model loaded
	StateStarting State = "starting" // subprocess launched, waiting for handshake
	StateReady    State = "ready"    // worker alive, no model loaded
	StateLoading  State = "loading"  // worker alive, loading a model
	StateServing  State = "serving"  // worker alive, model loaded
)

// Manager is the Go-side handle to the Python inference worker.
type Manager struct {
	// SAFETY: mu guards all mutable state. Exported methods must take mu
	// (or delegate). We *do* release mu across the spawn call since that
	// blocks on uv cold start — other callers during that window get a
	// "busy" error from the state check.
	mu     sync.Mutex
	state  State
	worker *worker
	model  string
	device string // populated on successful load; "mps" / "cuda" / "cpu"
	phase  string // current loader phase ("downloading_mimi", etc.), cleared on ready
	// lastError stores the most recent load failure so the UI can surface it
	// even when it polled in after the failing HTTP request already returned.
	// Cleared when a new load starts so stale errors don't linger.
	lastError string
}

// Snapshot is the Manager's external view — what the HTTP /status endpoint
// returns. Grouped here so callers don't accidentally read half-updated
// fields without the lock.
type Snapshot struct {
	State  State  `json:"state"`
	Model  string `json:"model"`
	Device string `json:"device"`
	Phase  string `json:"phase"`
	// Error is the last load failure, if any. Present so the UI can show
	// the error even if its triggering HTTP request already timed out.
	Error string `json:"error,omitempty"`
}

// NewManager builds a Manager in the idle state. Starting the worker is
// deferred until the UI asks for a model — spawning Python at server boot
// would add seconds of latency before the UI could even connect.
func NewManager() *Manager {
	return &Manager{state: StateIdle}
}

// LoadModel lazily boots the worker (if idle), then asks it to load `name`.
// Errors from spawn, handshake, and the worker-side loader all bubble up
// verbatim so the UI can display a meaningful message instead of a generic
// "something went wrong".
func (m *Manager) LoadModel(ctx context.Context, name string) error {
	// STEP 1: take the lock, check we're in a state that accepts a new
	// load, and optimistically advance. Releasing before the slow spawn
	// means a second LoadModel arriving during boot gets a clean "busy"
	// rather than silently queuing.
	m.mu.Lock()
	if m.state == StateStarting || m.state == StateLoading {
		m.mu.Unlock()
		return errors.New("busy: another load in progress")
	}
	// Clear any stale error from a previous failed attempt so the UI
	// doesn't continue to show it once the user retries.
	m.lastError = ""

	if m.worker == nil {
		m.state = StateStarting
		m.mu.Unlock()

		// STEP 2: spawn outside the lock. This can take several seconds
		// on first uv run (resolving + installing deps into the venv).
		w, err := spawnWorker(ctx, workerDir())

		m.mu.Lock()
		if err != nil {
			m.state = StateIdle
			m.mu.Unlock()
			return err
		}
		// STEP 2a: wire the event handler. The worker emits phase
		// updates during load (downloading_mimi, loading_tokenizer,
		// etc.); we stash the latest one so the /status endpoint can
		// return it for the UI to display.
		w.onEvent = m.handleEvent
		m.worker = w
	}

	m.state = StateLoading
	w := m.worker
	m.mu.Unlock()

	// STEP 3: ask the worker to load the model. The "name" field matches
	// the worker's ipc_commands.load_model handler.
	reply, err := w.send(ctx, "load_model", map[string]any{"name": name})

	m.mu.Lock()
	defer m.mu.Unlock()
	// Always clear phase once load_model returns — the transitional
	// "downloading_*" strings are meaningful only during the blocking call.
	m.phase = ""

	if err != nil {
		// WHY: on loader failure we stay in Ready, not Idle — the worker
		// is still up and happy to try a different model. Killing it on
		// every failed load would make recovery needlessly expensive.
		m.state = StateReady
		m.lastError = err.Error()
		return err
	}
	m.model = name
	if dev, ok := reply["device"].(string); ok {
		m.device = dev
	}
	m.state = StateServing
	return nil
}

// handleEvent is the worker.onEvent callback. Fires for every unsolicited
// worker→host message; today that's just phase updates from model loaders.
// Keep this fast — it runs on the worker's read-loop goroutine.
func (m *Manager) handleEvent(msg map[string]any) {
	event, _ := msg["event"].(string)
	switch event {
	case "model_phase":
		phase, _ := msg["phase"].(string)
		m.mu.Lock()
		m.phase = phase
		// The "resolving" phase includes the device up front so the UI
		// can start showing "Loading on MPS…" before any weights have
		// actually landed there.
		if dev, ok := msg["device"].(string); ok {
			m.device = dev
		}
		m.mu.Unlock()
	}
}

// Status returns a point-in-time snapshot of the inference subsystem.
// Grouped into one struct (rather than separate accessors per field) so
// callers can't interleave reads and see inconsistent combinations.
func (m *Manager) Status() Snapshot {
	m.mu.Lock()
	defer m.mu.Unlock()
	return Snapshot{
		State:  m.state,
		Model:  m.model,
		Device: m.device,
		Phase:  m.phase,
		Error:  m.lastError,
	}
}

// Shutdown terminates the worker subprocess if one is running. Safe to call
// even when idle. Honors ctx deadline for the kill timeout.
func (m *Manager) Shutdown(ctx context.Context) {
	m.mu.Lock()
	w := m.worker
	m.worker = nil
	m.model = ""
	m.device = ""
	m.phase = ""
	m.state = StateIdle
	m.mu.Unlock()

	if w != nil {
		_ = w.stop(ctx)
	}
}

// workerDir resolves the worker/ directory. In dev the server runs with
// cwd = server/, so the worker is a sibling. CYPRESS_WORKER_DIR overrides
// this for tests and packaged builds where the layout differs.
func workerDir() string {
	if env := os.Getenv("CYPRESS_WORKER_DIR"); env != "" {
		return env
	}
	abs, _ := filepath.Abs("../worker")
	return abs
}
