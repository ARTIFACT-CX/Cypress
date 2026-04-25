"""
AREA: worker · IPC · PORTS

Outbound ports for the IPC feature. These are the protocols ipc requires
from other features — duck-typed at runtime, but pinned to a Protocol so
the type checker keeps the contract honest.

The composition root (main.py) wires concrete implementations from other
features (today: models.REGISTRY) into ipc.run_loop. ipc itself never
imports from `models` or any other feature package.
"""

from typing import Awaitable, Callable, Mapping, Protocol


class _ModelInstance(Protocol):
    """Lifecycle methods every loaded model must support."""

    async def load(self) -> None: ...

    async def unload(self) -> None: ...

    def device(self) -> str | None: ...


class _ModelFactory(Protocol):
    """Constructs a model instance given an event-emit callback. Concrete
    Model subclasses satisfy this implicitly via their __init__ signature."""

    def __call__(self, emit: Callable[[dict], None]) -> _ModelInstance: ...


# The shape ipc wants from a registry: name → factory. The actual mapping
# may be a dict or anything else that supports __getitem__/get/keys().
ModelRegistry = Mapping[str, _ModelFactory]
