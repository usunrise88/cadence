import { ClipboardCheck } from "iconoir-react";
import type { PanelManifest } from "@/shell/panel";
import { TriageEmpty, TriagePanel } from "./TriagePanel";

// The centre of the Triage workspace (docs/spec/11-ui-panels.md "Default workspaces"). A tool panel that follows the
// project: the queue of disputed pseudo-labels and the Annotate mode of annotation batches. Renderer `always`: the
// audio and a half-typed transcript survive switching tabs.
const manifest: PanelManifest = {
  id: "triage",
  kind: "tool",
  title: "Triage",
  icon: ClipboardCheck,
  singleton: true,
  defaultSize: { w: 900, h: 640 },
  defaultLocation: "centre",
  renderer: "always",
  help: "panels.triage",
  commands: ["triage.accept", "triage.correct", "triage.reject", "annotations.new", "batches.new"],
  empty: TriageEmpty,
  component: TriagePanel,
};
export default manifest;
