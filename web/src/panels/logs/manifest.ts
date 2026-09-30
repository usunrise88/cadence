import { Terminal } from "iconoir-react";
import type { PanelManifest } from "@/shell/panel";
import { LogsEmpty, LogsPanel } from "./LogsPanel";

// Bottom row of the Training, Data and Ops workspaces (docs/spec/11-ui-panels.md "Default workspaces").
const manifest: PanelManifest = {
  id: "logs",
  kind: "tool",
  title: "Logs",
  icon: Terminal,
  singleton: true,
  defaultSize: { w: 720, h: 280 },
  defaultLocation: "bottom",
  help: "panels.logs",
  empty: LogsEmpty,
  component: LogsPanel,
};
export default manifest;
