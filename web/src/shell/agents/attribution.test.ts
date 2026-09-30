import { afterEach, describe, expect, it, vi } from "vitest";
import { agentLabel, openToolCall, sessionShort, setAgentResolver } from "./attribution";

describe("agent attribution", () => {
  afterEach(() => setAgentResolver({}));

  it("labels the driver and the session", () => {
    expect(agentLabel({ kind: "agent", id: "crd_1", name: "opencode", sessionId: "ses_9" })).toBe("opencode · session 9");
    expect(agentLabel({ kind: "agent", id: "crd_1", name: "agent", sessionId: "ses_0019" })).toBe("agent · session 19");
    expect(agentLabel({ kind: "agent", id: "crd_1", sessionId: "ses_01a0efb4-639b-743e-a577-2244e71d9339" })).toBe("agent · session 01a0efb4");
    expect(sessionShort(undefined)).toBe("?");
  });

  it("uses the resolver the agent-session stream installs", () => {
    const open = vi.fn();
    const ref = { actor: { kind: "agent" as const, id: "crd_1", sessionId: "ses_x" }, toolCallId: "toolu_1" };
    expect(openToolCall(ref)).toBe(false);
    setAgentResolver({ label: (a) => (a.sessionId === "ses_x" ? "claude-code · session 3" : undefined), open });
    expect(agentLabel(ref.actor)).toBe("claude-code · session 3");
    expect(openToolCall(ref)).toBe(true);
    expect(open).toHaveBeenCalledWith(ref);
  });
});
