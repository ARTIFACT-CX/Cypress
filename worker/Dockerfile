# AREA: worker · DEPLOY
# Cypress worker container, parameterized by model family. Build from
# the repo root:
#
#   docker build --build-arg FAMILY=moshi \
#                -f worker/Dockerfile -t cypress-worker-moshi .
#
#   docker build --build-arg FAMILY=personaplex --build-arg EXTRA_APT=git \
#                -f worker/Dockerfile -t cypress-worker-personaplex .
#
# One file covers every family — they only differ in which per-family
# pyproject feeds `uv sync` and which apt packages the build needs.
# Adding a new family (Kokoro, Orpheus) is a build-arg change.
#
# Two stages:
#   1. builder — uv resolves the family's deps into /opt/venv.
#   2. runtime — slim image with the venv copied in, worker source on
#      PYTHONPATH, main.py as the entrypoint.
#
# This image targets Linux/CUDA hosts (RunPod, Phoenix, BYO GPU). The
# moshi family's MLX dep is gated to darwin in its pyproject so
# `uv sync` skips it here automatically.

ARG FAMILY
# REASON: extra apt packages needed at *build* time for the family's
# deps. PersonaPlex pulls NVIDIA's moshi fork via git+ so it needs git;
# moshi's pure-PyPI deps need nothing. Default empty.
ARG EXTRA_APT=""

# REASON: uv's official slim image tracks Python 3.12 which matches
# what we test the worker against. Bookworm (Debian) is the path of
# least resistance for torch + CUDA wheels.
FROM ghcr.io/astral-sh/uv:python3.12-bookworm-slim AS builder
ARG FAMILY
ARG EXTRA_APT

# REASON: setting these in the build stage avoids uv creating a venv
# in a path that won't exist in the runtime stage.
ENV UV_PROJECT_ENVIRONMENT=/opt/venv \
    UV_LINK_MODE=copy \
    UV_COMPILE_BYTECODE=1

# REASON: install build-time apt packages only when EXTRA_APT is set,
# so families with no native build deps don't pay for an apt layer.
RUN if [ -n "$EXTRA_APT" ]; then \
      apt-get update && \
      apt-get install -y --no-install-recommends $EXTRA_APT && \
      rm -rf /var/lib/apt/lists/*; \
    fi

WORKDIR /build

# STEP 1: copy only the family pyproject + lockfile first so docker
# layer-caches the dep resolve across source-only changes.
COPY worker/models/${FAMILY}/pyproject.toml worker/models/${FAMILY}/uv.lock* ./
# REASON: --frozen is non-negotiable in CI builds — if the lock is
# stale, fail loud rather than silently regenerating it and shipping a
# different dep set than dev tested against.
RUN uv sync --frozen --no-install-project --no-dev

# Runtime image: smaller, no uv binary, just python + the prebuilt venv.
FROM python:3.12-slim-bookworm AS runtime
ARG FAMILY

# SETUP: non-root user. Containers shouldn't run as root by default.
RUN groupadd --system cypress && useradd --system --gid cypress --create-home cypress

# REASON: PYTHONUNBUFFERED so logs flush immediately — without it,
# docker logs lag stdout buffering and a crashed worker shows no
# trail. PYTHONDONTWRITEBYTECODE keeps the runtime FS clean.
ENV PYTHONUNBUFFERED=1 \
    PYTHONDONTWRITEBYTECODE=1 \
    PATH="/opt/venv/bin:$PATH" \
    PYTHONPATH=/app/worker

COPY --from=builder /opt/venv /opt/venv

WORKDIR /app

# STEP 2: copy the shared worker scaffold + the family subpackage. The
# proto stubs come along too — main.py adds proto/dist/python to
# sys.path at startup so the workerpb import resolves. A
# .dockerignore at the repo root keeps per-family .venv dirs and
# __pycache__ out of the build context.
COPY worker/ /app/worker/
COPY proto/dist/python/ /app/proto/dist/python/

USER cypress

# REASON: 7843 is the documented default in the host's
# CYPRESS_REMOTE_URL examples. Operators bind to whatever port the
# environment dictates — this is a hint, not a constraint.
EXPOSE 7843

WORKDIR /app/worker

# REASON: model weights are large (5–9 GB Moshi, ~16 GB PersonaPlex);
# operators mount a persistent volume here so a restarted container
# doesn't redownload. huggingface_hub honors HF_HOME for cache placement.
ENV HF_HOME=/var/cache/huggingface
VOLUME ["/var/cache/huggingface"]

# REASON: --listen / --token / --tls come from the runtime env so the
# same image works for loopback (SSH-tunneled) and public deployments
# without a rebuild. CYPRESS_FAMILY is baked from the build arg so
# operators don't have to repeat the family at run time.
ENV CYPRESS_FAMILY=${FAMILY}
ENTRYPOINT ["python", "main.py", "--listen", "tcp://0.0.0.0:7843"]
