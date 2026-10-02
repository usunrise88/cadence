import { Microphone } from "iconoir-react";
import type { PanelManifest } from "@/shell/panel";
import { TranscriptionEmpty, TranscriptionPanel } from "./TranscriptionPanel";

// A tool, floating by default (docs/spec/11-ui-panels.md "Panel catalogue", Transcription; R47–R50). Renderer
// `always`: a live session and its capture survive switching tabs.
const manifest: PanelManifest = {
  id: "transcription",
  kind: "tool",
  title: "Transcription",
  icon: Microphone,
  singleton: true,
  defaultSize: { w: 820, h: 560 },
  defaultLocation: "floating",
  renderer: "always",
  help: "panels.transcription",
  empty: TranscriptionEmpty,
  component: TranscriptionPanel,
};
export default manifest;
