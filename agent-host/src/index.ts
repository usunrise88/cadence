// Cadence agent host.
//
// Built (phase 1, stream D): the ACP client (src/acp) and the drivers for Claude Code and opencode (src/drivers),
// proven by spike A1 (docs/spikes/A1-acp-client.md). Wave 2 adds the session manager on top of `Agent`:
//   1. create a worktree of the project's main on `session/<id>` with AGENTS.md, CLAUDE.md, .claude/skills
//   2. `Agent.start(driver, …)` → initialize; `newSession` with cwd = worktree, mcpServers = [Cadence MCP + token]
//   3. user messages → `prompt`; every HostUpdate → POST /agent-sessions/{id}/events
//   4. permissions: the `onPermission` callback → approval card; the policy engine answers the routine ones
//   5. cancel: POST /agent-sessions/{id}:cancel → `cancel`; pause/resume → `restoreSession`

import type { DriverName } from "./drivers/types.ts";

export type Driver = DriverName;

export interface AgentSession {
  id: string;
  driver: Driver;
  projectId: string;
  worktree: string;
}

export function describe(s: AgentSession): string {
  return `${s.driver} session ${s.id} in ${s.worktree} (project ${s.projectId})`;
}

export async function main(): Promise<void> {
  console.log("cadence agent host: the session manager arrives in phase 1 wave 2 (docs/review/2026-09-29-phase-1-plan.md)");
}

if (import.meta.url === `file://${process.argv[1]}`) void main();
