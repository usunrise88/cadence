import { Community } from "iconoir-react";
import type { PanelManifest } from "@/shell/panel";
import { AgentSessionsEmpty, AgentSessionsPanel } from "./AgentSessionsPanel";

// No default workspace column (docs/spec/11-ui-panels.md "Default workspaces"): Agent sessions opens floating from
// the status-bar badge, the palette or a Chat.
const manifest: PanelManifest = {
  id: "agent-sessions",
  kind: "tool",
  title: "Agent sessions",
  icon: Community,
  singleton: true,
  defaultSize: { w: 460, h: 560 },
  defaultLocation: "floating",
  help: "panels.agent-sessions",
  commands: ["agentSessions.new", "playbooks.run", "agentSessions.pause", "agentSessions.resume", "agentSessions.accept", "agentSessions.revert"],
  empty: AgentSessionsEmpty,
  component: AgentSessionsPanel,
};
export default manifest;
