// AREA: inference · CATALOG
// Static metadata for the set of models Cypress knows how to load,
// plus a filesystem probe of the Hugging Face hub cache so the UI can
// show "downloaded" vs "needs download" before the user clicks load.
//
// The catalog mirrors what the Python worker actually loads — the
// repo names here have to match worker/models/*.py defaults. Keeping
// it Go-side means the picker UI can render this list without first
// spinning up the Python worker (which costs several seconds and only
// runs on demand). The trade-off is that the two sides need to stay
// in sync; there's exactly one knob today (CYPRESS_MOSHI_REPO) and we
// document the linkage in comments.
//
// Download status is "best effort" — we look for the canonical HF hub
// cache layout (~/.cache/huggingface/hub/models--<org>--<name>/) and
// report `downloaded: true` if any snapshot directory under that path
// is non-empty. We don't verify individual file hashes; HF's own
// loaders will do that at load time and trigger a refetch if needed.

package inference

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// ModelInfo is the per-model row returned by GET /models. Field names
// are the exact JSON shape the UI consumes.
type ModelInfo struct {
	Name         string `json:"name"`         // identifier passed to /model/load
	Label        string `json:"label"`        // display name
	Hint         string `json:"hint"`         // short tagline (parameters · capability)
	Backend      string `json:"backend"`      // "mlx", "torch", "—"
	Repo         string `json:"repo"`         // HF repo (e.g. "kyutai/moshiko-mlx-q8")
	SizeGB       string `json:"sizeGb"`       // approximate disk + RAM footprint
	Requirements string `json:"requirements"` // human-readable ram/vram + device hints
	Available    bool   `json:"available"`    // false = visible but not yet implemented
	Downloaded   bool   `json:"downloaded"`   // weights present in HF cache
}

// catalogEntry is the static description; the dynamic Downloaded flag
// is computed per request. Kept private so callers can't accidentally
// mutate the catalog.
type catalogEntry struct {
	Name         string
	Label        string
	Hint         string
	Backend      string
	Repo         string
	SizeGB       string
	Requirements string
	Available    bool
}

// REASON: backend selection mirrors worker/models/__init__.py's
// _default_moshi_backend. We can't import that logic, so we duplicate
// the platform check; if the worker logic ever grows more complex
// we'll need an IPC query instead.
func defaultMoshiEntry() catalogEntry {
	if runtime.GOOS == "darwin" && runtime.GOARCH == "arm64" {
		return catalogEntry{
			Name:         "moshi",
			Label:        "Moshi",
			Hint:         "3.5B · duplex · lighter",
			Backend:      "mlx",
			Repo:         "kyutai/moshiko-mlx-q8",
			SizeGB:       "~4 GB",
			Requirements: "~6 GB unified memory · Apple Silicon",
			Available:    true,
		}
	}
	return catalogEntry{
		Name:         "moshi",
		Label:        "Moshi",
		Hint:         "3.5B · duplex · lighter",
		Backend:      "torch",
		Repo:         "kyutai/moshiko-pytorch-bf16",
		SizeGB:       "~14 GB",
		Requirements: "~14 GB VRAM · CUDA preferred",
		Available:    true,
	}
}

// catalog returns the static set; Available reflects whether the
// worker has a registered loader. PersonaPlex is listed so users see
// what's coming but can't click it yet — the moment its loader lands
// in the worker we just flip Available.
func catalog() []catalogEntry {
	return []catalogEntry{
		defaultMoshiEntry(),
		{
			Name:         "personaplex",
			Label:        "PersonaPlex",
			Hint:         "7B · duplex + persona",
			Backend:      "torch",
			Repo:         "", // not yet published
			SizeGB:       "~14 GB",
			Requirements: "~16 GB VRAM · NVIDIA",
			Available:    false,
		},
	}
}

// ModelInfos returns the catalog with download status filled in.
// Computed fresh on each call — the cache directory could change
// between calls if the user kicks off a load.
func ModelInfos() []ModelInfo {
	root := hubCacheDir()
	out := make([]ModelInfo, 0, len(catalog()))
	for _, e := range catalog() {
		out = append(out, ModelInfo{
			Name:         e.Name,
			Label:        e.Label,
			Hint:         e.Hint,
			Backend:      e.Backend,
			Repo:         e.Repo,
			SizeGB:       e.SizeGB,
			Requirements: e.Requirements,
			Available:    e.Available,
			Downloaded:   isRepoCached(root, e.Repo),
		})
	}
	return out
}

// hubCacheDir resolves the HF hub cache root. Honors HF_HOME and
// HUGGINGFACE_HUB_CACHE so a user with a custom cache path doesn't
// see false negatives.
func hubCacheDir() string {
	if v := os.Getenv("HUGGINGFACE_HUB_CACHE"); v != "" {
		return v
	}
	if v := os.Getenv("HF_HOME"); v != "" {
		return filepath.Join(v, "hub")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".cache", "huggingface", "hub")
}

// isRepoCached probes HF's on-disk cache for any non-empty snapshot of
// the given repo. The cache layout is:
//
//	<root>/models--<org>--<name>/snapshots/<sha>/<files>
//
// We treat the presence of any snapshot containing at least one entry
// as "downloaded enough to attempt a load". HF's loader will refetch
// missing files on demand if our probe over-reports.
func isRepoCached(root, repo string) bool {
	if root == "" || repo == "" {
		return false
	}
	// repo e.g. "kyutai/moshiko-mlx-q8" → "models--kyutai--moshiko-mlx-q8".
	dirName := "models--" + strings.ReplaceAll(repo, "/", "--")
	snapshots := filepath.Join(root, dirName, "snapshots")
	entries, err := os.ReadDir(snapshots)
	if err != nil {
		return false
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		inner, err := os.ReadDir(filepath.Join(snapshots, e.Name()))
		if err != nil {
			continue
		}
		if len(inner) > 0 {
			return true
		}
	}
	return false
}
