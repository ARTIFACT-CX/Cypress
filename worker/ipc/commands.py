"""
AREA: worker · IPC · COMMANDS

Reads JSON-line commands from stdin and writes JSON-line replies via the
`write` callback supplied by main.py. One command = one reply, always.

Command shape:
    {"id": <opaque>, "cmd": "<name>", ...params}

Reply shape:
    {"id": <same>, "ok": true, ...result}
    {"id": <same>, "error": "<message>"}

The `id` field is optional but the host typically sets it to correlate
async requests. We echo it back verbatim so the host can match replies to
outstanding requests.

Cross-feature boundary: this module never imports from `models` or any
other feature directly. Instead it accepts a `ModelRegistry` at startup
(see ports.py) and looks models up through that. The composition root
(main.py) is the only place that knows about both ipc and models.

SWAP: handler table. Adding a new command is one entry here plus one
function. If the handler table grows large we'll split per-category
modules (model commands, audio commands, tool commands) and compose them.
"""

import asyncio
import base64
import json
import sys
from typing import Any, Awaitable, Callable

from .ports import ModelRegistry

# SETUP: shared worker state. The model instance is kept so load_model
# can cleanly unload the previous one before swapping. device is surfaced
# to /status so the UI can show "Running on MPS" etc.
_state: dict[str, Any] = {
    "model": None,  # Name of the currently loaded model, or None.
    "instance": None,  # Live Model subclass instance, or None.
    "device": None,  # Device string ("mps", "cuda", "cpu") or None.
    # Streaming: at most one active session per worker (mimi/lm_gen are
    # stateful; concurrent sessions would corrupt each other). The drain
    # task runs alongside, pulling output chunks and emitting events.
    "stream": None,  # MoshiStream-shaped session instance, or None.
    "stream_drain": None,  # asyncio.Task draining session → audio_out events.
}

# SETUP: dependencies wired in by run_loop. Stashing them as module
# globals (rather than threading them through every handler signature)
# matches the singleton nature of the worker — there is exactly one
# control loop per process, so per-call injection would be ceremony.
_write_fn: "WriteFn | None" = None
_registry: ModelRegistry | None = None


Handler = Callable[[dict], Awaitable[dict]]
WriteFn = Callable[[dict], None]


def emit_event(msg: dict) -> None:
    # Events are worker→host one-way messages with no id. The host
    # readLoop routes them into a Manager event handler rather than any
    # waiter channel. Keeping them out-of-band means they never collide
    # with pending request/reply pairs. Public so other features (audio,
    # eventually session) can push events without depending on commands'
    # internals.
    if _write_fn is not None:
        _write_fn(msg)


async def _handle_status(_msg: dict) -> dict:
    # Mirror of what the host's UI needs to show: are we idle, or do we
    # have a model loaded, and on what device?
    return {
        "ok": True,
        "model": _state["model"],
        "device": _state["device"],
    }


async def _handle_load_model(msg: dict) -> dict:
    # STEP 1: validate the target name.
    name = msg.get("name")
    if not isinstance(name, str) or not name:
        return {"error": "missing or invalid 'name'"}

    assert _registry is not None, "registry not wired; call run_loop first"
    cls = _registry.get(name)
    if cls is None:
        known = ", ".join(sorted(_registry.keys())) or "(none registered)"
        return {"error": f"unknown model {name!r}; known: {known}"}

    # STEP 2: unload any previously loaded model so we don't double up
    # on VRAM. Swallow errors from the old one — we're throwing it away
    # anyway, and a failed unload shouldn't block a new load.
    await _stop_active_stream()
    prev = _state["instance"]
    if prev is not None:
        try:
            await prev.unload()
        except Exception:
            pass
        _state["model"] = None
        _state["instance"] = None
        _state["device"] = None

    # STEP 3: instantiate and load. The loader handles its own phase
    # events via emit_event so the host can relay them to the UI.
    instance = cls(emit_event)
    try:
        await instance.load()
    except Exception as e:
        # Include the repr — torch / HF errors often have type info in
        # the class name that's lost by str(e) alone.
        return {"error": f"{type(e).__name__}: {e}"}

    _state["model"] = name
    _state["instance"] = instance
    _state["device"] = instance.device() if hasattr(instance, "device") else None
    return {"ok": True, "model": name, "device": _state["device"]}


async def _handle_unload(_msg: dict) -> dict:
    # Stop any active stream first — its session holds a reference to the
    # model's mimi/lm_gen and would crash mid-iteration if we yanked the
    # instance out from under it.
    await _stop_active_stream()
    prev = _state["instance"]
    if prev is not None:
        try:
            await prev.unload()
        except Exception as e:
            return {"error": f"unload failed: {type(e).__name__}: {e}"}
    _state["model"] = None
    _state["instance"] = None
    _state["device"] = None
    return {"ok": True}


async def _handle_shutdown(_msg: dict) -> dict:
    # The reply is sent before the loop exits (see run_loop below).
    return {"ok": True, "bye": True}


async def _handle_run_wav(msg: dict) -> dict:
    # Offline self-test: feed a wav file through the loaded model and
    # write the response to disk. Useful for verifying the streaming
    # pipeline works end-to-end before mic capture is wired in. Path-in,
    # path-out, metadata reply — no audio streamed back over IPC.
    instance = _state["instance"]
    if instance is None:
        return {"error": "no model loaded"}

    # STEP 1: validate the paths up front so we fail fast rather than
    # spending ~30s loading audio buffers only to error on a missing key.
    input_path = msg.get("input")
    output_path = msg.get("output")
    if not isinstance(input_path, str) or not input_path:
        return {"error": "missing or invalid 'input' path"}
    if not isinstance(output_path, str) or not output_path:
        return {"error": "missing or invalid 'output' path"}

    # STEP 2: gate on capability rather than model name. run_wav is
    # currently Moshi-specific — when PersonaPlex (or a TTS backend)
    # lands without offline self-test support, this hasattr keeps the
    # handler honest instead of erroring inside torch ops.
    if not hasattr(instance, "run_wav"):
        return {"error": f"loaded model {_state['model']!r} does not support run_wav"}

    # STEP 3: run the (blocking) generation off the event loop so the
    # worker can still react to a shutdown command mid-test if the user
    # changes their mind on a 2-minute clip.
    try:
        info = await asyncio.to_thread(instance.run_wav, input_path, output_path)
    except Exception as e:
        return {"error": f"{type(e).__name__}: {e}"}
    return {"ok": True, **info}


async def _handle_start_stream(_msg: dict) -> dict:
    # Open a realtime streaming session against the loaded model. After
    # this returns, the host can fire `audio_in` commands and receive
    # `audio_out` events asynchronously until `stop_stream`.
    instance = _state["instance"]
    if instance is None:
        return {"error": "no model loaded"}

    # Capability gate, same shape as run_wav: a model that doesn't expose
    # stream() (PersonaPlex eventually, or a non-duplex backend) gets
    # rejected here rather than crashing inside the handler.
    if not hasattr(instance, "stream"):
        return {"error": f"loaded model {_state['model']!r} does not support stream"}

    # Reject double-open rather than silently replacing the active session
    # — the host bug that caused this is more useful surfaced than hidden.
    if _state["stream"] is not None:
        return {"error": "stream already active; call stop_stream first"}

    try:
        session = instance.stream()
        await session.start()
    except Exception as e:
        return {"error": f"{type(e).__name__}: {e}"}

    _state["stream"] = session
    # Spawn the drain task before returning so the host can start sending
    # audio_in immediately — by the time the start_stream reply lands, the
    # output side is already wired up.
    _state["stream_drain"] = asyncio.create_task(
        _drain_stream(session), name="ipc-stream-drain"
    )
    # Surface the session's audio rate so the host doesn't have to know
    # which model is loaded to pick a playback rate. Falls back silently
    # if a session lacks the attribute (older fakes in tests).
    sample_rate = getattr(session, "sample_rate", None)
    reply: dict[str, Any] = {"ok": True}
    if sample_rate is not None:
        reply["sample_rate"] = int(sample_rate)
    return reply


async def _handle_audio_in(msg: dict) -> dict:
    # Push one chunk of caller-supplied PCM into the active session. The
    # session reframes internally; payload size doesn't have to match the
    # model's frame size. We keep the reply small ({"ok":true}) — the host
    # fires this fast and doesn't await each one.
    session = _state["stream"]
    if session is None:
        return {"error": "no active stream; call start_stream first"}

    pcm_b64 = msg.get("pcm")
    if not isinstance(pcm_b64, str):
        return {"error": "missing or invalid 'pcm' (expected base64 string)"}
    try:
        pcm = base64.b64decode(pcm_b64, validate=True)
    except (ValueError, base64.binascii.Error) as e:
        return {"error": f"invalid base64 pcm: {e}"}

    try:
        await session.feed(pcm)
    except Exception as e:
        return {"error": f"{type(e).__name__}: {e}"}
    return {"ok": True}


async def _handle_stop_stream(_msg: dict) -> dict:
    # Idempotent: stop_stream with no active session is a no-op, not an
    # error. Lets the host call it defensively (e.g. from a "hang up"
    # button) without tracking state.
    await _stop_active_stream()
    return {"ok": True}


async def _drain_stream(session: Any) -> None:
    """Worker→host pump for one streaming session. Runs as an asyncio task
    spawned by start_stream; ends naturally when the session emits its EOF
    sentinel (i.e. when stop_stream → session.aclose() runs)."""
    try:
        async for chunk in session:
            # Base64 keeps audio inline in the JSON IPC for v0.1 (see #20
            # for the planned sidechannel optimization). text is None on
            # most frames since inner-monologue tokens are sparse.
            emit_event(
                {
                    "event": "audio_out",
                    "pcm": base64.b64encode(chunk.audio_pcm).decode("ascii"),
                    "text": chunk.text,
                }
            )
    except asyncio.CancelledError:
        # Cooperative shutdown via _stop_active_stream(). Not actionable.
        pass
    except Exception as e:
        # Surface unexpected drain failures as an event so the host UI
        # can show a useful error rather than just "stream went silent."
        emit_event({"event": "stream_error", "error": f"{type(e).__name__}: {e}"})


async def _stop_active_stream() -> None:
    """Tear down the active session + drain task, if any. Safe to call
    when nothing is active. Used by stop_stream and by load_model/unload
    to avoid leaking sessions across model swaps."""
    session = _state["stream"]
    drain = _state["stream_drain"]
    if session is None and drain is None:
        return

    # SAFETY: do NOT clear _state["stream"] before aclose() returns. On a
    # busy worker (e.g. mid-_step), aclose can take seconds while it waits
    # for the model thread to wind up — and during that window a new
    # start_stream would race in, see no active stream, and call
    # mimi.streaming_forever() while mimi is still in streaming state from
    # this session. That trips the upstream "is already streaming"
    # assertion. Clearing only after aclose+_stop_streaming have run keeps
    # the streaming-state machine consistent with our own bookkeeping.
    if session is not None:
        try:
            await session.aclose()
        except Exception:
            # We're throwing the session away — a failing aclose isn't
            # worth surfacing or blocking on.
            pass
    if drain is not None:
        # aclose() above sends the EOF sentinel which lets the drain task
        # exit naturally; awaiting it here just collects the result.
        # Cancel as a belt-and-suspenders in case aclose stalled.
        drain.cancel()
        try:
            await drain
        except (asyncio.CancelledError, Exception):
            pass

    _state["stream"] = None
    _state["stream_drain"] = None


_HANDLERS: dict[str, Handler] = {
    "status": _handle_status,
    "load_model": _handle_load_model,
    "unload": _handle_unload,
    "shutdown": _handle_shutdown,
    "run_wav": _handle_run_wav,
    "start_stream": _handle_start_stream,
    "audio_in": _handle_audio_in,
    "stop_stream": _handle_stop_stream,
}


async def _stdin_reader() -> asyncio.StreamReader:
    # REASON: asyncio needs stdin wrapped as a StreamReader before we can
    # await readline(). Without this wrapper, reading stdin blocks the
    # entire event loop and nothing else (audio socket, timers) can run.
    loop = asyncio.get_running_loop()
    reader = asyncio.StreamReader()
    protocol = asyncio.StreamReaderProtocol(reader)
    await loop.connect_read_pipe(lambda: protocol, sys.stdin)
    return reader


async def run_loop(write: WriteFn, registry: ModelRegistry) -> None:
    # Stash the wired dependencies. run_loop is the single entry point,
    # so doing it once here covers every downstream caller.
    global _write_fn, _registry
    _write_fn = write
    _registry = registry

    reader = await _stdin_reader()

    while True:
        # STEP 1: read one newline-delimited command from the host. An
        # empty read means the host closed stdin — treat as shutdown.
        line = await reader.readline()
        if not line:
            return

        # STEP 2: parse JSON. Malformed input is reported but does not
        # terminate the loop — the host may send more valid commands next.
        try:
            msg = json.loads(line)
        except json.JSONDecodeError as e:
            write({"error": f"invalid json: {e}"})
            continue

        cmd = msg.get("cmd")
        msg_id = msg.get("id")

        # STEP 3: dispatch.
        handler = _HANDLERS.get(cmd)
        if handler is None:
            write(_tag_id({"error": f"unknown command: {cmd!r}"}, msg_id))
            continue

        # STEP 4: run the handler. Catch broadly so a buggy handler never
        # takes down the worker; the host sees the error and can decide
        # whether to retry, send a new command, or restart the worker.
        try:
            reply = await handler(msg)
        except Exception as e:
            reply = {"error": f"handler crashed: {e!r}"}

        write(_tag_id(reply, msg_id))

        # STEP 5: shutdown is special — its reply goes out *before* we
        # return, so the host gets its ack and can proceed to join the
        # process cleanly.
        if cmd == "shutdown":
            return


def _tag_id(reply: dict, msg_id) -> dict:
    # Echo the caller's correlation id back if they sent one. Keeps the
    # reply shape deterministic for the host's request/reply matcher.
    if msg_id is not None:
        reply = {"id": msg_id, **reply}
    return reply
