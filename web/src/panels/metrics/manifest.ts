import { GraphUp } from "iconoir-react";
import type { PanelManifest } from "@/shell/panel";
import { MetricsEmpty, MetricsPanel } from "./MetricsPanel";

// Bottom row of the Training workspace (docs/spec/11-ui-panels.md "Default workspaces").
const manifest: PanelManifest = {
  id: "metrics",
  kind: "tool",
  title: "Metrics",
  icon: GraphUp,
  singleton: true,
  defaultSize: { w: 760, h: 320 },
  defaultLocation: "bottom",
  help: "panels.metrics",
  empty: MetricsEmpty,
  component: MetricsPanel,
};
export default manifest;
