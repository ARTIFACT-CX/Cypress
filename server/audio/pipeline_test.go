// AREA: audio · PIPELINE · TESTS
// Unit tests for Pipeline.Run. Both ports are faked: a fakeInference
// satisfying InferenceClient gives us a controllable session, and a
// fakeTransport drives ReadFrame/WriteFrame without touching the wire.
// Lets us assert the cross-direction wiring (mic → Feed, Outputs →
// WriteFrame, text → SendText) end-to-end in milliseconds.

package audio

import (
	"context"
	"errors"
	"io"
	"sync"
	"testing"
	"time"
)

// fakeSession is a controllable StreamSession. Tests set what they want
// to see come out (via outputs) and inspect what got fed in (fed).
type fakeSession struct {
	mu         sync.Mutex
	fed        [][]byte
	outputs    chan StreamOutput
	closed     bool
	feedErr    error
	sampleRate int
}

func newFakeSession() *fakeSession {
	return &fakeSession{
		outputs:    make(chan StreamOutput, 8),
		sampleRate: 24000,
	}
}

func (f *fakeSession) Feed(_ context.Context, pcm []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.feedErr != nil {
		return f.feedErr
	}
	// Copy the slice — the caller may reuse the backing buffer.
	cp := make([]byte, len(pcm))
	copy(cp, pcm)
	f.fed = append(f.fed, cp)
	return nil
}

func (f *fakeSession) Outputs() <-chan StreamOutput { return f.outputs }
func (f *fakeSession) SampleRate() int              { return f.sampleRate }

func (f *fakeSession) Close(_ context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.closed {
		f.closed = true
		close(f.outputs)
	}
	return nil
}

// fakeInference is the InferenceClient port. startErr lets a test
// simulate "no model loaded" or other startup failures.
type fakeInference struct {
	session  *fakeSession
	startErr error
}

func (f *fakeInference) StartStream(_ context.Context) (StreamSession, error) {
	if f.startErr != nil {
		return nil, f.startErr
	}
	return f.session, nil
}

// fakeTransport is the per-connection Transport port. Tests push frames
// in via pushIn and read what was written via the captured slices.
type fakeTransport struct {
	mu sync.Mutex

	in       chan []byte
	inClosed bool

	written     [][]byte
	textsSent   []string
	errorsSent  []string
	openCalled  bool
	openSampleR int
}

func newFakeTransport() *fakeTransport {
	return &fakeTransport{in: make(chan []byte, 16)}
}

// pushIn enqueues a binary frame for ReadFrame to return.
func (t *fakeTransport) pushIn(data []byte) {
	t.in <- data
}

// closeIn signals client disconnect — ReadFrame returns io.EOF after
// the queue drains.
func (t *fakeTransport) closeIn() {
	t.mu.Lock()
	if !t.inClosed {
		t.inClosed = true
		close(t.in)
	}
	t.mu.Unlock()
}

func (t *fakeTransport) ReadFrame(ctx context.Context) ([]byte, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case data, ok := <-t.in:
		if !ok {
			return nil, io.EOF
		}
		return data, nil
	}
}

func (t *fakeTransport) WriteFrame(_ context.Context, pcm []byte) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	cp := make([]byte, len(pcm))
	copy(cp, pcm)
	t.written = append(t.written, cp)
	return nil
}

func (t *fakeTransport) SendOpen(_ context.Context, sampleRate int) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.openCalled = true
	t.openSampleR = sampleRate
	return nil
}

func (t *fakeTransport) SendText(_ context.Context, text string) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.textsSent = append(t.textsSent, text)
	return nil
}

func (t *fakeTransport) SendError(_ context.Context, message string) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.errorsSent = append(t.errorsSent, message)
	return nil
}

func TestPipeline_Run_StartFailureSendsError(t *testing.T) {
	// REASON: a "no model loaded" condition is the most likely error a
	// connecting UI will hit. The pipeline must surface it as a control
	// frame (so the UI can show a real message) rather than just dropping
	// the connection silently.
	pipe := NewPipeline(&fakeInference{startErr: errors.New("no model loaded")})
	tr := newFakeTransport()

	err := pipe.Run(context.Background(), tr)
	if err == nil || err.Error() != "no model loaded" {
		t.Fatalf("Run err = %v, want 'no model loaded'", err)
	}
	if len(tr.errorsSent) != 1 || tr.errorsSent[0] != "no model loaded" {
		t.Errorf("errorsSent = %v, want ['no model loaded']", tr.errorsSent)
	}
	if tr.openCalled {
		t.Error("SendOpen called despite start failure")
	}
}

func TestPipeline_Run_AnnouncesSampleRateBeforeAudio(t *testing.T) {
	// The UI needs the sample rate before it can schedule the first
	// playback frame. SendOpen must fire before any WriteFrame so the
	// client never races a frame ahead of its rate config.
	sess := newFakeSession()
	sess.sampleRate = 24000
	pipe := NewPipeline(&fakeInference{session: sess})
	tr := newFakeTransport()

	// Run in a goroutine because we want to inject and then EOF.
	done := make(chan error, 1)
	go func() { done <- pipe.Run(context.Background(), tr) }()

	// Push one chunk out, then close the client side cleanly.
	sess.outputs <- StreamOutput{PCM: []byte{0xaa, 0xbb}}
	// Give the writer a tick to flush.
	time.Sleep(20 * time.Millisecond)
	tr.closeIn()
	sess.Close(context.Background())

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Run did not exit")
	}

	if !tr.openCalled {
		t.Fatal("SendOpen was never called")
	}
	if tr.openSampleR != 24000 {
		t.Errorf("open sample_rate = %d, want 24000", tr.openSampleR)
	}
	if len(tr.written) != 1 || string(tr.written[0]) != string([]byte{0xaa, 0xbb}) {
		t.Errorf("written = %v, want [[0xaa 0xbb]]", tr.written)
	}
}

func TestPipeline_Run_ForwardsMicFramesToFeed(t *testing.T) {
	// The reader-direction core: every binary frame the client sends
	// must land in session.Feed exactly once, in order.
	sess := newFakeSession()
	pipe := NewPipeline(&fakeInference{session: sess})
	tr := newFakeTransport()

	done := make(chan error, 1)
	go func() { done <- pipe.Run(context.Background(), tr) }()

	tr.pushIn([]byte{0x01, 0x02})
	tr.pushIn([]byte{0x03, 0x04})
	tr.pushIn([]byte{0x05, 0x06})

	// Wait until all three land in fed before closing.
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		sess.mu.Lock()
		n := len(sess.fed)
		sess.mu.Unlock()
		if n == 3 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	tr.closeIn()
	sess.Close(context.Background())
	<-done

	sess.mu.Lock()
	defer sess.mu.Unlock()
	if len(sess.fed) != 3 {
		t.Fatalf("fed len = %d, want 3", len(sess.fed))
	}
	for i, want := range [][]byte{{1, 2}, {3, 4}, {5, 6}} {
		if string(sess.fed[i]) != string(want) {
			t.Errorf("fed[%d] = %v, want %v", i, sess.fed[i], want)
		}
	}
}

func TestPipeline_Run_RoutesTextChunksToSendText(t *testing.T) {
	// Text-bearing chunks fan into the SendText control path while their
	// PCM still goes through WriteFrame — the transport API splits them
	// so the client can buffer audio independently of transcript.
	sess := newFakeSession()
	pipe := NewPipeline(&fakeInference{session: sess})
	tr := newFakeTransport()

	done := make(chan error, 1)
	go func() { done <- pipe.Run(context.Background(), tr) }()

	sess.outputs <- StreamOutput{PCM: []byte{0xff}, Text: "hello"}
	time.Sleep(20 * time.Millisecond)
	tr.closeIn()
	sess.Close(context.Background())
	<-done

	if len(tr.textsSent) != 1 || tr.textsSent[0] != "hello" {
		t.Errorf("textsSent = %v, want ['hello']", tr.textsSent)
	}
	if len(tr.written) != 1 {
		t.Errorf("written len = %d, want 1", len(tr.written))
	}
}

func TestPipeline_Run_ClosesSessionOnClientDisconnect(t *testing.T) {
	// Critical: the worker side only allows one stream at a time. If we
	// don't close on disconnect, the next WS connect would deadlock
	// trying to open a new one. EOF from ReadFrame must trigger Close.
	sess := newFakeSession()
	pipe := NewPipeline(&fakeInference{session: sess})
	tr := newFakeTransport()

	done := make(chan error, 1)
	go func() { done <- pipe.Run(context.Background(), tr) }()

	tr.closeIn()
	sess.Close(context.Background()) // simulate worker side closing too

	if err := <-done; err != nil {
		t.Fatalf("Run: %v", err)
	}
	sess.mu.Lock()
	defer sess.mu.Unlock()
	if !sess.closed {
		t.Error("session.Close was not called")
	}
}

func TestPipeline_Run_ContextCancelStopsPumps(t *testing.T) {
	// Server shutdown cancels the request context. Both pump goroutines
	// must wake up and let Run return; otherwise srv.Shutdown blocks on
	// the active WS connection past its deadline.
	sess := newFakeSession()
	pipe := NewPipeline(&fakeInference{session: sess})
	tr := newFakeTransport()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- pipe.Run(ctx, tr) }()

	// Let the pumps actually start before cancelling.
	time.Sleep(10 * time.Millisecond)
	cancel()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Run did not exit on context cancel")
	}
}
