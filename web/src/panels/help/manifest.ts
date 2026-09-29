import { HelpCircle } from "iconoir-react";
import type { PanelManifest } from "@/shell/panel";
import { HelpEmpty, HelpPanel } from "./HelpPanel";

const manifest: PanelManifest = {
  id: "help",
  kind: "tool",
  title: "Help",
  icon: HelpCircle,
  singleton: true,
  defaultSize: { w: 360, h: 480 },
  defaultLocation: "right",
  help: "panels.help",
  empty: HelpEmpty,
  component: HelpPanel,
};
export default manifest;
