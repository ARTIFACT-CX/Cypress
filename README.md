# Cypress

Local-first desktop app for duplex voice AI with agentic capabilities. Think Ollama / LM Studio, but for voice agents — load a model, talk to it, let it use tools, all on your own hardware.

Built by [ARTIFACT](https://github.com/ARTIFACT-CX).

## Status

v0.1 in progress. Single platform (macOS), single model at a time, push-to-talk first. Shipping rough in public.

## Architecture

```
app/      Tauri shell (React + TypeScript + Rust)
  ↕       WebSocket over localhost — audio streams
server/   Go orchestration (model lifecycle, routing)
  ↕       subprocess + JSON-line IPC + unix socket
worker/   Python inference runtime (PyTorch)
  ↕
          Local model weights on disk
```

Three processes, one machine. The Rust shell owns the UI and manages the Go server's lifecycle. The Go server manages the Python worker's lifecycle. The Python worker does the actual model work.

## Models

Planned support:

| Model | Size | Notes |
| --- | --- | --- |
| Moshi | 3.5B | Default, lighter, full duplex |
| PersonaPlex | 7B | Flagship, duplex + persona/role conditioning |
| Kokoro | 82M | TTS only, runs on almost anything |
| Orpheus | 3B | TTS alternative |

Loaders are not yet implemented — clicking a model button currently returns a not-yet-implemented error from the worker. Wiring up the first real loader is the next milestone.

## Development

You'll need:

- Node 24 LTS
- Rust (stable) + Xcode command-line tools (for Tauri on macOS)
- Go 1.22+
- [uv](https://docs.astral.sh/uv/) (manages the Python worker's env)

Install and run the app:

```sh
cd app
npm install
npm run tauri dev
```

The Tauri shell launches; clicking **Start Server** spawns the Go server on `127.0.0.1:7842`, and clicking a model button lazily spawns the Python worker and asks it to load. Errors from spawn, handshake, and the loader all surface in the UI.

To run the server standalone (for curl-testing the HTTP endpoints):

```sh
cd server
go run .
```

The worker is driven by the server, but you can also run it directly over stdin JSON for debugging:

```sh
cd worker
uv run python main.py
```

## Repo layout

```
app/           Tauri app — React frontend, Rust shell
server/        Go orchestration server
worker/        Python inference worker (uv-managed)
assets/        Logo, icon sources
.claude/       AI collaboration guide (CLAUDE.md)
```

## License

TBD — open-source core planned.
