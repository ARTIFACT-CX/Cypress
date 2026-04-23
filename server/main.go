// AREA: server · ENTRY
// Cypress orchestration server. Boots the HTTP + WebSocket listener, owns the
// Python inference worker lifecycle, and routes audio frames between the UI
// and the active model.

package main

import (
	"context"
	"log"
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

	srv := &http.Server{
		Addr:              listenAddr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	// STEP 3: run the server in a goroutine so main can listen for shutdown
	// signals. On SIGINT/SIGTERM we drain in-flight requests and tear down the
	// Python worker cleanly.
	go func() {
		log.Printf("cypress-server listening on %s", listenAddr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
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
