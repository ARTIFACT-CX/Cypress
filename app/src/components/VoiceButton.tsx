// AREA: ui · VOICE · BUTTON
// Tap-to-converse button + live transcript, fixed at the bottom of the
// window. Owns one VoiceSession (via the hook) and surfaces its state
// as a single circular control: idle, connecting, live (pulsing while
// audio flows), error.
//
// Visibility rules:
//   - The button only appears once a model is loaded (inference state =
//     "serving"). Without a model, there's nothing for the user to talk
//     to and the WS would just reject the session — better to hide.
//   - The transcript stays hidden until the user starts a conversation
//     for the first time, then sticks around so they can read what was
//     said after they tap to end.
//
// Click = toggle. The model is duplex, so "live" means both directions
// are open; tap again to end the conversation.

import { useEffect, useRef, useState } from "react";
import { Mic, MicOff, Loader2 } from "lucide-react";
import type { useVoiceSession } from "../hooks/useVoiceSession";
import { cn } from "../lib/utils";

// Take the voice-session bag as a prop rather than calling the hook
// internally. App owns the hook so page chrome (tagline, etc.) can read
// the same state — without lifting we'd have two parallel sessions.
type VoiceSessionApi = ReturnType<typeof useVoiceSession>;

// SETUP: Go server base URL. Same listenAddr as the rest of the app.
// Polling /status here is redundant with ServerControl's poller, but the
// frequency is low (1.5s) and keeping the gating logic self-contained
// beats threading a context through just for one boolean.
const SERVER_URL = "http://127.0.0.1:7842";
const STATUS_POLL_MS = 1500;

type InferenceState = "idle" | "starting" | "ready" | "loading" | "serving";

export function VoiceButton({ voice }: { voice: VoiceSessionApi }) {
  const { state, error, transcript, micLevel, playbackLevel, toggle } = voice;

  // STEP 1: gate visibility on whether a model is actually loaded. We
  // poll /status rather than subscribing to events because the existing
  // wire-format is "snapshot via fetch" (see ServerControl); a future
  // server-status event-bus refactor would replace both pollers at once.
  const [modelReady, setModelReady] = useState(false);
  useEffect(() => {
    let cancelled = false;
    const tick = () => {
      fetch(`${SERVER_URL}/status`)
        .then((r) => r.json())
        .then((s: { state: InferenceState }) => {
          if (!cancelled) setModelReady(s.state === "serving");
        })
        .catch(() => {
          if (!cancelled) setModelReady(false);
        });
    };
    tick();
    const handle = setInterval(tick, STATUS_POLL_MS);
    return () => {
      cancelled = true;
      clearInterval(handle);
    };
  }, []);

  // STEP 2: track whether the user has started a conversation at least
  // once. Drives transcript visibility — we don't want an empty box
  // floating on first launch, but we do want the transcript to persist
  // after the user taps to end so they can read the last reply.
  const [hasStarted, setHasStarted] = useState(false);
  useEffect(() => {
    if (state === "connecting" || state === "live") setHasStarted(true);
  }, [state]);

  const live = state === "live";
  const busy = state === "connecting" || state === "closing";
  // Levels are still surfaced on the hook for future use (e.g. an
  // accessibility-friendly text indicator), but the button itself no
  // longer animates with them — App routes the same signal into the
  // logo's chromatic aberration instead.
  void micLevel;
  void playbackLevel;

  // Auto-scroll transcript to the newest token. Append-only stream, no
  // observer needed.
  const tailRef = useRef<HTMLDivElement | null>(null);
  useEffect(() => {
    tailRef.current?.scrollIntoView({ behavior: "smooth", block: "end" });
  }, [transcript]);

  if (!modelReady) return null;

  const label = live
    ? "Tap to end"
    : busy
      ? state === "connecting"
        ? "Connecting…"
        : "Closing…"
      : "Tap to talk";

  return (
    <div className="pointer-events-none fixed inset-x-0 bottom-6 flex flex-col items-center gap-3">
      {/* Transcript — only after the user has actually started once. */}
      {hasStarted && (
        <div className="pointer-events-auto h-20 w-72 overflow-y-auto rounded-md border bg-card/80 px-3 py-2 text-xs leading-relaxed text-foreground shadow-sm backdrop-blur">
          {transcript.length === 0 ? (
            <span className="text-muted-foreground">
              {live ? "Listening…" : "No transcript yet."}
            </span>
          ) : (
            <>
              {transcript.join("")}
              <div ref={tailRef} />
            </>
          )}
        </div>
      )}

      {/* Status / error caption. min-h reserves a row so the button
          doesn't jump when the caption appears/disappears. */}
      <div className="min-h-[1rem] text-center text-[10px] text-muted-foreground">
        {error ? <span className="text-red-400">{error}</span> : label}
      </div>

      {/* Button. The audio-reactive visual lives on the logo (see
          useChromaticAberration in App), so this stays a calm static
          control — no competing pulse here. */}
      <div className="pointer-events-auto relative flex h-14 w-14 items-center justify-center">
        <button
          type="button"
          onClick={toggle}
          disabled={busy}
          aria-pressed={live}
          aria-label={label}
          className={cn(
            "relative flex h-12 w-12 items-center justify-center rounded-full border shadow-md transition-colors",
            live
              ? "border-sky-300/50 bg-sky-500/20 text-sky-100 hover:bg-sky-500/30"
              : "border-border bg-card text-foreground hover:bg-accent",
            busy && "cursor-wait opacity-70",
          )}
        >
          {busy ? (
            <Loader2 className="h-5 w-5 animate-spin" />
          ) : live ? (
            <MicOff className="h-5 w-5" />
          ) : (
            <Mic className="h-5 w-5" />
          )}
        </button>
      </div>
    </div>
  );
}
