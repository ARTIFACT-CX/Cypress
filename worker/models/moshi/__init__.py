"""
AREA: worker · MODELS · MOSHI

Moshi backends. Two concrete loaders live here:

  - torch.py: PyTorch / MPS / CUDA path (kyutai/moshi).
  - mlx.py:   Apple-Silicon-native MLX path (kyutai/moshi-mlx).

Importing this subpackage registers both classes in the top-level
REGISTRY and wires the `"moshi"` alias to whichever backend is the
right default for the current host.

Backend autoselect: MLX on Apple Silicon (q8 default for usable
real-time perf), PyTorch elsewhere. CYPRESS_MOSHI_BACKEND=mlx|torch
overrides the autoselect; a v0.2 settings UI (#21) will surface it
without env vars.
"""

import os
import platform

from ..base import REGISTRY

# REASON: importing the concrete model modules triggers their @register
# decorators so REGISTRY is fully populated by the time the host looks
# anything up. Both backends are imported unconditionally — they only
# do lazy imports of their heavy deps inside load(), so importing the
# modules themselves is cheap on any platform.
from . import torch  # noqa: F401
from . import mlx  # noqa: F401


def _default_backend() -> str:
    """Pick the registered class name `"moshi"` should alias to.

    Order: explicit env override → platform default → torch fallback.
    Apple Silicon picks MLX because torch-MPS at bf16 runs roughly an
    order of magnitude slower than MLX-q8 in practice (one frame per
    minute vs real-time on the same hardware)."""
    override = os.environ.get("CYPRESS_MOSHI_BACKEND", "").strip().lower()
    if override == "mlx":
        return "moshi-mlx"
    if override == "torch":
        return "moshi-torch"
    if platform.system() == "Darwin" and platform.machine() == "arm64":
        return "moshi-mlx"
    return "moshi-torch"


# SETUP: register the autoselect alias. We point REGISTRY["moshi"] at
# the same class as the chosen backend rather than registering twice —
# both names refer to one class, so a swap of CYPRESS_MOSHI_BACKEND on
# restart picks up the new default cleanly.
_default = _default_backend()
if _default in REGISTRY:
    REGISTRY["moshi"] = REGISTRY[_default]
