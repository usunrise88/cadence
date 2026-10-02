import { SoundHigh } from "iconoir-react";
import type { PanelManifest } from "@/shell/panel";
import { AudioEmpty, AudioPanel } from "./AudioPanel";

// Floating by default (docs/spec/11-ui-panels.md "Default workspaces": the Eval workspace's floating Audio). Renderer
// `always`: playback and the view's state survive switching tabs.
const manifest: PanelManifest = {
  id: "audio",
  kind: "tool",
  title: "Audio",
  icon: SoundHigh,
  singleton: true,
  defaultSize: { w: 760, h: 380 },
  defaultLocation: "floating",
  renderer: "always",
  help: "panels.audio",
  empty: AudioEmpty,
  component: AudioPanel,
};
export default manifest;
