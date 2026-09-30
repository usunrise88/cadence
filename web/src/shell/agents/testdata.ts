import type { AgentMessage, AgentSession } from "@/api/gen/types.gen";

// Fixtures for the agent-session tests (shell and Chat panel).

export function session(over: Partial<AgentSession> = {}): AgentSession {
  return {
    id: "ses_1",
    number: 3,
    projectId: "prj_1",
    project: "demo",
    kind: "interactive",
    driver: "claude-code",
    model: "sonnet",
    preset: "guardrails-default",
    state: "running",
    busy: false,
    turn: 1,
    branch: "session/ses_1",
    merge: { state: "none" },
    autoMerge: "never",
    budget: { turns: 40, tokens: 400_000, tokensPerTurn: 100_000 },
    use: { turns: 1, inputTokens: 1200, outputTokens: 300 },
    references: [],
    startedBy: { kind: "user", id: "usr_admin", name: "admin" },
    rev: 1,
    createdAt: "2026-09-30T10:00:00Z",
    updatedAt: "2026-09-30T10:00:00Z",
    ...over,
  };
}

let seq = 0;
export function message(over: Partial<AgentMessage> = {}): AgentMessage {
  seq++;
  return {
    id: `msg_${seq}`,
    sessionId: "ses_1",
    seq,
    kind: "agent_message",
    turn: 1,
    actor: { kind: "agent", id: "crd_1", name: "claude-code", sessionId: "ses_1" },
    rev: 1,
    createdAt: "2026-09-30T10:00:00Z",
    updatedAt: "2026-09-30T10:00:00Z",
    ...over,
  };
}
