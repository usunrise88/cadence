import { ChatBubble } from "iconoir-react";
import type { PanelManifest } from "@/shell/panel";
import { ChatEmpty, ChatPanel } from "./ChatPanel";
import { ChatTab } from "./ChatTab";

// One Chat per agent session (docs/spec/10-ui-shell.md "Panel manifest": "Chat is one per agent session"): the
// workspace's Chat (instance `chat`) is pinned to a session; others open as `chat:agent_session:<id>`. The tab names
// the session (CC · S4) and marks unread news.
const manifest: PanelManifest = {
  id: "chat",
  kind: "tool",
  title: "Chat",
  icon: ChatBubble,
  singleton: false,
  defaultSize: { w: 440, h: 640 },
  defaultLocation: "right",
  help: "panels.chat",
  commands: ["view.askAgent", "agentSessions.cancel", "agentSessions.pause", "agentSessions.resume", "agentMessages.new"],
  empty: ChatEmpty,
  component: ChatPanel,
  tab: ChatTab,
};
export default manifest;
