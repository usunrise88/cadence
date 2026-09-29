// Cadence agent host — spike A1 skeleton.
//
// Responsibilities (see system spec, "Agent integration"):
//   1. create a worktree of the project's recipes branch with AGENTS.md, CLAUDE.md, .claude/skills
//   2. spawn the agent: `opencode acp` or the claude-agent-acp adapter
//   3. ACP: initialize → session/new (cwd = worktree, mcpServers = [Cadence MCP + session token])
//   4. relay: user messages → session/prompt; session/update → POST /agent-sessions/{id}/events
//   5. permissions: session/request_permission → approval card; policy engine answers the routine ones
//   6. cancel: POST /agent-sessions/{id}:cancel → session/cancel
//
// The driver interface is ACP-shaped; a native Claude Agent SDK driver may replace step 2–3 for named gaps only.

export type Driver = "claude" | "opencode";

export interface AgentSession {
  id: string;
  driver: Driver;
  projectId: string;
  worktree: string;
}

export async function main(): Promise<void> {
  console.log("cadence agent host stub — run spike A1 (docs/spikes/A1-acp-client.md) to fill this in");
}

main();
