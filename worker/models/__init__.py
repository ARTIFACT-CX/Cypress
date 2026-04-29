"""
AREA: worker · MODELS

Package of voice-model implementations. Each model family lives in its
own subpackage (moshi/, personaplex/, …) and registers its concrete
classes via the `register` decorator in base.py. The command handler
looks models up by name in REGISTRY.

Family loading is explicit: the composition root calls `load_family(name)`
once at startup. Eager imports across all families don't work because
each family runs out of its own per-family venv with conflicting deps
(kyutai vs NVIDIA `moshi` package collision is the canonical case),
and a Docker image only ships one family's stack.
"""

import importlib

from .base import Model, REGISTRY, register


def load_family(name: str) -> None:
    """Import the named family's subpackage so its @register decorators
    run and REGISTRY is populated. Idempotent: re-importing a loaded
    module is a no-op."""
    if not name:
        raise ValueError("load_family: name is required")
    # REASON: relative import via importlib (rather than __import__) so
    # the dotted form `models.moshi` resolves predictably regardless of
    # how the caller imported `models`.
    importlib.import_module(f".{name}", package=__name__)


__all__ = ["Model", "REGISTRY", "register", "load_family"]
