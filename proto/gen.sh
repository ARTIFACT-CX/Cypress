#!/usr/bin/env bash
# AREA: proto · CODEGEN
# Regenerate Go and Python stubs from worker.proto. Run from repo root or
# from anywhere; the script anchors to its own location.
#
# Prereqs:
#   - protoc on PATH (brew install protobuf)
#   - protoc-gen-go + protoc-gen-go-grpc on PATH
#       go install google.golang.org/protobuf/cmd/protoc-gen-go@latest
#       go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@latest
#   - grpcio-tools available in some Python (we shell into uv to find it)
#
# Outputs:
#   proto/dist/go/workerpb/worker.pb.go
#   proto/dist/go/workerpb/worker_grpc.pb.go
#   proto/dist/python/workerpb/worker_pb2.py
#   proto/dist/python/workerpb/worker_pb2_grpc.py

set -euo pipefail

PROTO_DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_ROOT="$(cd "$PROTO_DIR/.." && pwd)"
PROTO_FILE="worker.proto"

GO_OUT="$PROTO_DIR/dist/go/workerpb"
PY_OUT="$PROTO_DIR/dist/python/workerpb"

mkdir -p "$GO_OUT" "$PY_OUT"

echo ">>> Generating Go stubs → $GO_OUT"
protoc \
  --proto_path="$PROTO_DIR" \
  --go_out="$PROTO_DIR/dist/go" \
  --go_opt=module=github.com/ARTIFACT-CX/cypress/proto/dist/go \
  --go-grpc_out="$PROTO_DIR/dist/go" \
  --go-grpc_opt=module=github.com/ARTIFACT-CX/cypress/proto/dist/go \
  "$PROTO_FILE"

echo ">>> Generating Python stubs → $PY_OUT"
# REASON: grpcio-tools ships protoc bindings + the gRPC plugin. Pulled
# in via the worker's own venv so we don't depend on a global Python.
# Falls back to any python3 with grpc_tools installed if uv isn't around.
if command -v uv >/dev/null 2>&1 && [ -f "$REPO_ROOT/worker/pyproject.toml" ]; then
  cd "$REPO_ROOT/worker"
  uv run --with grpcio-tools python -m grpc_tools.protoc \
    --proto_path="$PROTO_DIR" \
    --python_out="$PY_OUT" \
    --grpc_python_out="$PY_OUT" \
    --pyi_out="$PY_OUT" \
    "$PROTO_FILE"
  cd - >/dev/null
else
  python3 -m grpc_tools.protoc \
    --proto_path="$PROTO_DIR" \
    --python_out="$PY_OUT" \
    --grpc_python_out="$PY_OUT" \
    --pyi_out="$PY_OUT" \
    "$PROTO_FILE"
fi

# REASON: grpcio-tools generates `import worker_pb2` (flat, top-level),
# which only works if PY_OUT itself is on sys.path. Rewrite to a sibling-
# relative import so consumers can `from workerpb import worker_pb2_grpc`
# — matching how main.py / conftest.py put PY_OUT's parent on sys.path.
# sed -i differs between BSD (macOS) and GNU; portable two-arg form.
if [ -f "$PY_OUT/worker_pb2_grpc.py" ]; then
  sed -i.bak 's|^import worker_pb2 as|from . import worker_pb2 as|' \
    "$PY_OUT/worker_pb2_grpc.py"
  rm -f "$PY_OUT/worker_pb2_grpc.py.bak"
fi

# Make Python see workerpb as a package.
touch "$PY_OUT/__init__.py"

echo ">>> Done."
