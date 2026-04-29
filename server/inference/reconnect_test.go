// AREA: inference · TESTS · RECONNECT
// Unit coverage for watchWorker. Three branches:
//   - Local subprocess drop → no retry, state→Idle, error surfaced.
//   - Remote drop with successful redial → state→Ready, error advises
//     reload; new handle installed.
//   - Remote drop with redial budget exhausted → state→Idle, error
//     surfaced.
//
// Backoff timing is shrunk via the package-level constants so tests
// run in milliseconds. The fake spawn function records attempt count
// so we can assert "retried more than once" without sleeping a second.

package inference

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ARTIFACT-CX/cypress/server/workers"
)

// withFastBackoff temporarily shrinks the reconnect timing constants
// so tests don't wait on real-world delays. Restored by t.Cleanup.
func withFastBackoff(t *testing.T) {
	t.Helper()
	origInitial := reconnectInitialBackoff
	origMax := reconnectMaxBackoff
	origBudget := reconnectTotalBudget
	reconnectInitialBackoff = 1 * time.Millisecond
	reconnectMaxBackoff = 4 * time.Millisecond
	reconnectTotalBudget = 50 * time.Millisecond
	t.Cleanup(func() {
		reconnectInitialBackoff = origInitial
		reconnectMaxBackoff = origMax
		reconnectTotalBudget = origBudget
	})
}

// loadAndServe drives the Manager through LoadModel into StateServing
// using the supplied fake. Helper because every reconnect test wants
// the worker live before simulating a drop.
func loadAndServe(t *testing.T, m *Manager) {
	t.Helper()
	if err := m.LoadModel("moshi"); err != nil {
		t.Fatalf("LoadModel: %v", err)
	}
	waitForState(t, m, StateServing)
}

func TestManager_LocalCrash_TransitionsToIdle(t *testing.T) {
	withFastBackoff(t)

	fake := &fakeWorker{}
	m := newTestManager(fake)
	// remote stays nil — this is the local-subprocess flavor.

	loadAndServe(t, m)
	fake.disconnect()

	snap := waitForState(t, m, StateIdle)
	if !strings.Contains(snap.Error, "subprocess exited") {
		t.Errorf("error = %q, want it to mention subprocess exit", snap.Error)
	}
}

func TestManager_RemoteDrop_RedialsAndLandsReady(t *testing.T) {
	withFastBackoff(t)

	first := &fakeWorker{}
	second := &fakeWorker{}
	var attempt atomic.Int32
	spawn := func(_ context.Context, _ string) (workers.Handle, error) {
		switch attempt.Add(1) {
		case 1:
			return first, nil
		case 2:
			return second, nil
		}
		return nil, errors.New("unexpected extra spawn")
	}
	m := newManagerWithSpawn(spawn)
	// REASON: watchWorker only retries when remote is non-nil; the
	// endpoint contents don't matter here because spawn is faked.
	m.remote = &workers.RemoteEndpoint{URL: "grpcs://example.test:7843", Token: "t"}

	loadAndServe(t, m)
	first.disconnect()

	// After redial we should land in Ready with a reload-advice error.
	snap := waitForState(t, m, StateReady)
	if !strings.Contains(snap.Error, "reload model") {
		t.Errorf("error = %q, want reload-advice message", snap.Error)
	}
	if snap.Model != "" {
		t.Errorf("model = %q, want cleared", snap.Model)
	}
	if got := attempt.Load(); got < 2 {
		t.Errorf("spawn attempts = %d, want at least 2 (initial + reconnect)", got)
	}

	// Second drop should also be observed — the new handle has its own
	// watcher installed by the recovery path.
	second.disconnect()
	// Spawn returns error on the third try; we should land in Idle.
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if m.Status().State == StateIdle {
			break
		}
		time.Sleep(2 * time.Millisecond)
	}
	if got := m.Status().State; got != StateIdle {
		t.Errorf("after second drop state = %q, want %q", got, StateIdle)
	}
}

func TestManager_RemoteDrop_BudgetExhausted_TransitionsToIdle(t *testing.T) {
	withFastBackoff(t)

	fake := &fakeWorker{}
	var attempt atomic.Int32
	spawn := func(_ context.Context, _ string) (workers.Handle, error) {
		// REASON: first call (initial spawn) succeeds. All subsequent
		// (reconnect) attempts fail to exhaust the budget.
		if attempt.Add(1) == 1 {
			return fake, nil
		}
		return nil, errors.New("connection refused")
	}
	m := newManagerWithSpawn(spawn)
	m.remote = &workers.RemoteEndpoint{URL: "grpcs://example.test:7843", Token: "t"}

	loadAndServe(t, m)
	fake.disconnect()

	snap := waitForState(t, m, StateIdle)
	if !strings.Contains(snap.Error, "remote worker disconnected") {
		t.Errorf("error = %q, want remote-disconnect message", snap.Error)
	}
	if got := attempt.Load(); got < 2 {
		t.Errorf("spawn attempts = %d, want at least 2 (initial + at least one retry)", got)
	}
}

func TestManager_Shutdown_DuringRedial_DoesNotResurrect(t *testing.T) {
	// REASON: regression for the Shutdown-vs-watchWorker race. If the
	// transport drops, the watcher snapshots state and starts redialing
	// lock-free. A concurrent Shutdown should win — the watcher must
	// notice it lost the slot and discard the freshly dialed handle
	// rather than resurrect a worker after Shutdown.
	withFastBackoff(t)
	// Stretch the redial budget so we can fit a Shutdown into the gap
	// between dial start and "fresh ready". The fake spawn blocks on a
	// channel the test owns, modeling a slow dial.
	reconnectTotalBudget = 500 * time.Millisecond

	first := &fakeWorker{}
	second := &fakeWorker{}
	gate := make(chan struct{})
	var attempt atomic.Int32
	spawn := func(_ context.Context, _ string) (workers.Handle, error) {
		switch attempt.Add(1) {
		case 1:
			return first, nil
		case 2:
			<-gate // hold here until the test releases us
			return second, nil
		}
		return nil, errors.New("unexpected spawn")
	}
	m := newManagerWithSpawn(spawn)
	m.remote = &workers.RemoteEndpoint{URL: "grpcs://example.test:7843", Token: "t"}

	loadAndServe(t, m)
	first.disconnect() // watcher fires, blocks inside redial waiting on gate

	// Wait until the watcher is parked in spawn (state=Starting).
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if m.Status().State == StateStarting {
			break
		}
		time.Sleep(2 * time.Millisecond)
	}
	if m.Status().State != StateStarting {
		t.Fatalf("watcher never reached redial; state=%q", m.Status().State)
	}

	// Shutdown wins the race. m.worker == nil already (watcher cleared
	// it); Shutdown sets state=Idle.
	m.Shutdown(context.Background())
	if got := m.Status().State; got != StateIdle {
		t.Fatalf("post-Shutdown state = %q, want %q", got, StateIdle)
	}

	// Now release the dial. The watcher gets `second` back and must
	// discard it instead of installing.
	close(gate)

	// Give the watcher time to run its post-redial check.
	time.Sleep(20 * time.Millisecond)
	snap := m.Status()
	if snap.State != StateIdle {
		t.Errorf("state = %q, want still %q (no resurrection)", snap.State, StateIdle)
	}
	w, _ := m.Worker()
	if w != nil {
		t.Errorf("worker = %v, want nil (Shutdown should be authoritative)", w)
	}
}

func TestManager_Shutdown_DoesNotTriggerReconnect(t *testing.T) {
	withFastBackoff(t)

	var attempt atomic.Int32
	fake := &fakeWorker{}
	spawn := func(_ context.Context, _ string) (workers.Handle, error) {
		attempt.Add(1)
		return fake, nil
	}
	m := newManagerWithSpawn(spawn)
	m.remote = &workers.RemoteEndpoint{URL: "grpcs://example.test:7843", Token: "t"}

	loadAndServe(t, m)
	// Shutdown should clear the worker, then Stop closes the fake's
	// done channel. watchWorker must see m.worker != fake and bail
	// without redialing.
	m.Shutdown(context.Background())

	// Give any spurious reconnect time to fire.
	time.Sleep(20 * time.Millisecond)
	if got := attempt.Load(); got != 1 {
		t.Errorf("spawn attempts = %d, want exactly 1 (no reconnect after Shutdown)", got)
	}
	if got := m.Status().State; got != StateIdle {
		t.Errorf("state = %q, want %q", got, StateIdle)
	}
}
