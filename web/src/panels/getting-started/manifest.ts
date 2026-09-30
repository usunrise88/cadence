import { Rocket } from "iconoir-react";
import type { PanelManifest } from "@/shell/panel";
import { GettingStartedEmpty, GettingStartedPanel } from "./GettingStartedPanel";
import { isDismissed } from "./steps";

const manifest: PanelManifest = {
  id: "getting-started",
  kind: "tool",
  title: "Getting started",
  icon: Rocket,
  singleton: true,
  defaultSize: { w: 360, h: 480 },
  defaultLocation: "right",
  inDefaults: () => !isDismissed(),
  help: "panels.getting-started",
  empty: GettingStartedEmpty,
  component: GettingStartedPanel,
};
export default manifest;
