/*
 * AREA: ui · HOOKS · CHROMATIC-ABERRATION
 *
 * Drives the `.chromatic-aberration` CSS filter from one of two inputs,
 * depending on whether a live voice session is active:
 *
 *   - voice session OFF : original mouse-reactive behavior. Channels
 *     split toward the cursor with a linear distance falloff. The
 *     logo is otherwise still — no idle animation.
 *   - voice session ON  : audio-reactive behavior. The 0..1 RMS level
 *     drives channel separation magnitude; mouse is ignored. When
 *     audio is silent (between turns) the magnitude collapses to 0
 *     and the logo sits perfectly still — no spin, no throb.
 *
 * Returns a tuple: a ref to attach to the target element, and a setter
 * `(level, live)` to push the latest audio level + session state on
 * each render. The setter only writes internal refs — it does NOT
 * re-run the effect — so callers can pump it from React state at
 * whatever rate they like without thrashing.
 *
 * Three CSS custom properties land on the target element:
 *
 *   --ca-dx   : horizontal channel offset   (unitless, used in calc())
 *   --ca-dy   : vertical channel offset     (unitless, used in calc())
 *   --ca-blur : drop-shadow blur radius     (unitless, used in calc())
 *
 * The effect is filter-only — the element itself never translates,
 * scales, or rotates. Only the color channels split apart.
 */
import { useEffect, useRef } from "react";

type Options = {
  // SWAP: tune these to taste. Defaults aim for "noticeable but not tacky".
  maxOffset?: number; // peak horizontal channel separation, px (mouse path)
  maxBlur?: number; // peak drop-shadow blur, px (mouse path)
  falloff?: number; // distance from center at which mouse intensity hits 0, px
  // Audio path: speech RMS sits in 0.05–0.2 even when loud, so the
  // hook sqrt-curves the level (boosting low values) and scales by
  // these so a quiet "hello" still pops. The result is clamped at the
  // mouse-path peak (`maxOffset` / `maxBlur`) so a shout saturates at
  // the same visual ceiling as a centered cursor, instead of blasting
  // past it.
  audioOffsetScale?: number;
  audioBlurScale?: number;
};

export function useChromaticAberration<T extends HTMLElement>(
  opts: Options = {},
) {
  const {
    maxOffset = 6,
    maxBlur = 3,
    falloff = 400,
    audioOffsetScale = 4,
    audioBlurScale = 3,
  } = opts;

  const ref = useRef<T | null>(null);

  // Latest audio level + session-live flag. Updated by the returned
  // setter on every render so React state pumps don't trigger effect
  // re-runs; the rAF loop reads them fresh every frame.
  const audioLevelRef = useRef(0);
  const liveRef = useRef(false);

  // Latest mouse contribution. Computed in the mousemove handler; read
  // by the rAF tick. Stored as the *output* values (already scaled by
  // intensity + falloff) so the tick stays cheap.
  const mouseRef = useRef({ ox: 0, oy: 0, blur: 0 });

  // Smoothed random walk used by the audio path. Each frame we kick
  // these by a small random delta and apply a mild pull toward 0 so
  // they stay roughly in [-1, 1] without piling up. The result reads
  // as organic jitter rather than a clean rotation — direction wanders
  // unpredictably while audio is flowing.
  const noiseRef = useRef({ x: 0, y: 0, b: 0 });

  useEffect(() => {
    const el = ref.current;
    if (!el) return;

    const onMove = (e: MouseEvent) => {
      // STEP 1: measure element center in viewport coords. Recomputed
      // every move so window resizes / layout shifts don't desync.
      const rect = el.getBoundingClientRect();
      const cx = rect.left + rect.width / 2;
      const cy = rect.top + rect.height / 2;

      // STEP 2: vector from center to cursor.
      const dx = e.clientX - cx;
      const dy = e.clientY - cy;
      const dist = Math.hypot(dx, dy);

      // STEP 3: linear falloff with distance.
      const intensity = Math.max(0, 1 - dist / falloff);

      // STEP 4: normalize direction so offset magnitude is controlled
      // by intensity alone. Guard div-by-zero on dead-center cursor.
      const nx = dist === 0 ? 0 : dx / dist;
      const ny = dist === 0 ? 0 : dy / dist;

      mouseRef.current = {
        ox: nx * intensity * maxOffset,
        oy: ny * intensity * maxOffset,
        blur: intensity * maxBlur,
      };
    };

    // STEP 5: rAF loop picks the active path (mouse vs audio) based on
    // whether a voice session is live, and writes the CSS vars. Always
    // running (cheap when idle — a few multiplies + three setProperty
    // calls) is simpler than starting/stopping per-state.
    let raf = 0;
    const tick = () => {
      let ox = 0,
        oy = 0,
        blur = 0;

      if (liveRef.current) {
        // AUDIO PATH: shape the audio signal. Speech RMS sits in
        // 0.05–0.2 even when loud, so a linear mapping looks dead.
        // sqrt pushes 0.1 to ~0.32, 0.04 to ~0.2 — the effect tracks
        // soft talk visibly while still saturating on shouts. When
        // rawAudio is 0 (between turns) all three outputs collapse
        // to 0 and the logo sits completely still.
        const audio = Math.sqrt(audioLevelRef.current);

        // Smoothed random walk for direction + blur. Per frame we add
        // a small uniform-random kick and pull mildly back toward 0
        // (decay 0.9) — this is a bounded brownian / 1-pole low-pass
        // of white noise, which reads as organic jitter rather than
        // a circular sweep.
        const n = noiseRef.current;
        n.x = n.x * 0.9 + (Math.random() * 2 - 1) * 0.35;
        n.y = n.y * 0.9 + (Math.random() * 2 - 1) * 0.35;
        n.b = n.b * 0.9 + Math.random() * 0.35;

        // Clamp at the mouse-path peak so a shout saturates at the same
        // visual ceiling as a centered cursor, rather than blasting past.
        const peak = audio * maxOffset * audioOffsetScale;
        ox = Math.max(-maxOffset, Math.min(maxOffset, n.x * peak));
        oy = Math.max(-maxOffset, Math.min(maxOffset, n.y * peak));
        blur = Math.min(n.b * audio * maxBlur * audioBlurScale, maxBlur);
      } else {
        // MOUSE PATH: the original cursor-pulls-channels-apart effect.
        const mouse = mouseRef.current;
        ox = mouse.ox;
        oy = mouse.oy;
        blur = mouse.blur;
      }

      el.style.setProperty("--ca-dx", ox.toFixed(2));
      el.style.setProperty("--ca-dy", oy.toFixed(2));
      el.style.setProperty("--ca-blur", blur.toFixed(2));

      raf = requestAnimationFrame(tick);
    };

    window.addEventListener("mousemove", onMove);
    raf = requestAnimationFrame(tick);
    return () => {
      window.removeEventListener("mousemove", onMove);
      cancelAnimationFrame(raf);
    };
  }, [maxOffset, maxBlur, falloff, audioOffsetScale, audioBlurScale]);

  // Returned setter pushes the latest audio level + live flag into the
  // refs. Cheap; safe to call every render or every animation frame.
  const setAudio = (level: number, live: boolean) => {
    audioLevelRef.current = Math.max(0, Math.min(1, level));
    liveRef.current = live;
  };

  return [ref, setAudio] as const;
}
