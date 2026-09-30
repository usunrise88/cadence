// Cadence agent host (docs/spec/05-agents.md "Agent integration"): claims agent sessions from the control plane and
// runs each one — a worktree of the project repository on session/<id>, Claude Code (claude-agent-acp) or opencode
// (`opencode acp`) spawned through one ACP client (src/acp, src/drivers) as the session's own Unix user with the
// Cadence MCP server, a commit per turn, budgets, runaway checks and the stuck-turn clock — and reports every
// transcript entry and state change back (src/host).
//
//   npx tsx src/index.ts          run the host (configuration: src/host/config.ts)
//   npx tsx src/login.ts claude   put the Claude login into the agent-credentials volume (once)

import { driverFor } from "./drivers/index.ts";
import { HttpControlPlane } from "./host/api.ts";
import { realClock } from "./host/clock.ts";
import { loadConfig } from "./host/config.ts";
import { jsonLogger } from "./host/log.ts";
import { SessionManager } from "./host/manager.ts";

export const VERSION = process.env.CADENCE_VERSION ?? "dev";

export async function main(): Promise<void> {
  const log = jsonLogger((process.env.CADENCE_LOG_LEVEL as "info" | "debug" | undefined) ?? "info");
  const cfg = loadConfig(process.env);
  if (!cfg.uids) log.log("warn", "sessions run as the host's own user (not root, or CADENCE_SESSION_UIDS=off): no per-session isolation");
  if (!cfg.credentials) log.log("warn", "CADENCE_AGENT_CREDENTIALS is not set: agents use their default login (development only, R3)");
  const manager = new SessionManager({
    cp: new HttpControlPlane(cfg.baseUrl, cfg.token),
    clock: realClock,
    hostId: cfg.hostId,
    baseUrl: cfg.baseUrl,
    dataDir: cfg.dataDir,
    ...(cfg.credentials ? { credentials: cfg.credentials } : {}),
    ...(cfg.uids ? { uids: cfg.uids } : {}),
    hostEnv: process.env,
    log,
    driverFor,
    capacity: cfg.capacity,
    version: VERSION,
  });
  const stop = new AbortController();
  for (const sig of ["SIGINT", "SIGTERM"] as const) {
    process.once(sig, () => {
      log.log("info", "shutting down", { signal: sig });
      stop.abort();
    });
  }
  log.log("info", "cadence agent host started", { hostId: cfg.hostId, controlPlane: cfg.baseUrl, version: VERSION });
  await manager.run(stop.signal);
  await manager.shutdown();
}

if (import.meta.url === `file://${process.argv[1]}`) {
  main().catch((err: unknown) => {
    process.stderr.write(`cadence agent host: ${err instanceof Error ? err.message : String(err)}\n`);
    process.exit(1);
  });
}
