import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { useDock } from "@/shell/dock/store";
import { panels } from "@/shell/registries";
import { useSelection } from "@/shell/selection/store";
import { headlessDockview } from "@/shell/workspaces/testkit";
import { agentLabel, openToolCall, setAgentResolver } from "./attribution";
import { askAgent, attachSelectionToChat, currentChatSession, installAgentBridge, openChat, openChats, useChatBridge } from "./bridge";
import { rememberSessions } from "./labels";
import { session } from "./testdata";

// The context bridge against a real (headless) Dockview: which Chat shows which session, the badge's jump to the
// tool call, Ctrl/Cmd+I and Ask agent filling the composer.

if (!panels.get("chat")) {
  panels.register({ id: "chat", kind: "tool", title: "Chat", icon: () => null, singleton: false, defaultSize: { w: 400, h: 600 }, defaultLocation: "right", help: "panels.chat", empty: () => null, component: () => null });
}

let dispose: () => void;
beforeEach(() => {
  const dv = headlessDockview();
  dispose = dv.dispose;
  useDock.getState().setApi(dv.api);
  useSelection.setState({ activeDoc: null, selections: {}, pins: {} });
  useChatBridge.setState({ drafts: {}, lastChat: null, focus: {}, highlight: null });
  installAgentBridge();
});
afterEach(() => {
  useDock.getState().setApi(null);
  setAgentResolver({});
  dispose();
});

describe("attribution badge resolver", () => {
  it("names the session by driver and number once it is known", () => {
    const actor = { kind: "agent", id: "crd_1", name: "agent", sessionId: "ses_7" };
    expect(agentLabel(actor)).toBe("agent · session 7");
    rememberSessions([session({ id: "ses_7", number: 7, driver: "opencode" })]);
    expect(agentLabel(actor)).toBe("opencode · session 7");
  });

  it("opens the session's Chat and asks it to highlight the tool call", () => {
    expect(openToolCall({ actor: { kind: "agent", id: "crd_1", sessionId: "ses_7" }, toolCallId: "toolu_1" })).toBe(true);
    const api = useDock.getState().api!;
    expect(api.getPanel("chat")).toBeDefined(); // no Chat was open: the workspace Chat opens, pinned to the session
    expect(useSelection.getState().pins.chat).toBe("agent_session:ses_7");
    expect(useChatBridge.getState().highlight).toMatchObject({ sessionId: "ses_7", toolCallId: "toolu_1" });
    expect(useChatBridge.getState().lastChat).toBe("chat");
  });
});

describe("one Chat per session", () => {
  it("reuses the Chat showing a session, pins a free workspace Chat, else opens another", () => {
    expect(openChat("ses_1")).toBe("chat");
    expect(openChat("ses_2")).toBe("chat:agent_session:ses_2");
    expect(openChat("ses_1")).toBe("chat");
    expect(openChats()).toEqual([
      { instanceId: "chat", sessionId: "ses_1" },
      { instanceId: "chat:agent_session:ses_2", sessionId: "ses_2" },
    ]);
    expect(currentChatSession()).toBe("ses_1");
  });
});

describe("context bridge", () => {
  it("Ctrl/Cmd+I attaches the selection to the current Chat's composer and focuses it", () => {
    useSelection.getState().setActiveDoc("mix:mix_1");
    useSelection.getState().select("mix:mix_1", "groups/0");
    const target = attachSelectionToChat();
    expect(target).toBe("chat");
    const st = useChatBridge.getState();
    expect(st.drafts.chat?.refs).toEqual([{ ref: "@mix:mix_1#groups/0" }]);
    expect(st.focus.chat).toBe(1);
    attachSelectionToChat(); // twice: no duplicate chip
    expect(useChatBridge.getState().drafts.chat?.refs).toHaveLength(1);
  });

  it("Ask agent prefills a prompt naming the entity and the intent", () => {
    askAgent({ refs: [{ ref: "@mix:mix_1", label: "mix he-smoke" }], intent: "Evaluate this mix on the phone golden set" });
    expect(useChatBridge.getState().drafts.chat).toEqual({
      text: "Evaluate this mix on the phone golden set (@mix:mix_1)",
      refs: [{ ref: "@mix:mix_1", label: "mix he-smoke" }],
    });
  });
});
