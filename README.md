# Cypress

Local-first desktop app for duplex voice AI with agentic capabilities. Think Ollama / LM Studio, but for voice agents — load a model, talk to it, let it use tools, all on your own hardware.

Built by [ARTIFACT](https://github.com/ARTIFACT-CX).

## Status

v0.1 done. End-to-end voice loop works on Apple Silicon: pick Moshi from the model selector, the Go server downloads weights from Hugging Face with live progress, the Python worker loads them on MLX, and you can hold push-to-talk for a duplex conversation.

v0.2 in progress. Remote workers can run on a separate GPU box (or RunPod) and Cypress dials them over gRPC + TLS — the local app, server, and protocol are all the same; only the worker moves. See [Remote workers](#remote-workers).

What's there today:

- macOS client (Apple Silicon — MLX backend by default; torch/MPS fallback). Remote workers can run on Linux/CUDA via Docker.
- One model loaded at a time. Hot-swapping families is fine; concurrent models are not.
- Moshi 3.5B is the only fully-wired model. PersonaPlex 7B is scaffolded (catalog entry, per-family Python env) but the loader isn't implemented — see [#3](https://github.com/ARTIFACT-CX/cypress/issues/3).
- Push-to-talk only. Always-on listening, tool calling, and personas are upcoming work.
- Dev-only — no signed bundle, no installer. Run from source.

What's next:

- Remainder of v0.2 — PersonaPlex loader, agentic tool calling, in-app settings UI.
- v1.0 — bundled distributable app (signed + notarized DMG, CI, README polish).

## Architecture

```
app/      Tauri shell (React + TypeScript + Rust)
  ↕       WebSocket over localhost — audio streams
server/   Go orchestration (model lifecycle, downloads, routing)
  ↕       gRPC bidi — Unix socket (local) or TCP+TLS (remote)
worker/   Python inference runtime (PyTorch / MLX)
  ↕
          Local model weights on disk (~/.cache/huggingface)
```

Three processes, one box for local dev. The Rust shell owns the UI and manages the Go server's lifecycle. The Go server either spawns a local Python worker or dials a remote one — the rest of the system can't tell which. The Python worker does the actual model work. Each model family gets its own venv (`worker/models/<family>/.venv`) so conflicting Python deps stay isolated. See [Remote workers](#remote-workers) for offloading inference to a GPU box.

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

Only needed if you edit `proto/worker.proto`:

- `brew install protobuf` (provides `protoc`)
- `go install google.golang.org/protobuf/cmd/protoc-gen-go@latest`
- `go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@latest`

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
# Use a family's venv directly so its model deps are on the path.
models/moshi/.venv/bin/python main.py --family moshi
```

### Tests

```sh
cd server && go test ./...                      # Go unit suite
cd worker && uv run pytest                      # Python unit suite
cd server && go test -tags=integration ./...    # Integration (real worker subprocess)
```

## Remote workers

If your local hardware can't fit a model, or you would like to use cloud GPUs for better performance, Cypress can dial a worker running on a separate machine over gRPC. The local app, the Go server, and the protocol are all the same; only the worker moves.

Two deployment paths are supported in v0.2:

- **RunPod (or any container host)** — public network, TLS + bearer token.
- **SSH tunnel** (BYO GPU box, GT Phoenix, lab workstation) — loopback only, bearer token.

Pick whichever matches where the GPU lives.

### Generate a shared token

Both paths use the same scheme: the worker validates an `Authorization: Bearer <token>` header on every gRPC call. Generate a token once and use it on both ends:

```sh
openssl rand -hex 32
```

Treat it like an API key. Anyone with the token can drive the worker; anyone with `(token, network access)` can open a session. Don't commit it.

### Path A — RunPod / public container host

This path runs the worker as a Docker container behind RunPod's HTTPS proxy (or any TLS-terminating ingress).

1. **Build the image** from a checkout of this repo. One image per family — the Moshi family's deps and PersonaPlex's NVIDIA fork can't coexist:

   ```sh
   docker build --build-arg FAMILY=moshi \
                -f worker/Dockerfile -t cypress-worker-moshi .
   ```

   For PersonaPlex, add `--build-arg EXTRA_APT=git` (its `moshi` dep is git-sourced).

2. **Push to a registry** RunPod can pull from (Docker Hub, GHCR, ECR, etc.). Cypress doesn't publish images yet — you build and push your own.

3. **Spin up a pod**, exposing port `7843`. Set the `CYPRESS_TOKEN` env var to the token you generated. RunPod's HTTPS proxy terminates TLS for you, so no `--tls` flag is needed inside the container — the entrypoint already binds `tcp://0.0.0.0:7843` and the proxy adds TLS.

4. **Mount a volume** at `/var/cache/huggingface` so a restarted container doesn't redownload the 5–9 GB Moshi weights (~16 GB for PersonaPlex).

5. **On your laptop**, point Cypress at the proxied URL:

   ```sh
   export CYPRESS_REMOTE_URL=grpcs://<your-pod>.proxy.runpod.net:443
   export CYPRESS_REMOTE_TOKEN=<the-token>
   cd app && npm run desktop
   ```

The Go server detects both env vars and dials the remote worker instead of spawning a local subprocess. The catalog UI works the same; "load Moshi" downloads the weights to the *pod*, not your laptop.

If you're hosting on a box you control directly (no proxy), terminate TLS at the worker. Mount `cert.pem` + `key.pem` into the container and append `--tls /certs/cert.pem /certs/key.pem` to the entrypoint. A 90-day Let's Encrypt cert via DNS-01 is the simplest path.

### Path B — SSH tunnel to a GPU box

For workstations or HPC environments where you don't want to expose a public port. The worker binds loopback-only on the remote box; you tunnel `localhost:7843` over SSH.

1. **On the GPU host**, clone the repo and sync the family venv:

   ```sh
   git clone https://github.com/ARTIFACT-CX/cypress.git
   cd cypress/worker/models/moshi
   uv sync
   ```

2. **Run the worker** bound to loopback:

   ```sh
   cd ../..   # back to worker/
   models/moshi/.venv/bin/python main.py \
     --listen tcp://127.0.0.1:7843 \
     --family moshi \
     --token <the-token>
   ```

   No `--tls` is required because the listener is loopback-only — the bearer token never crosses the network in clear text. The SSH transport is the encryption layer.

3. **From your laptop**, open the tunnel and start Cypress:

   ```sh
   ssh -N -L 7843:localhost:7843 <user>@<gpu-host> &
   export CYPRESS_REMOTE_URL=tcp://localhost:7843
   export CYPRESS_REMOTE_TOKEN=<the-token>
   cd app && npm run desktop
   ```

   The `tcp://` scheme is accepted because the resolved host is loopback. Any non-loopback host with `tcp://` is refused at startup — that's the gate that keeps the token off the wire by accident.

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

Apache License 2.0 — see [LICENSE](./LICENSE).

You're free to use, modify, distribute, and build commercial products on top
of this code; the license requires you to preserve copyright notices and
ships an explicit patent grant. Contributions to this repo are accepted
under the same terms.
