// AREA: inference · TESTS
// HTTP handler tests. Drive the routes via httptest, assert status codes
// and JSON shape. The Manager is real but its spawner is faked, so these
// run without Python.

package inference

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// newHandlerTestServer wires a Manager (with fake spawner) onto a fresh mux
// and serves it via httptest. Returned with the Manager so tests can
// pre-seed state when needed.
func newHandlerTestServer(t *testing.T, fake *fakeWorker) (*httptest.Server, *Manager) {
	t.Helper()
	m := newTestManager(fake)
	mux := http.NewServeMux()
	RegisterRoutes(mux, m)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	t.Cleanup(func() {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		m.Shutdown(ctx)
	})
	return srv, m
}

func TestHandlers_Status_ReturnsSnapshotJSON(t *testing.T) {
	srv, _ := newHandlerTestServer(t, &fakeWorker{})

	res, err := http.Get(srv.URL + "/status")
	if err != nil {
		t.Fatalf("GET /status: %v", err)
	}
	defer res.Body.Close()

	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", res.StatusCode)
	}
	if got := res.Header.Get("Content-Type"); !strings.HasPrefix(got, "application/json") {
		t.Errorf("content-type = %q, want application/json", got)
	}

	var snap Snapshot
	if err := json.NewDecoder(res.Body).Decode(&snap); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if snap.State != StateIdle {
		t.Errorf("state = %q, want %q", snap.State, StateIdle)
	}
}

func TestHandlers_LoadModel_Accepted(t *testing.T) {
	fake := &fakeWorker{
		sendFn: func(_ context.Context, _ string, _ map[string]any) (map[string]any, error) {
			return map[string]any{"ok": true, "device": "cpu"}, nil
		},
	}
	srv, _ := newHandlerTestServer(t, fake)

	body := bytes.NewBufferString(`{"name":"moshi"}`)
	res, err := http.Post(srv.URL+"/model/load", "application/json", body)
	if err != nil {
		t.Fatalf("POST /model/load: %v", err)
	}
	defer res.Body.Close()

	if res.StatusCode != http.StatusAccepted {
		t.Fatalf("status = %d, want 202", res.StatusCode)
	}
}

func TestHandlers_LoadModel_RejectsWrongMethod(t *testing.T) {
	srv, _ := newHandlerTestServer(t, &fakeWorker{})

	res, err := http.Get(srv.URL + "/model/load")
	if err != nil {
		t.Fatalf("GET /model/load: %v", err)
	}
	defer res.Body.Close()

	if res.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405", res.StatusCode)
	}
}

func TestHandlers_LoadModel_RejectsBadBody(t *testing.T) {
	srv, _ := newHandlerTestServer(t, &fakeWorker{})

	res, err := http.Post(srv.URL+"/model/load", "application/json", bytes.NewBufferString(`{not-json`))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer res.Body.Close()

	if res.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", res.StatusCode)
	}
}

func TestHandlers_LoadModel_RejectsMissingName(t *testing.T) {
	srv, _ := newHandlerTestServer(t, &fakeWorker{})

	res, err := http.Post(srv.URL+"/model/load", "application/json", bytes.NewBufferString(`{}`))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer res.Body.Close()

	if res.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 for missing name", res.StatusCode)
	}
}

func TestHandlers_LoadModel_ConflictsWhenBusy(t *testing.T) {
	// Block the first load so the second one finds the manager busy.
	block := make(chan struct{})
	defer close(block)
	fake := &fakeWorker{
		sendFn: func(ctx context.Context, _ string, _ map[string]any) (map[string]any, error) {
			select {
			case <-block:
			case <-ctx.Done():
			}
			return map[string]any{"ok": true}, nil
		},
	}
	srv, _ := newHandlerTestServer(t, fake)

	if _, err := http.Post(srv.URL+"/model/load", "application/json", bytes.NewBufferString(`{"name":"moshi"}`)); err != nil {
		t.Fatalf("first POST: %v", err)
	}

	res, err := http.Post(srv.URL+"/model/load", "application/json", bytes.NewBufferString(`{"name":"personaplex"}`))
	if err != nil {
		t.Fatalf("second POST: %v", err)
	}
	defer res.Body.Close()

	if res.StatusCode != http.StatusConflict {
		t.Errorf("status = %d, want 409", res.StatusCode)
	}
}
