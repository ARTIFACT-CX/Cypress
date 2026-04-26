// AREA: ui · VOICE · HOOK
// React wrapper around VoiceSession. Owns one session per component
// instance; tears it down on unmount so a navigation away doesn't leak
// an open WS + mic. UI components don't talk to VoiceSession directly —
// they use this hook so state transitions go through React's render.

import { useCallback, useEffect, useRef, useState } from "react";
import { VoiceSession, type SessionState } from "../lib/voiceSession";

// Bounded transcript buffer. Inner-monologue tokens stream fast; without
// a cap a long session would balloon the React tree. 400 entries ≈ a few
// minutes of dialogue at Moshi's typical token rate — plenty for showing
// recent context, far short of becoming a memory issue.
const MAX_TRANSCRIPT = 400;

export function useVoiceSession() {
  const sessionRef = useRef<VoiceSession | null>(null);
  const [state, setState] = useState<SessionState>("idle");
  const [error, setError] = useState<string | null>(null);
  const [transcript, setTranscript] = useState<string[]>([]);
  const [micLevel, setMicLevel] = useState(0);
  const [playbackLevel, setPlaybackLevel] = useState(0);

  // Smoothing for the level meters. AudioProcessingEvent fires every
  // ~85ms, which is jittery to the eye; keep a decaying tail so the
  // visual pulses gently rather than strobing.
  const micLevelRef = useRef(0);
  const playLevelRef = useRef(0);

  // STEP 1: lazy-construct the session on first start. We defer the
  // constructor until the user actually presses the button so an unused
  // mounted hook doesn't trip permission prompts.
  const ensureSession = useCallback((): VoiceSession => {
    if (sessionRef.current) return sessionRef.current;
    const s = new VoiceSession({
      onState: (next, reason) => {
        setState(next);
        if (next === "error") setError(reason ?? "unknown error");
        else if (next === "live") setError(null);
      },
      onText: (chunk) =>
        setTranscript((prev) => {
          const next = prev.length + 1 > MAX_TRANSCRIPT
            ? [...prev.slice(prev.length - MAX_TRANSCRIPT + 1), chunk]
            : [...prev, chunk];
          return next;
        }),
      onMicLevel: (level) => {
        // Exponential smoothing — fast attack, slow decay so a single
        // quiet frame doesn't make the indicator flicker dark.
        const prev = micLevelRef.current;
        const smoothed = level > prev ? level : prev * 0.85 + level * 0.15;
        micLevelRef.current = smoothed;
        setMicLevel(smoothed);
      },
      onPlaybackLevel: (level) => {
        const prev = playLevelRef.current;
        const smoothed = level > prev ? level : prev * 0.85 + level * 0.15;
        playLevelRef.current = smoothed;
        setPlaybackLevel(smoothed);
      },
    });
    sessionRef.current = s;
    return s;
  }, []);

  const start = useCallback(async () => {
    setError(null);
    setTranscript([]);
    try {
      await ensureSession().start();
    } catch (e) {
      // setState already fired error via the callback; nothing more to do.
    }
  }, [ensureSession]);

  const stop = useCallback(async () => {
    await sessionRef.current?.stop("user stopped");
  }, []);

  const toggle = useCallback(async () => {
    const s = sessionRef.current?.getState() ?? "idle";
    if (s === "live") {
      await stop();
    } else if (s === "idle" || s === "error") {
      await start();
    }
  }, [start, stop]);

  // STEP 2: cleanup on unmount. Without this, a hot-reload during dev
  // leaves the mic light on and the WS half-open until the page reloads.
  useEffect(() => {
    return () => {
      void sessionRef.current?.stop("unmount");
    };
  }, []);

  return {
    state,
    error,
    transcript,
    micLevel,
    playbackLevel,
    start,
    stop,
    toggle,
  };
}
