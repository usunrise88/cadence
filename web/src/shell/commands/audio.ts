import { Pause, ZoomIn, ZoomOut } from "iconoir-react";
import { focusedAudio, type AudioController } from "@/shell/audio/controller";
import { commands } from "@/shell/registries";
import type { Command } from "./registry";

// The audio view's keys (R51 "view.audio.*"; ids are view.audio<Action>, as client-only ids take one dot): enabled
// only while an audio view has the keyboard focus, so Space, the arrows and the punctuation keys stay free
// everywhere else (a disabled command leaves the key alone). Shift+= types "+", which a chord cannot name: the view
// takes "+" as zoom in itself.

const need = (): true | string => (focusedAudio() ? true : "No audio view has the focus");
const on = (fn: (c: AudioController) => void) => () => {
  const c = focusedAudio();
  if (c) fn(c);
};

export const AUDIO_COMMANDS: Command[] = [
  { id: "view.audioPlay", title: "Audio: play or pause", group: "View", icon: Pause, keys: ["Space"], enabled: need, run: on((c) => c.togglePlay()) },
  { id: "view.audioSeekBack", title: "Audio: seek back", group: "View", keys: ["Left"], enabled: need, run: on((c) => c.seekBy(-0.1)) },
  { id: "view.audioSeekForward", title: "Audio: seek forward", group: "View", keys: ["Right"], enabled: need, run: on((c) => c.seekBy(0.1)) },
  { id: "view.audioZoomIn", title: "Audio: zoom in", group: "View", icon: ZoomIn, keys: ["="], enabled: need, run: on((c) => c.zoomBy(0.5)) },
  { id: "view.audioZoomOut", title: "Audio: zoom out", group: "View", icon: ZoomOut, keys: ["-"], enabled: need, run: on((c) => c.zoomBy(2)) },
  { id: "view.audioLoopIn", title: "Audio: loop starts here", group: "View", keys: ["["], enabled: need, run: on((c) => c.loopIn()) },
  { id: "view.audioLoopOut", title: "Audio: loop ends here", group: "View", keys: ["]"], enabled: need, run: on((c) => c.loopOut()) },
  { id: "view.audioPreviousWord", title: "Audio: previous word", group: "View", keys: [","], enabled: need, run: on((c) => c.stepWord(-1)) },
  { id: "view.audioNextWord", title: "Audio: next word", group: "View", keys: ["."], enabled: need, run: on((c) => c.stepWord(1)) },
];

export function registerAudioCommands(): void {
  for (const c of AUDIO_COMMANDS) commands.register(c);
}
