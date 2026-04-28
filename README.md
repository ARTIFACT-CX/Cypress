# Cypress

Local-first desktop app for duplex voice AI with agentic capabilities. Think Ollama / LM Studio, but for voice agents — load a model, talk to it, let it use tools, all on your own hardware.

Built by [ARTIFACT](https://github.com/ARTIFACT-CX).

## Status

v0.1 done. End-to-end voice loop works on Apple Silicon: pick Moshi from the model selector, the Go server downloads weights from Hugging Face with live progress, the Python worker loads them on MLX, and you can hold push-to-talk for a duplex conversation.

What's there today:

- macOS only (Apple Silicon — MLX backend by default; torch/MPS fallback).
- One model loaded at a time. Hot-swapping families is fine; concurrent models are not.
- Moshi 3.5B is the only fully-wired model. PersonaPlex 7B is scaffolded (catalog entry, per-family Python env) but the loader isn't implemented — see [#3](https://github.com/ARTIFACT-CX/cypress/issues/3).
- Push-to-talk only. Always-on listening, tool calling, and personas are upcoming work.
- Dev-only — no signed bundle, no installer. Run from source.

What's next:

- v0.2 — cloud inference (offload to RunPod / GT Phoenix / BYO GPU when local hardware can't fit a model), gRPC transport unification, PersonaPlex loader, agentic tool calling.
- v1.0 — bundled distributable app (signed + notarized DMG, CI, README polish).

## Architecture

```
app/      Tauri shell (React + TypeScript + Rust)
  ↕       WebSocket over localhost — audio streams
server/   Go orchestration (model lifecycle, downloads, routing)
  ↕       subprocess + JSON-line IPC + unix socket
worker/   Python inference runtime (PyTorch / MLX)
  ↕
          Local model weights on disk (~/.cache/huggingface)
```

Three processes, one machine. The Rust shell owns the UI and manages the Go server's lifecycle. The Go server manages the Python worker's lifecycle. The Python worker does the actual model work. Each model family gets its own venv (`worker/models/<family>/.venv`) so conflicting Python deps stay isolated.

## Models

| Model | Size | Status | Notes |
| --- | --- | --- | --- |
| Moshi | 3.5B | ✅ Working | Default. MLX on Apple Silicon, torch elsewhere. Q4/Q8/bf16 selectable via `CYPRESS_MOSHI_REPO`. |
| PersonaPlex | 7B | 🚧 Scaffolded | NVIDIA fork; loader not yet implemented (#3). Likely needs INT4 to fit on M-series 16GB. |
| Kokoro | 82M | 📋 Planned | TTS only. |
| Orpheus | 3B | 📋 Planned | TTS alternative. |

## Development

You'll need:

- Node 24 LTS
- Rust (stable) + Xcode command-line tools
- Go 1.22+
- [uv](https://docs.astral.sh/uv/) — manages the per-family Python envs
- An Apple Silicon Mac (Intel + Linux/Windows untested)

Install and run:

```sh
cd app
npm install
npm run desktop
```

The Tauri shell launches; clicking **Start Server** spawns the Go server on `127.0.0.1:7842`. Pick a model from the selector — first launch downloads weights (~5–9 GB for Moshi depending on quant) with live progress, then loads onto the device. Once the model is ready, the Talk page lets you hold the mic button for duplex audio.

### Optional env overrides

Copy `.env.example` to `.env` if you want to tweak defaults (force a backend, switch quantization, point at a different HF repo, etc.). Nothing in there is required — sensible defaults ship in code.

### Running pieces standalone

For curl-testing the HTTP endpoints:

```sh
cd server
go run .
```

For poking the worker over JSON-line IPC directly:

```sh
cd worker
uv run python main.py
```

### Tests

```sh
cd server && go test ./...                      # Go unit suite
cd worker && uv run pytest                      # Python unit suite
cd server && go test -tags=integration ./...    # Integration (real worker subprocess)
```

## Repo layout

```
app/           Tauri app — React frontend, Rust shell
server/        Go orchestration server (feature-sliced: inference/, ...)
worker/        Python inference worker (uv workspace; per-family packages under models/)
assets/        Logo, icon sources
.claude/       AI collaboration guide (CLAUDE.md)
```

See [`.claude/CLAUDE.md`](./.claude/CLAUDE.md) for the architectural conventions (feature-sliced layout, per-feature tests, the `// AREA:` tag convention, etc.).

## License

TBD — open-source core planned.
