import logo from "./assets/logo.png";
import { ModelPicker } from "./components/ModelPicker";
import { ServerControl } from "./components/ServerControl";
import { VoiceButton } from "./components/VoiceButton";
import { useChromaticAberration } from "./hooks/useChromaticAberration";
import { useVoiceSession } from "./hooks/useVoiceSession";

function App() {
  const [logoRef, setLogoAudio] = useChromaticAberration<HTMLImageElement>();
  // Hoist the voice session up so the page chrome (tagline, logo
  // chromatic-aberration animation) can react to live state without
  // VoiceButton having to publish anything externally.
  const voice = useVoiceSession();
  const live = voice.state === "live" || voice.state === "connecting";
  // Pump audio level + live flag into the logo effect. While live, the
  // hook ignores mouse and reacts only to audio (silent → no effect).
  // While not live, it falls back to the original mouse-reactive mode.
  setLogoAudio(Math.max(voice.micLevel, voice.playbackLevel), live);
  return (
    <>
      <div data-tauri-drag-region className="titlebar-drag" />
      <main className="flex min-h-screen flex-col items-center justify-center gap-6">
        <img ref={logoRef} src={logo} alt="Cypress" className="h-32 w-32 chromatic-aberration" />
        <h1 className="text-2xl font-medium tracking-tight text-foreground">
          Cypress
        </h1>
        {/* Tagline gives way to the conversation UX once the user
            starts a session — the transcript becomes the focal copy. */}
        {!live && (
          <p className="text-sm text-muted-foreground">
            Local voice inference, on your machine.
          </p>
        )}
      </main>
      <ModelPicker />
      <ServerControl />
      <VoiceButton voice={voice} />
    </>
  );
}

export default App;
