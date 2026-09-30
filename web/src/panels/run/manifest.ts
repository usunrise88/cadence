import { Play } from "iconoir-react";
import type { PanelManifest } from "@/shell/panel";
import { RunEmpty, RunPanel } from "./RunPanel";

// Centre of the Training workspace (docs/spec/11-ui-panels.md "Default workspaces"); opens from links.
const manifest: PanelManifest = {
  id: "run",
  kind: "document",
  title: "Run",
  icon: Play,
  singleton: false,
  defaultSize: { w: 820, h: 620 },
  defaultLocation: "centre",
  entity: "run",
  help: "panels.run",
  commands: ["runs.stage", "runs.resume", "jobs.pause", "jobs.resume", "jobs.cancel"],
  empty: RunEmpty,
  component: RunPanel,
};
export default manifest;
