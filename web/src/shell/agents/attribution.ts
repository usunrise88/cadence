// Agent attribution (docs/spec/06-platform.md "Attribution"): a change an agent made shows a badge such as
// "opencode · session 9"; clicking it scrolls the Chat panel to the tool call that caused it.
//
// The agent-session stream owns sessions (driver, ordinal) and the Chat panel, so it installs a resolver here:
// `label` names the session the way people know it and `open` jumps to the tool call. Until then the badge names
// the actor and the session id, and a click says where the tool call will show.

/** An actor as events and entities carry it (the contract's Actor). */
export type ActorRef = { kind: string; id: string; name?: string; sessionId?: string };

export type AgentRef = { actor: ActorRef; toolCallId?: string };

export type AgentResolver = {
  /** "opencode · session 9"; undefined falls back to the default label. */
  label?: (actor: AgentRef["actor"]) => string | undefined;
  /** Scroll the Chat panel of the session to the tool call. */
  open?: (ref: AgentRef) => void;
};

let resolver: AgentResolver = {};

/** Installed by the agent-session stream (Chat panel); replaces the previous resolver. */
export function setAgentResolver(r: AgentResolver): void {
  resolver = r;
}

/** The session part of a label: the number of a numbered id ("ses_9" → 9), else the head of the id. */
export function sessionShort(sessionId: string | undefined): string {
  if (!sessionId) return "?";
  const n = /^(?:[a-z]+_)?(?:[a-z]+)?(\d{1,6})$/.exec(sessionId);
  if (n) return String(Number(n[1]));
  const bare = sessionId.replace(/^[a-z]+_/, "");
  return bare.slice(0, 8);
}

/** "opencode · session 9": the driver (the actor's name when it is one) and the session. */
export function agentLabel(actor: AgentRef["actor"]): string {
  const custom = resolver.label?.(actor);
  if (custom) return custom;
  const who = actor.name && actor.name !== "agent" ? actor.name : "agent";
  return `${who} · session ${sessionShort(actor.sessionId)}`;
}

/** Whether a click on the badge can reach the tool call yet. */
export function canOpenToolCall(): boolean {
  return !!resolver.open;
}

/** The badge's click: the Chat panel at the tool call, once the agent-session stream installs it. */
export function openToolCall(ref: AgentRef): boolean {
  if (!resolver.open) return false;
  resolver.open(ref);
  return true;
}
