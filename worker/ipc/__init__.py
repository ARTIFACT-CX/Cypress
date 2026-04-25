"""
AREA: worker · IPC

Feature: stdin/stdout JSON-line control protocol with the Go host. Owns
the dispatch table for inbound commands and the write callback that
handlers (and side channels like model phase events) use to push lines
back. Concrete handlers live in commands.py.

Public surface:
- run_loop(write, registry): the control loop entry point
- emit_event(msg): out-of-band (no-id) push from anywhere in the worker

Anything else here is an implementation detail.
"""

from .commands import emit_event, run_loop

__all__ = ["run_loop", "emit_event"]
