// AREA: server · ENTRY
// Cypress orchestration server. Boots the HTTP + WebSocket listener, owns the
// Python inference worker lifecycle, and routes audio frames between the UI
// and the active model.

package main

import (
	"context"
	"encoding/json"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/ARTIFACT-CX/cypress/server/audio"
	"github.com/ARTIFACT-CX/cypress/server/inference"
	"github.com/ARTIFACT-CX/cypress/server/transport"
)

// SETUP: default listen address. The UI connects here from localhost; we don't
// bind externally because all traffic is intra-machine.
const listenAddr = "127.0.0.1:7842"

func main() {
	// STEP 1: build the long-lived subsystems. Each one is created in "idle"
	// state — starting the Python worker or opening an audio pipeline happens
	// later, in response to explicit UI commands.
	inferenceMgr := inference.NewManager()
	audioPipeline := audio.NewPipeline(inferenceMgr)
	wsHandler := transport.NewWSHandler(audioPipeline)

	// STEP 2: wire HTTP routes. `/health` is the liveness probe the Tauri UI
	// polls after spawning this process; `/ws` upgrades into the binary audio
	// stream.
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	mux.Handle("/ws", wsHandler)

	// STEP 2a: inference control endpoints. The UI hits these directly via
	// fetch() from localhost — no Rust bridge needed since the traffic is
	// intra-machine and same-origin from Tauri's perspective.
	mux.HandleFunc("/status", func(w http.ResponseWriter, _ *http.Request) {
		// Snapshot carries {state, model, device, phase} in one atomic
		// read — see inference.Manager.Status.
		writeJSON(w, http.StatusOK, inferenceMgr.Status())
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
			if err := inferenceMgr.LoadModel(ctx, body.Name); err != nil {
				log.Printf("load_model %q failed: %v", body.Name, err)
			}
		}()
		writeJSON(w, http.StatusAccepted, map[string]any{
			"ok":    true,
			"model": body.Name,
		})
	})

	srv := &http.Server{
		Addr:              listenAddr,
		Handler:           withCORS(mux),
		ReadHeaderTimeout: 5 * time.Second,
	}

	// STEP 3: bind the port first, then announce ready. Binding before
	// logging means a bind failure (port in use, permission) is visible at
	// the top of the log rather than buried after an optimistic "listening"
	// line. Accept() is deferred to the goroutine below.
	ln, err := net.Listen("tcp", listenAddr)
	if err != nil {
		log.Fatalf("bind %s failed: %v", listenAddr, err)
	}
	log.Printf("cypress-server listening on %s", listenAddr)

	go func() {
		if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
			log.Fatalf("server failed: %v", err)
		}
	}()

	// STEP 4: block until we get a shutdown signal.
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop
	log.Println("shutting down...")

	// STEP 5: graceful shutdown with a hard deadline. Python worker goes first
	// because it holds the big GPU handle; HTTP server second.
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	inferenceMgr.Shutdown(shutdownCtx)
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Printf("http shutdown error: %v", err)
	}
	log.Println("bye")
}

// withCORS wraps the mux with permissive CORS headers. Safe because the
// server binds to 127.0.0.1 only — no remote origin can reach us anyway,
// but the browser still enforces same-origin on the Tauri UI's dev server
// (http://localhost:1420). Without these headers fetch() from the UI fails
// with an opaque "access control checks" error.
func withCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// writeJSON is a tiny helper so the HTTP handlers above aren't littered
// with the same three lines. Keeps Content-Type correct and the status
// code explicit at the call site.
func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
