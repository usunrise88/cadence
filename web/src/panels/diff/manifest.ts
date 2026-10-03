import { Compress } from "iconoir-react";
import type { PanelManifest } from "@/shell/panel";
import { DiffEmpty, DiffPanel } from "./DiffPanel";

// Bottom of the Eval workspace, right column of Triage (docs/spec/11-ui-panels.md "Default workspaces"). Follows the
// active Eval report's selected utterance.
const manifest: PanelManifest = {
  id: "diff",
  kind: "tool",
  title: "Diff",
  icon: Compress,
  singleton: true,
  defaultSize: { w: 720, h: 280 },
  defaultLocation: "bottom",
  help: "panels.diff",
  empty: DiffEmpty,
  component: DiffPanel,
};
export default manifest;
