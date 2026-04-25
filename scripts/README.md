# scripts/

Local-only sandbox for smoke tests, sample audio, and throwaway repro
snippets. Everything in here is gitignored except this README — the
folder is yours to scribble in without polluting the tree.

If something here proves reusable, graduate it into the feature folder
it belongs to (e.g. `server/audio/` for an audio path test, with a
proper `*_test.go` / `test_*.py` next to the code).

## What lives here

- `ws_smoke.py` — end-to-end WebSocket smoke test. Streams a wav
  through the running server and writes the model's reply to disk.
  Self-contained via PEP 723 inline metadata (run with `uv`).
- `in.wav` / `out.wav` — input + output for `ws_smoke.py`. Generate
  `in.wav` from text with `npm run say -- "your prompt here"` (run from
  `app/`); the script writes a 24kHz int16 mono wav straight into this
  folder.

## Typical loop

```sh
# 1. start the desktop app (spawns the Go server + Python worker)
cd app && npm run desktop

# 2. once a model is loaded in the UI, in another shell:
npm run say -- "hello, how are you"
./scripts/ws_smoke.py scripts/in.wav scripts/out.wav

# 3. listen
afplay scripts/out.wav
```
