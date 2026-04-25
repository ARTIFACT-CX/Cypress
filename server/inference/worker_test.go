// AREA: inference · TESTS
// Unit tests for the IPC framing layer in worker.go. These don't spawn a
// real Python process — they construct a worker around io.Pipe and feed
// JSON lines from the test goroutine, exercising the dispatch / waiter /
// event paths in isolation.

package inference

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"
)

// pipedWorker constructs a *worker wired to an io.Pipe pair so the test can
// drive its readLoop directly. Returns the worker and a writer the test
// uses to push fake worker→host lines, plus a reader for everything the
// worker writes to its "stdin" (i.e. host→worker commands the test asserts
// on).
func pipedWorker(t *testing.T) (*worker, io.WriteCloser, *bufio.Reader, func()) {
	t.Helper()

	// stdin pipe: worker writes commands here; test reads them.
	cmdR, cmdW := io.Pipe()
	// stdout pipe: test writes replies/events here; readLoop consumes them.
	repR, repW := io.Pipe()

	w := &worker{
		stdin:   cmdW,
		waiters: make(map[uint64]chan reply),
		done:    make(chan struct{}),
	}
	go w.readLoop(bufio.NewScanner(repR))

	cleanup := func() {
		// Close the reply pipe to let readLoop exit, then close the command
		// pipe to free the test reader.
		_ = repW.Close()
		<-w.done
		_ = cmdW.Close()
		_ = cmdR.Close()
	}
	return w, repW, bufio.NewReader(cmdR), cleanup
}

// readCommand pulls one line off the worker's "stdin" and parses it.
// Used to learn the correlation id the worker just assigned.
func readCommand(t *testing.T, r *bufio.Reader) map[string]any {
	t.Helper()
	line, err := r.ReadString('\n')
	if err != nil {
		t.Fatalf("read command: %v", err)
	}
	var msg map[string]any
	if err := json.Unmarshal([]byte(line), &msg); err != nil {
		t.Fatalf("unmarshal command %q: %v", line, err)
	}
	return msg
}

func writeReply(t *testing.T, w io.Writer, msg map[string]any) {
	t.Helper()
	b, err := json.Marshal(msg)
	if err != nil {
		t.Fatalf("marshal reply: %v", err)
	}
	if _, err := w.Write(append(b, '\n')); err != nil {
		t.Fatalf("write reply: %v", err)
	}
}

func TestWorker_Send_RoutesReplyByID(t *testing.T) {
	w, replyW, cmdR, cleanup := pipedWorker(t)
	defer cleanup()

	type result struct {
		out map[string]any
		err error
	}
	done := make(chan result, 1)
	go func() {
		out, err := w.send(context.Background(), "status", nil)
		done <- result{out, err}
	}()

	// Pull the command the worker emitted, echo its id back in a reply.
	cmd := readCommand(t, cmdR)
	writeReply(t, replyW, map[string]any{"id": cmd["id"], "ok": true, "device": "mps"})

	r := <-done
	if r.err != nil {
		t.Fatalf("send: %v", r.err)
	}
	if r.out["device"] != "mps" {
		t.Errorf("reply device = %v, want mps", r.out["device"])
	}
}

func TestWorker_Send_ParallelCallsDontCross(t *testing.T) {
	// Two concurrent sends must each get their own reply. We deliberately
	// reply in reverse order to prove correlation isn't FIFO-dependent.
	w, replyW, cmdR, cleanup := pipedWorker(t)
	defer cleanup()

	type result struct {
		out map[string]any
		err error
	}
	resA := make(chan result, 1)
	resB := make(chan result, 1)

	go func() {
		out, err := w.send(context.Background(), "a", nil)
		resA <- result{out, err}
	}()
	cmdA := readCommand(t, cmdR)

	go func() {
		out, err := w.send(context.Background(), "b", nil)
		resB <- result{out, err}
	}()
	cmdB := readCommand(t, cmdR)

	// Reply to B first.
	writeReply(t, replyW, map[string]any{"id": cmdB["id"], "tag": "B"})
	writeReply(t, replyW, map[string]any{"id": cmdA["id"], "tag": "A"})

	rb := <-resB
	ra := <-resA
	if rb.out["tag"] != "B" || ra.out["tag"] != "A" {
		t.Errorf("crossed replies: A=%v B=%v", ra.out, rb.out)
	}
}

func TestWorker_Send_SurfacesErrorField(t *testing.T) {
	// REASON: the worker uses {"error": "..."} as a sentinel for handler-
	// level failures (unknown model, bad args). The Manager turns those
	// into Go errors so the HTTP layer can respond properly.
	w, replyW, cmdR, cleanup := pipedWorker(t)
	defer cleanup()

	done := make(chan error, 1)
	go func() {
		_, err := w.send(context.Background(), "load_model", nil)
		done <- err
	}()
	cmd := readCommand(t, cmdR)
	writeReply(t, replyW, map[string]any{"id": cmd["id"], "error": "unknown model 'foo'"})

	err := <-done
	if err == nil || !strings.Contains(err.Error(), "unknown model") {
		t.Fatalf("send error = %v, want one containing 'unknown model'", err)
	}
}

func TestWorker_Send_RespectsContextCancel(t *testing.T) {
	w, _, cmdR, cleanup := pipedWorker(t)
	defer cleanup()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := w.send(ctx, "status", nil)
		done <- err
	}()

	// Drain the command so send's stdin write unblocks, then cancel.
	// io.Pipe is synchronous — without the read, send sits forever inside
	// w.stdin.Write and never reaches the ctx.Done() select arm.
	readCommand(t, cmdR)
	cancel()

	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("err = %v, want context.Canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("send didn't return after context cancel")
	}
}

func TestWorker_Send_FailsWhenWorkerExits(t *testing.T) {
	w, replyW, cmdR, _ := pipedWorker(t)

	done := make(chan error, 1)
	go func() {
		_, err := w.send(context.Background(), "status", nil)
		done <- err
	}()

	// Drain the command first so send gets past its stdin write and
	// parks in the reply select. Then close the reply pipe to terminate
	// readLoop and exercise the "worker exited" exit path.
	readCommand(t, cmdR)
	_ = replyW.Close()
	<-w.done

	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "exited") {
			t.Errorf("err = %v, want exit error", err)
		}
	case <-time.After(time.Second):
		t.Fatal("send didn't return after worker exit")
	}
}

func TestWorker_ReadLoop_RoutesEvents(t *testing.T) {
	// Lines without an id but with an "event" field flow to onEvent.
	w, replyW, _, cleanup := pipedWorker(t)
	defer cleanup()

	var (
		mu       sync.Mutex
		captured []map[string]any
	)
	w.setOnEvent(func(m map[string]any) {
		mu.Lock()
		defer mu.Unlock()
		captured = append(captured, m)
	})

	writeReply(t, replyW, map[string]any{"event": "model_phase", "phase": "downloading_lm"})

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		n := len(captured)
		mu.Unlock()
		if n > 0 {
			break
		}
		time.Sleep(time.Millisecond)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(captured) != 1 {
		t.Fatalf("captured %d events, want 1", len(captured))
	}
	if captured[0]["phase"] != "downloading_lm" {
		t.Errorf("phase = %v, want downloading_lm", captured[0]["phase"])
	}
}
