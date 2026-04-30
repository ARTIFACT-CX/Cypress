"""
AREA: worker · PLATFORM-INFO
Snapshot of the worker's runtime characteristics — built once at boot
and stamped into every gRPC Handshake. The Go host uses these fields
to pick model variants (MLX vs torch, quantization, repo) that match
the *worker's* platform rather than the laptop's. Without this, an
Apple-Silicon laptop dialing a Linux GPU worker would tell it to
download MLX weights it can't load — see issue tracker for the bug
this fixes.

The snapshot is intentionally simple:

  os                  — platform.system() lowered ("linux" | "darwin").
  arch                — platform.machine() normalized ("amd64" | "arm64").
  available_backends  — which model backends import without error in
                        this venv. Each is probe-imported once at boot
                        so we don't lie based on platform alone (e.g.
                        a darwin/arm64 venv that hasn't installed mlx).
  downloaded_repos    — HF repo IDs ("owner/name") fully cached on
                        disk in HF_HOME. One-time scan; the host
                        keeps it current via download_done events for
                        the rest of the session.
"""

from __future__ import annotations

import importlib.util
import os
import pathlib
import platform


# --- Platform tuple ----------------------------------------------------------


def _normalize_arch(machine: str) -> str:
    """Map platform.machine() → Go-style arch names. We mirror Go's GOARCH
    so the Go side can compare strings without a translation table."""
    m = machine.lower()
    if m in ("x86_64", "amd64"):
        return "amd64"
    if m in ("aarch64", "arm64"):
        return "arm64"
    if m.startswith("armv7"):
        return "arm"
    return m or "unknown"


def os_name() -> str:
    return platform.system().lower()


def arch_name() -> str:
    return _normalize_arch(platform.machine())


# --- Backend probe -----------------------------------------------------------
#
# Per-family map of backend label → top-level Python module that has to
# import successfully for the backend to count as "available." We probe
# with importlib.util.find_spec so we don't pay the cost of actually
# importing heavy deps at boot — the spec check answers "is this
# installed?" without running module-level code.

_BACKEND_PROBES: dict[str, dict[str, str]] = {
    "moshi": {
        "torch": "torch",
        "mlx": "mlx.core",
    },
    # PersonaPlex backends will land here once the loader is implemented.
}


def available_backends(family: str) -> list[str]:
    """Return the subset of the family's backends whose underlying
    framework is importable in this venv. Order is deterministic: every
    backend the family declares, in declaration order, filtered by
    install state. Empty list if the family is unknown."""
    probes = _BACKEND_PROBES.get(family)
    if not probes:
        return []
    out: list[str] = []
    for label, module in probes.items():
        # SAFETY: find_spec("mlx.core") tries to import the parent
        # package "mlx" first; if mlx isn't installed at all, that
        # raises ModuleNotFoundError instead of returning None. Wrap
        # so an absent backend just falls out of the list.
        try:
            spec = importlib.util.find_spec(module)
        except (ImportError, ValueError):
            spec = None
        if spec is not None:
            out.append(label)
    return out


# --- HF cache scan -----------------------------------------------------------


def _hf_cache_root() -> pathlib.Path:
    """Resolve the HF hub cache the same way huggingface_hub does. We
    can't import huggingface_hub here (it isn't in the shared worker
    venv, only in per-family ones) — but the env-var precedence is
    documented and stable: HUGGINGFACE_HUB_CACHE > HF_HOME/hub > default."""
    explicit = os.environ.get("HUGGINGFACE_HUB_CACHE")
    if explicit:
        return pathlib.Path(explicit).expanduser()
    hf_home = os.environ.get("HF_HOME")
    if hf_home:
        return pathlib.Path(hf_home).expanduser() / "hub"
    return pathlib.Path("~/.cache/huggingface/hub").expanduser()


def _is_complete_snapshot(repo_dir: pathlib.Path) -> bool:
    """A repo is "downloaded" if at least one snapshot exists and no
    sibling .incomplete files are left. Mirrors the Go side's
    IsRepoCached probe so both sides agree on what counts as ready."""
    snapshots = repo_dir / "snapshots"
    if not snapshots.is_dir():
        return False
    has_snapshot = any(p.is_dir() for p in snapshots.iterdir())
    if not has_snapshot:
        return False
    blobs = repo_dir / "blobs"
    if blobs.is_dir():
        for entry in blobs.iterdir():
            if entry.name.endswith(".incomplete"):
                return False
    return True


def downloaded_repos() -> list[str]:
    """Walk the HF cache directory, return the list of fully-cached
    repos in 'owner/name' form. Skips partial downloads (anything with
    a *.incomplete blob) so the host doesn't think a half-pulled repo
    is ready to load."""
    root = _hf_cache_root()
    if not root.is_dir():
        return []
    out: list[str] = []
    for entry in root.iterdir():
        # HF naming: models--<owner>--<name> for model repos; we ignore
        # datasets-- and spaces-- because the catalog only references
        # model repos today.
        if not entry.is_dir() or not entry.name.startswith("models--"):
            continue
        if not _is_complete_snapshot(entry):
            continue
        parts = entry.name[len("models--") :].split("--")
        if len(parts) < 2:
            continue
        owner, name = parts[0], "-".join(parts[1:])
        out.append(f"{owner}/{name}")
    out.sort()
    return out


# --- Snapshot builder --------------------------------------------------------


def gather(family: str) -> dict:
    """Collect every field the gRPC Handshake carries. Returned as a
    plain dict so the IPC layer can splat it into the protobuf without
    importing this module's types."""
    return {
        "os": os_name(),
        "arch": arch_name(),
        "available_backends": available_backends(family),
        "downloaded_repos": downloaded_repos(),
    }
