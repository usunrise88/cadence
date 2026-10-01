import { Bookmark } from "iconoir-react";
import type { PanelManifest } from "@/shell/panel";
import { CheckpointsEmpty, CheckpointsPanel } from "./CheckpointsPanel";

// Right column of the Training workspace (docs/spec/11-ui-panels.md "Default workspaces").
const manifest: PanelManifest = {
  id: "checkpoints",
  kind: "tool",
  title: "Checkpoints",
  icon: Bookmark,
  singleton: true,
  defaultSize: { w: 420, h: 520 },
  defaultLocation: "right",
  help: "panels.checkpoints",
  commands: ["checkpoints.average", "runs.stage"],
  empty: CheckpointsEmpty,
  component: CheckpointsPanel,
};
export default manifest;
