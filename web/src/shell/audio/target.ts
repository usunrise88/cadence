import { create } from "zustand";
import { openPanel } from "@/shell/dock/layout";
import { audioItem, parseAudioItem, type AudioTarget } from "./selection";

// What the Audio panel shows: the last span opened with openAudio (an Eval report row, Diff, a golden set sample,
// a chat reference). It is a selection item (utt:<id>#t=…), so it round-trips through references and deep links.

export const AUDIO_PANEL = "audio";

type State = { item: string | null; set(item: string | null): void };

export const useAudioTarget = create<State>((set) => ({ item: null, set: (item) => set({ item }) }));

/** Shows a target in the Audio panel (opening it, floating by default). */
export function openAudio(target: AudioTarget | string): void {
  const item = typeof target === "string" ? target : audioItem(target);
  if (!parseAudioItem(item)) return;
  useAudioTarget.getState().set(item);
  openPanel(AUDIO_PANEL);
}

export function useAudioItem(): AudioTarget | undefined {
  const item = useAudioTarget((s) => s.item);
  return parseAudioItem(item ?? undefined);
}
