import { FolderSettings } from "iconoir-react";
import type { PanelManifest } from "@/shell/panel";
import { AgentSettingsEmpty, AgentSettingsPanel } from "./AgentSettingsPanel";

const manifest: PanelManifest = {
  id: "agent-settings",
  kind: "tool",
  title: "Agent settings",
  icon: FolderSettings,
  singleton: true,
  defaultSize: { w: 520, h: 640 },
  defaultLocation: "right",
  help: "panels.agent-settings",
  commands: ["agentProfile.edit"],
  empty: AgentSettingsEmpty,
  component: AgentSettingsPanel,
};
export default manifest;
