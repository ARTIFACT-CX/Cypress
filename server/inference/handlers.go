// AREA: inference · HANDLERS · INBOUND-ADAPTER
// HTTP routes for the inference feature. The UI hits these directly via
// fetch() from localhost — no Rust bridge needed since the traffic is
// intra-machine and same-origin from Tauri's perspective.
//
// This is inference's *inbound adapter* — the way external callers (the
// UI) drive the inference business logic in manager.go. Lives in the
// inference feature because every route here is about model lifecycle.

package inference

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"time"
)

// RegisterRoutes attaches the inference HTTP routes onto the given mux.
// Caller (main.go) supplies the mux; we don't own it. Keeps the wiring
// in one place at the composition root.
func RegisterRoutes(mux *http.ServeMux, mgr *Manager) {
	mux.HandleFunc("/status", func(w http.ResponseWriter, _ *http.Request) {
		// Snapshot carries {state, model, device, phase, error} in one
		// atomic read — see Manager.Status.
		writeJSON(w, http.StatusOK, mgr.Status())
	})

	mux.HandleFunc("/model/load", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var body struct {
			Name string `json:"name"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "invalid json body", http.StatusBadRequest)
			return
		}
		if body.Name == "" {
			http.Error(w, "missing 'name'", http.StatusBadRequest)
			return
		}
		// WHY: fire-and-forget. First-run model downloads can take many
		// minutes, and the browser's fetch would time out long before the
		// download finishes — leaving the UI thinking the load failed
		// while the worker is still happily pulling weights. Instead we
		// kick the load off in a background goroutine (with a generous
		// upper bound so a wedged loader can't pin the worker forever)
		// and let the UI drive completion from /status polling. The
		// Snapshot carries state + phase + error, which is everything
		// the UI needs to render the outcome.
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
			defer cancel()
			if err := mgr.LoadModel(ctx, body.Name); err != nil {
				log.Printf("load_model %q failed: %v", body.Name, err)
			}
		}()
		writeJSON(w, http.StatusAccepted, map[string]any{
			"ok":    true,
			"model": body.Name,
		})
	})
}

// writeJSON keeps Content-Type correct and the status code explicit at
// the call site. Local to this file because no other inbound adapter in
// this feature exists yet.
func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
