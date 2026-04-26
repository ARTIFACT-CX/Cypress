// AREA: ui · VOICE · SESSION
// Browser-side audio engine for a duplex voice conversation. Owns three
// pieces that all share one lifecycle:
//
//   mic capture → resample to 24kHz mono int16 → WS binary send
//   WS binary recv → queue PCM → AudioContext playback
//   WS text envelope → text listener (transcript)
//
// SWAP: this is the inbound adapter for the audio path on the UI side. The
// matching seam server-side is server/audio/ws.go. Wire format is documented
// in issue #5: binary frames = PCM, JSON text frames = control envelopes.
//
// One session at a time per app — the server enforces this too, but we
// don't try to multiplex client-side either. start() is idempotent; a
// second call while already live is a no-op.

// SETUP: must match the worker / mimi config. We open the mic AudioContext
// at 24kHz so the browser does the resampling for us — saves shipping a
// separate resampler and matches what the smoke script does end-to-end.
const SAMPLE_RATE = 24000;

// ScriptProcessorNode buffer size is constrained to powers of 2 in
// [256, 16384]. 2048 samples ≈ 85ms at 24kHz, close to mimi's 80ms native
// frame and well within the worker's 8-frame input queue (~640ms headroom).
// Smaller buffers add overhead for no perceptual gain at this rate.
const MIC_BUFFER_SIZE = 2048;

// SWAP: WebSocket URL. Same listenAddr as the rest of the app; we go
// straight to the Go server rather than through Tauri IPC because audio
// throughput on the IPC channel would be wasteful for what's already a
// localhost socket.
const WS_URL = "ws://127.0.0.1:7842/ws";

export type SessionState = "idle" | "connecting" | "live" | "closing" | "error";

// One-line description of why the session ended. Surfaced to the UI so
// the user sees something useful instead of a silent close.
export type CloseReason = string;

export type VoiceSessionEvents = {
  onState?: (state: SessionState, reason?: CloseReason) => void;
  onText?: (text: string) => void;
  // 0..1 RMS level of the most recent mic frame. Driven from the mic tap
  // so the UI can pulse / animate without sampling the AudioContext itself.
  onMicLevel?: (level: number) => void;
  // 0..1 RMS level of the most recent playback frame. Lets the UI show
  // the model is talking back, not just that the session is open.
  onPlaybackLevel?: (level: number) => void;
};

export class VoiceSession {
  private ws: WebSocket | null = null;
  private micCtx: AudioContext | null = null;
  private playCtx: AudioContext | null = null;
  private micStream: MediaStream | null = null;
  private micSource: MediaStreamAudioSourceNode | null = null;
  private micProcessor: ScriptProcessorNode | null = null;
  // Running cursor for chained playback. AudioBufferSourceNode.start(when)
  // schedules in the AudioContext clock; we keep the next-frame's start
  // time here so successive frames play seamlessly without overlap or gap.
  private playCursor = 0;
  private state: SessionState = "idle";

  constructor(private events: VoiceSessionEvents = {}) {}

  getState(): SessionState {
    return this.state;
  }

  async start(): Promise<void> {
    if (this.state !== "idle" && this.state !== "error") return;
    this.setState("connecting");

    try {
      // STEP 1: mic permission + capture. Request 24kHz mono explicitly so
      // the browser resamples for us. WebKit honors `sampleRate` on
      // getUserMedia constraints; Chromium honors it via the AudioContext
      // sample rate (set in STEP 2). Belt-and-suspenders.
      this.micStream = await navigator.mediaDevices.getUserMedia({
        audio: {
          channelCount: 1,
          sampleRate: SAMPLE_RATE,
          echoCancellation: true,
          noiseSuppression: true,
          autoGainControl: true,
        },
      });

      // STEP 2: open the WebSocket. We wait for the `open` envelope before
      // marking the session live — if the server has no model loaded it
      // sends `{type:"error"}` instead and we bail out cleanly.
      this.ws = new WebSocket(WS_URL);
      this.ws.binaryType = "arraybuffer";
      await this.handshake(this.ws);

      // STEP 3: wire up audio capture → WS. The AudioContext at 24kHz lets
      // the browser handle the 48k→24k resample. ScriptProcessorNode is
      // deprecated but works everywhere without a separate worklet file;
      // a future pass can swap to AudioWorkletNode without changing the
      // wire format.
      this.micCtx = new AudioContext({ sampleRate: SAMPLE_RATE });
      this.micSource = this.micCtx.createMediaStreamSource(this.micStream);
      this.micProcessor = this.micCtx.createScriptProcessor(
        MIC_BUFFER_SIZE,
        1,
        1,
      );
      this.micProcessor.onaudioprocess = (e) => this.onMicFrame(e);
      // SAFETY: ScriptProcessor only ticks while it's connected to the
      // destination *or* an active analyser. We connect to a muted gain
      // so the user doesn't hear their own mic looped back; without this
      // wiring the onaudioprocess callback never fires.
      const sink = this.micCtx.createGain();
      sink.gain.value = 0;
      this.micSource.connect(this.micProcessor);
      this.micProcessor.connect(sink);
      sink.connect(this.micCtx.destination);

      // STEP 4: prepare the playback AudioContext. The server's `open`
      // envelope already told us its sample rate (24000 today, but kept
      // dynamic so a future model with a different codec just works).
      this.playCtx = new AudioContext({ sampleRate: SAMPLE_RATE });
      this.playCursor = this.playCtx.currentTime;

      this.setState("live");
    } catch (err) {
      const msg = err instanceof Error ? err.message : String(err);
      await this.cleanup();
      this.setState("error", msg);
      throw err;
    }
  }

  async stop(reason: CloseReason = "user stopped"): Promise<void> {
    if (this.state === "idle" || this.state === "closing") return;
    this.setState("closing", reason);
    await this.cleanup();
    this.setState("idle", reason);
  }

  // STEP A: handshake — wait for the server's first envelope before letting
  // the rest of the session proceed. Resolves on `{type:"open"}`, rejects
  // on `{type:"error"}` or unexpected first-frame.
  private handshake(ws: WebSocket): Promise<void> {
    return new Promise((resolve, reject) => {
      const cleanup = () => {
        ws.removeEventListener("message", onMsg);
        ws.removeEventListener("error", onErr);
        ws.removeEventListener("close", onClose);
      };
      const onMsg = (ev: MessageEvent) => {
        if (typeof ev.data !== "string") {
          cleanup();
          reject(new Error("server sent binary before open envelope"));
          return;
        }
        try {
          const env = JSON.parse(ev.data);
          if (env.type === "open") {
            cleanup();
            // Re-attach the long-lived message handler now that we've
            // consumed the open envelope.
            ws.addEventListener("message", (m) => this.onWsMessage(m));
            ws.addEventListener("close", (c) =>
              this.onWsClose(c.reason || "closed"),
            );
            ws.addEventListener("error", () =>
              this.onWsClose("transport error"),
            );
            resolve();
          } else if (env.type === "error") {
            cleanup();
            reject(new Error(env.message || "server error"));
          } else {
            cleanup();
            reject(new Error(`unexpected first frame: ${env.type}`));
          }
        } catch (err) {
          cleanup();
          reject(err);
        }
      };
      const onErr = () => {
        cleanup();
        reject(new Error("websocket error during handshake"));
      };
      const onClose = (ev: CloseEvent) => {
        cleanup();
        reject(new Error(ev.reason || "websocket closed during handshake"));
      };
      ws.addEventListener("message", onMsg);
      ws.addEventListener("error", onErr);
      ws.addEventListener("close", onClose);
    });
  }

  // STEP B: per-mic-frame callback. Convert float32 → int16 LE, ship over
  // WS, emit a level reading for the UI.
  private onMicFrame(e: AudioProcessingEvent) {
    if (!this.ws || this.ws.readyState !== WebSocket.OPEN) return;
    const ch = e.inputBuffer.getChannelData(0);
    const int16 = new Int16Array(ch.length);
    let sumSq = 0;
    for (let i = 0; i < ch.length; i++) {
      const s = Math.max(-1, Math.min(1, ch[i]));
      int16[i] = (s * 32767) | 0;
      sumSq += s * s;
    }
    this.ws.send(int16.buffer);
    // RMS for the level meter. Square root + clamp; the UI does its own
    // shaping (log curve, smoothing) on top.
    const rms = Math.sqrt(sumSq / ch.length);
    this.events.onMicLevel?.(Math.min(1, rms));
  }

  // STEP C: WS message handler. Binary = PCM playback; text = JSON envelope.
  private onWsMessage(ev: MessageEvent) {
    if (ev.data instanceof ArrayBuffer) {
      this.enqueuePlayback(ev.data);
      return;
    }
    if (typeof ev.data === "string") {
      try {
        const env = JSON.parse(ev.data);
        if (env.type === "text") {
          this.events.onText?.(env.data ?? "");
        } else if (env.type === "error") {
          // Server-side fatal — close out and surface the reason.
          this.stop(env.message || "server error");
        }
      } catch {
        // Malformed JSON from the server is a bug, not a runtime concern;
        // ignore here so a bad envelope doesn't kill the session.
      }
    }
  }

  private onWsClose(reason: string) {
    if (this.state === "idle" || this.state === "closing") return;
    // Async cleanup is fine; nothing here awaits it.
    void this.stop(reason);
  }

  // STEP D: schedule one PCM frame on the playback context's clock. We
  // chain frames using `playCursor` so consecutive buffers play back-to-
  // back even if WS delivery is bursty.
  private enqueuePlayback(buf: ArrayBuffer) {
    if (!this.playCtx) return;
    const int16 = new Int16Array(buf);
    if (int16.length === 0) return;

    const audioBuf = this.playCtx.createBuffer(1, int16.length, SAMPLE_RATE);
    const ch = audioBuf.getChannelData(0);
    let sumSq = 0;
    for (let i = 0; i < int16.length; i++) {
      const s = int16[i] / 32768;
      ch[i] = s;
      sumSq += s * s;
    }
    const src = this.playCtx.createBufferSource();
    src.buffer = audioBuf;
    src.connect(this.playCtx.destination);

    // If the cursor has fallen behind real time (e.g. tab was backgrounded
    // and the queue stalled), snap forward to "now". Otherwise we'd try to
    // schedule frames in the past and they'd all fire at once.
    const now = this.playCtx.currentTime;
    if (this.playCursor < now) this.playCursor = now;
    src.start(this.playCursor);
    this.playCursor += audioBuf.duration;

    const rms = Math.sqrt(sumSq / int16.length);
    this.events.onPlaybackLevel?.(Math.min(1, rms));
  }

  private setState(state: SessionState, reason?: CloseReason) {
    this.state = state;
    this.events.onState?.(state, reason);
  }

  // SAFETY: idempotent teardown. Called from stop() and from error paths
  // mid-start; must not throw on partially-initialized state.
  private async cleanup() {
    try {
      this.micProcessor?.disconnect();
    } catch {}
    try {
      this.micSource?.disconnect();
    } catch {}
    if (this.micStream) {
      for (const t of this.micStream.getTracks()) t.stop();
    }
    try {
      await this.micCtx?.close();
    } catch {}
    try {
      await this.playCtx?.close();
    } catch {}
    if (this.ws && this.ws.readyState <= WebSocket.OPEN) {
      try {
        this.ws.close();
      } catch {}
    }
    this.ws = null;
    this.micCtx = null;
    this.playCtx = null;
    this.micStream = null;
    this.micSource = null;
    this.micProcessor = null;
    this.playCursor = 0;
  }
}
