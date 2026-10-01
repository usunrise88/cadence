import { create } from "zustand";
import type { Notice } from "./store";

// The notification sound: a short tone while the Cadence tab is open (in front or in the background) when a live
// notice arrives that wants attention — an approval request (warning) or a failure (error). It is synthesised with
// Web Audio, so there is no asset; browsers allow it only after the person has interacted with the page once, so the
// first pointer or key press unlocks it. The choice is per browser (localStorage), switched in the user menu.

const KEY = "cadence.notificationSound";

function readPref(): boolean {
  try {
    return localStorage.getItem(KEY) !== "off";
  } catch {
    return true;
  }
}

type SoundState = { enabled: boolean; setEnabled(on: boolean): void };

export const useNotificationSound = create<SoundState>((set) => ({
  enabled: readPref(),
  setEnabled: (on) => {
    try {
      localStorage.setItem(KEY, on ? "on" : "off");
    } catch {
      // private window: the choice lasts until reload
    }
    set({ enabled: on });
  },
}));

/** Notes per level (Hz, in order); levels that are absent stay silent. */
export const TONES: Partial<Record<Notice["level"], number[]>> = {
  warning: [880, 1175],
  error: [523, 392],
};

/** Whether a notice plays a tone. Pure. */
export function soundFor(level: Notice["level"], enabled: boolean): number[] | undefined {
  return enabled ? TONES[level] : undefined;
}

let ctx: AudioContext | undefined;

function audio(): AudioContext | undefined {
  if (typeof window === "undefined" || typeof window.AudioContext === "undefined") return undefined;
  ctx ??= new window.AudioContext();
  return ctx;
}

/** Resumes the audio context on the first interaction (autoplay policy); returns the listeners' removal. */
export function unlockSoundOnInteraction(): () => void {
  const remove = () => {
    window.removeEventListener("pointerdown", unlock);
    window.removeEventListener("keydown", unlock);
  };
  const unlock = () => {
    void audio()?.resume().catch(() => undefined);
    remove();
  };
  window.addEventListener("pointerdown", unlock);
  window.addEventListener("keydown", unlock);
  return remove;
}

/** Plays the tone a notice of this level deserves, if the sound is on. */
export function playNoticeSound(level: Notice["level"]): void {
  const notes = soundFor(level, useNotificationSound.getState().enabled);
  const ac = notes && audio();
  if (!notes || !ac || ac.state !== "running") return;
  const start = ac.currentTime;
  notes.forEach((hz, i) => {
    const t = start + i * 0.14;
    const osc = ac.createOscillator();
    const gain = ac.createGain();
    osc.type = "sine";
    osc.frequency.value = hz;
    gain.gain.setValueAtTime(0.0001, t);
    gain.gain.exponentialRampToValueAtTime(0.15, t + 0.01);
    gain.gain.exponentialRampToValueAtTime(0.0001, t + 0.13);
    osc.connect(gain).connect(ac.destination);
    osc.start(t);
    osc.stop(t + 0.14);
  });
}
