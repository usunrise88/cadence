import { GitFork } from "iconoir-react";
import type { PanelManifest } from "@/shell/panel";
import { LineageEmpty, LineagePanel } from "./LineagePanel";

// A tool that follows the active document (docs/spec/11-ui-panels.md "Panel catalogue", Lineage); the Eval workspace
// places it in the right column.
const manifest: PanelManifest = {
  id: "lineage",
  kind: "tool",
  title: "Lineage",
  icon: GitFork,
  singleton: true,
  defaultSize: { w: 640, h: 420 },
  defaultLocation: "right",
  help: "panels.lineage",
  empty: LineageEmpty,
  component: LineagePanel,
};
export default manifest;
