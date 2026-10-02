import type { AgentSession, Branch } from "@/api/gen/types.gen";

// Which branches of the project repository wait for a person (the status bar's Branches badge; the server's
// bootstrap.WaitingBranches is the same rule): ahead of main, and nobody working on it — a template sync, another
// branch, or the branch of an agent session that ended. A live session's branch is its agent's, not yet a person's.

const LIVE = new Set(["created", "running", "waiting_approval", "paused"]);

export function waitingBranches(branches: Branch[], sessions: AgentSession[]): Branch[] {
  const live = new Set(sessions.filter((s) => LIVE.has(s.state)).map((s) => s.id));
  return branches.filter((b) => b.ahead > 0 && !(b.kind === "session" && b.sessionId && live.has(b.sessionId)));
}

/** Why a branch waits, in a few words. */
export function waitingReason(b: Branch): string {
  if (b.kind === "sync") return "template sync";
  if (b.kind === "session") return "agent session ended without merging";
  return "branch ahead of main";
}
