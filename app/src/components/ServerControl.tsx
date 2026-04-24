// AREA: ui · SERVER-CONTROL
// Small floating control for starting/stopping the Go orchestration server
// subprocess and surfacing its live status. State is owned by the Rust side
// (see src-tauri/src/server.rs); we hydrate once via `server_status` and then
// listen for `server-status` events for every transition.
//
// On hover, expands into a details panel showing the inference subsystem's
// snapshot (device, loaded model, phase). Those fields come from the Go
// server's /status endpoint, which we poll lightly while the server is up.

import { useCallback, useEffect, useState } from "react";
import { invoke } from "@tauri-apps/api/core";
import { listen } from "@tauri-apps/api/event";

// SWAP: keep this shape in sync with ServerStatus in server.rs. If we add
// more lifecycle states there, mirror them here so the dot/label logic stays
// exhaustive.
type ServerStatus =
  | { state: "idle" }
  | { state: "starting" }
  | { state: "running" }
  | { state: "stopping" }
  | { state: "error"; message: string };

// Mirror of server/inference/manager.go's Snapshot. Kept as a string union
// on TS side so exhaustive checks catch any new state added server-side.
type InferenceState = "idle" | "starting" | "ready" | "loading" | "serving";
type InferenceSnapshot = {
  state: InferenceState;
  model: string;
  device: string;
  phase: string;
};

// SETUP: Go server base URL. Matches listenAddr in server/main.go.
const SERVER_URL = "http://127.0.0.1:7842";

// Human-readable device labels. Unknown devices fall through to the raw
// string so a new backend shows up in the UI without a frontend change.
const DEVICE_LABEL: Record<string, string> = {
  mps: "Apple Silicon (MPS)",
  cuda: "NVIDIA (CUDA)",
  cpu: "CPU",
};

// Static per-state presentation. Keeping this declarative means adding a new
// state is one entry in this map + one arm in the button logic below.
const STATUS_META: Record<
  ServerStatus["state"],
  { label: string; dotClass: string }
> = {
  idle: { label: "Server idle", dotClass: "bg-muted-foreground" },
  starting: { label: "Starting…", dotClass: "bg-yellow-400 animate-pulse" },
  running: { label: "Running", dotClass: "bg-green-500" },
  stopping: { label: "Stopping…", dotClass: "bg-yellow-400 animate-pulse" },
  error: { label: "Error", dotClass: "bg-red-500" },
};

const EMPTY_SNAPSHOT: InferenceSnapshot = {
  state: "idle",
  model: "",
  device: "",
  phase: "",
};

function inferenceLabel(s: InferenceSnapshot): string {
  switch (s.state) {
    case "idle":
      return "No worker";
    case "starting":
      return "Starting worker…";
    case "ready":
      return "Ready";
    case "loading":
      return "Loading model…";
    case "serving":
      return "Serving";
  }
}

export function ServerControl() {
  const [status, setStatus] = useState<ServerStatus>({ state: "idle" });
  const [busy, setBusy] = useState(false);
  const [snapshot, setSnapshot] = useState<InferenceSnapshot>(EMPTY_SNAPSHOT);

  // STEP 1: hydrate initial state + subscribe to live updates. The Rust side
  // emits `server-status` on every transition; we treat events as the source
  // of truth and fall back to the invoke only for the initial render.
  useEffect(() => {
    let mounted = true;
    invoke<ServerStatus>("server_status")
      .then((s) => mounted && setStatus(s))
      .catch(() => {});
    const unlistenPromise = listen<ServerStatus>("server-status", (e) => {
      if (mounted) setStatus(e.payload);
    });
    return () => {
      mounted = false;
      unlistenPromise.then((un) => un());
    };
  }, []);

  // STEP 2: poll /status while the server is up so the hover panel shows
  // live device + model info. 2s is gentle — the heavy-traffic polling
  // during a model load happens in ModelPicker at 500ms; here we're just
  // keeping the ambient snapshot fresh.
  useEffect(() => {
    if (status.state !== "running") {
      setSnapshot(EMPTY_SNAPSHOT);
      return;
    }
    let cancelled = false;
    const tick = () => {
      fetch(`${SERVER_URL}/status`)
        .then((r) => r.json())
        .then((s: InferenceSnapshot) => {
          if (!cancelled) setSnapshot(s);
        })
        .catch(() => {});
    };
    tick();
    const handle = setInterval(tick, 2000);
    return () => {
      cancelled = true;
      clearInterval(handle);
    };
  }, [status.state]);

  // STEP 3: button action. While a command is in flight we disable the button
  // so the user can't queue start/stop/start in quick succession.
  const onClick = useCallback(async () => {
    setBusy(true);
    try {
      if (status.state === "running" || status.state === "starting") {
        await invoke("stop_server");
      } else {
        await invoke("start_server");
      }
    } catch {
      // The Rust side already emitted an Error status event; no extra UI.
    } finally {
      setBusy(false);
    }
  }, [status.state]);

  const meta = STATUS_META[status.state];
  const isRunLike =
    status.state === "running" || status.state === "starting";
  const buttonLabel = isRunLike ? "Stop Server" : "Start Server";
  const disabled =
    busy || status.state === "starting" || status.state === "stopping";

  const deviceLabel = snapshot.device
    ? DEVICE_LABEL[snapshot.device] ?? snapshot.device
    : "—";

  return (
    <div className="group fixed bottom-4 right-4 flex flex-col items-end gap-2">
      {/* Hover panel — slides up from the control. Only rendered meaningfully
          while the server is running, since otherwise there's nothing to show. */}
      <div
        className="pointer-events-none flex w-56 translate-y-1 flex-col gap-1 rounded-md border bg-card/90 p-3 text-xs opacity-0 shadow-md backdrop-blur transition-all duration-150 group-hover:translate-y-0 group-hover:opacity-100"
      >
        <div className="mb-1 font-medium text-foreground">Server details</div>
        <Row label="Server" value={meta.label} />
        <Row label="Device" value={deviceLabel} />
        <Row label="Model" value={snapshot.model || "—"} />
        <Row label="Inference" value={inferenceLabel(snapshot)} />
        {snapshot.phase && (
          <Row label="Phase" value={snapshot.phase} />
        )}
      </div>

      <div className="flex items-center gap-3 rounded-md border bg-card/80 px-3 py-2 text-xs backdrop-blur">
        <div className="flex items-center gap-2">
          <span className={`h-2 w-2 rounded-full ${meta.dotClass}`} />
          <span className="text-foreground">{meta.label}</span>
        </div>
        <button
          type="button"
          onClick={onClick}
          disabled={disabled}
          className="rounded border border-border bg-secondary px-2 py-1 text-secondary-foreground hover:bg-accent disabled:opacity-50"
        >
          {buttonLabel}
        </button>
      </div>
    </div>
  );
}

function Row({ label, value }: { label: string; value: string }) {
  return (
    <div className="flex items-baseline justify-between gap-2">
      <span className="text-muted-foreground">{label}</span>
      <span className="truncate text-foreground">{value}</span>
    </div>
  );
}
