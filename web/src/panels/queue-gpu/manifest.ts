import { Cpu } from "iconoir-react";
import type { PanelManifest } from "@/shell/panel";
import { QueueGpuEmpty, QueueGpuPanel } from "./QueueGpuPanel";

// Ops workspace, left column (docs/spec/11-ui-panels.md "Default workspaces").
const manifest: PanelManifest = {
  id: "queue-gpu",
  kind: "tool",
  title: "Queue & GPU",
  icon: Cpu,
  singleton: true,
  defaultSize: { w: 520, h: 640 },
  defaultLocation: "left",
  help: "panels.queue-gpu",
  commands: ["jobs.edit", "jobs.pause", "jobs.resume", "jobs.cancel"],
  empty: QueueGpuEmpty,
  component: QueueGpuPanel,
};
export default manifest;
