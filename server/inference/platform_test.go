// AREA: inference · TESTS · PLATFORM
// Manager's platform discovery: local mode reads runtime values, remote
// mode runs an eager probe at construction to capture handshake
// fields, and download_done events keep the downloaded-repos set
// fresh between handshakes. These tests cover the seams so the bug
// where laptop-platform leaked into remote variant selection can't
// regress.

package inference

import (
	"context"
	"errors"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ARTIFACT-CX/cypress/server/workers"
)

// withFakePlatform returns a fakeWorker whose Platform() reports the
// supplied snapshot. Used by remote-flavor tests so the spawned
// "worker" looks Linux even when go test runs on macOS.
type platformFake struct {
	*fakeWorker
	plat workers.Platform
}

func (p *platformFake) Platform() workers.Platform { return p.plat }

func newPlatformFake(plat workers.Platform) *platformFake {
	return &platformFake{fakeWorker: &fakeWorker{}, plat: plat}
}

func TestPlatform_LocalMode_UsesRuntime(t *testing.T) {
	m := newTestManager(&fakeWorker{})
	host, arch, ready := m.Platform()
	if !ready {
		t.Fatal("local mode should be ready immediately")
	}
	if host != runtime.GOOS || arch != runtime.GOARCH {
		t.Errorf("got %s/%s, want %s/%s", host, arch, runtime.GOOS, runtime.GOARCH)
	}
}

func TestPlatform_RemoteMode_BlocksUntilProbe(t *testing.T) {
	// REASON: the eager probe runs on a goroutine, so initial reads
	// must report not-ready. Without that, /models would return the
	// host's catalog (wrong repo for the remote worker) and the UI
	// would render Download for variants the worker can't load.
	gate := make(chan struct{})
	spawn := func(ctx context.Context, _ string) (workers.Handle, error) {
		<-gate // hold here until the test releases us
		return newPlatformFake(workers.Platform{
			OS:                "linux",
			Arch:              "amd64",
			AvailableBackends: []string{"torch"},
			DownloadedRepos:   []string{"kyutai/moshiko-pytorch-bf16"},
		}), nil
	}
	m := newManagerWithSpawn(spawn)
	m.remote = &workers.RemoteEndpoint{URL: "grpcs://example.test:7843", Token: "t"}
	// Reset the platform-ready signal to mimic NewManager's remote path.
	m.platformReady = make(chan struct{})
	m.platformReadyOnce = onceReset()
	go m.probeRemotePlatform()

	// Before releasing the gate, Platform should report not-ready.
	if _, _, ready := m.Platform(); ready {
		t.Fatal("Platform reported ready before probe completed")
	}

	close(gate)

	// After the probe runs, ready flips and the worker's reported
	// values land.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, _, ready := m.Platform(); ready {
			break
		}
		time.Sleep(2 * time.Millisecond)
	}
	host, arch, ready := m.Platform()
	if !ready {
		t.Fatal("Platform never became ready")
	}
	if host != "linux" || arch != "amd64" {
		t.Errorf("Platform = %s/%s, want linux/amd64", host, arch)
	}
	dl := m.DownloadedRepos()
	if !dl["kyutai/moshiko-pytorch-bf16"] {
		t.Errorf("downloadedRepos = %v, want torch repo present", dl)
	}
}

func TestPlatform_RemoteProbeFailure_StillMarksReady(t *testing.T) {
	// REASON: a failing probe should NOT pin /models in the loading
	// state forever — the user might have just not started the remote
	// worker yet. We mark ready (with empty platform) so the UI can at
	// least render an explanatory empty state.
	probeErr := errors.New("dial refused")
	spawn := func(_ context.Context, _ string) (workers.Handle, error) {
		return nil, probeErr
	}
	m := newManagerWithSpawn(spawn)
	m.remote = &workers.RemoteEndpoint{URL: "grpcs://example.test:7843", Token: "t"}
	m.platformReady = make(chan struct{})
	m.platformReadyOnce = onceReset()

	m.probeRemotePlatform()

	// Should be marked ready even though platform values are empty.
	host, arch, ready := m.Platform()
	if !ready {
		t.Fatal("ready never flipped after failed probe")
	}
	if host != "" || arch != "" {
		t.Errorf("expected empty platform after failed probe; got %s/%s", host, arch)
	}
}

func TestDownloadDone_AddsToDownloadedSet(t *testing.T) {
	// REASON: the second half of the bug — when a remote download
	// completes, /models must immediately reflect it. handleEvent's
	// download_done branch should add the repo to m.downloadedRepos so
	// the next /models call returns Downloaded=true without waiting
	// for a fresh handshake.
	m := newTestManager(&fakeWorker{})
	// Force remote mode so DownloadedRepos returns the in-memory set
	// rather than nil.
	m.remote = &workers.RemoteEndpoint{URL: "grpcs://example.test:7843", Token: "t"}

	m.handleEvent(map[string]any{
		"event": "download_done",
		"name":  "moshi",
		"repo":  "kyutai/moshiko-pytorch-bf16",
	})

	dl := m.DownloadedRepos()
	if !dl["kyutai/moshiko-pytorch-bf16"] {
		t.Errorf("downloadedRepos after download_done = %v, want the torch repo", dl)
	}
}

// onceReset returns a fresh sync.Once, used by tests that re-arm the
// "platform ready" gate after constructing the Manager. The default
// constructor closes the channel (local-mode behavior); these tests
// explicitly want the remote-mode timing.
func onceReset() (o oncePlaceholder) { return }

type oncePlaceholder = sync.Once

// counter is a tiny atomic helper so a couple of tests above can
// assert "spawn was called n times" without touching workers/.
type counter struct{ n atomic.Int32 }

func (c *counter) inc() { c.n.Add(1) }
func (c *counter) get() int32 { return c.n.Load() }
