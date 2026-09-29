import { InfoCircle } from "iconoir-react";
import type { PanelManifest } from "@/shell/panel";
import { InspectorEmpty, InspectorPanel } from "./InspectorPanel";

const manifest: PanelManifest = {
  id: "inspector",
  kind: "tool",
  title: "Inspector",
  icon: InfoCircle,
  singleton: true,
  defaultSize: { w: 320, h: 360 },
  defaultLocation: "right",
  help: "panels.inspector",
  empty: InspectorEmpty,
  component: InspectorPanel,
};
export default manifest;
