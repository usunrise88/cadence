// The agent host of the evals, in this process: the same SessionManager, drivers and HttpControlPlane that
// src/index.ts runs (unprivileged development mode: no per-session users, agents use the machine's own logins).
// Offline, every driver's agent command is replaced by the scripted agent (the driver still reads its tool calls,
// builds its session/new and answers its permissions); live, the real claude-agent-acp and `opencode acp` run.

import { mkdtemp, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { fileURLToPath } from "node:url";
import { driverFor } from "../src/drivers/index.ts";
import { HttpControlPlane } from "../src/host/api.ts";
import { realClock } from "../src/host/clock.ts";
import type { Logger } from "../src/host/log.ts";
import { SessionManager } from "../src/host/manager.ts";

const SCRIPTED = fileURLToPath(new URL("./scripted-agent.ts", import.meta.url));
const TSX = import.meta.resolve("tsx");

export interface Host {
  stop(): Promise<void>;
}

export async function startHost(opts: { url: string; token: string; offline: boolean; log: Logger }): Promise<Host> {
  const dataDir = await mkdtemp(join(tmpdir(), "cadence-evals-host-"));
  const manager = new SessionManager({
    cp: new HttpControlPlane(opts.url, opts.token),
    clock: realClock,
    hostId: `evals-${process.pid}`,
    baseUrl: opts.url,
    dataDir,
    hostEnv: process.env,
    log: opts.log,
    driverFor,
    capacity: 2,
    version: "evals",
    waitSeconds: 2,
    ...(opts.offline ? { launch: (d) => ({ command: process.execPath, args: ["--import", TSX, SCRIPTED, d.name] }) } : {}),
  });
  const stop = new AbortController();
  const running = manager.run(stop.signal);
  return {
    async stop() {
      stop.abort();
      await running;
      await manager.shutdown();
      await rm(dataDir, { recursive: true, force: true });
    },
  };
}
