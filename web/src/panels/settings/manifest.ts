import { Settings } from "iconoir-react";
import type { PanelManifest } from "@/shell/panel";
import { SettingsEmpty, SettingsPanel } from "./SettingsPanel";

const manifest: PanelManifest = {
  id: "settings",
  kind: "tool",
  title: "Settings",
  icon: Settings,
  singleton: true,
  defaultSize: { w: 820, h: 560 },
  defaultLocation: "floating",
  help: "panels.settings",
  commands: ["compute.edit", "secrets.new", "credentials.new", "credentials.revoke", "policies.edit"],
  empty: SettingsEmpty,
  component: SettingsPanel,
};
export default manifest;
