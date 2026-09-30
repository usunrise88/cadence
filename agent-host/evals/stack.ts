// The system under test, started the way the Playwright specs start it: web/e2e/stack.sh runs a throwaway Postgres
// in Docker and `cadence serve` on private ports, and writes the agent-host token into the tools directory. The
// agent host itself runs in this process (host.ts). Nothing here touches the compose stand.

import { type ChildProcess, spawn } from "node:child_process";
import { createWriteStream, existsSync, readFileSync } from "node:fs";
import { mkdtemp } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { fileURLToPath } from "node:url";

const STACK = fileURLToPath(new URL("../../web/e2e/stack.sh", import.meta.url));

export interface Stack {
  url: string;
  hostToken: string;
  stop(): Promise<void>;
}

async function waitHealthy(url: string, proc: ChildProcess, ms: number): Promise<void> {
  const end = Date.now() + ms;
  while (Date.now() < end) {
    if (proc.exitCode !== null) throw new Error(`web/e2e/stack.sh exited with ${proc.exitCode} before the control plane was up`);
    try {
      const res = await fetch(new URL("/healthz", url));
      if (res.ok) return;
    } catch {
      // not up yet
    }
    await new Promise((r) => setTimeout(r, 500));
  }
  throw new Error(`the control plane at ${url} was not healthy after ${ms / 1000} s`);
}

/** Starts Postgres and the control plane; the stack's output goes to logFile. */
export async function startStack(opts: { pgPort: number; apiPort: number; logFile: string }): Promise<Stack> {
  const tools = await mkdtemp(join(tmpdir(), "cadence-evals-tools-"));
  const log = createWriteStream(opts.logFile, { flags: "a" });
  const proc = spawn("bash", [STACK], {
    detached: true, // its own process group: stop() ends cadence serve and the script, whose trap removes Postgres
    stdio: ["ignore", "pipe", "pipe"],
    env: { ...process.env, E2E_PG_PORT: String(opts.pgPort), E2E_API_PORT: String(opts.apiPort), E2E_TOOLS_DIR: tools },
  });
  proc.stdout?.pipe(log);
  proc.stderr?.pipe(log);
  const exited = new Promise<void>((r) => proc.once("exit", () => r()));
  const url = `http://127.0.0.1:${opts.apiPort}`;
  const stop = async (): Promise<void> => {
    if (proc.exitCode === null && proc.pid) {
      try {
        process.kill(-proc.pid, "SIGTERM");
      } catch {
        // already gone
      }
      await Promise.race([exited, new Promise((r) => setTimeout(r, 20_000))]);
    }
    log.end();
  };
  try {
    await waitHealthy(url, proc, 240_000);
    const tokenFile = join(tools, "host-token");
    const end = Date.now() + 10_000;
    while (!existsSync(tokenFile) && Date.now() < end) await new Promise((r) => setTimeout(r, 100));
    return { url, hostToken: readFileSync(tokenFile, "utf8").trim(), stop };
  } catch (err) {
    await stop();
    throw err;
  }
}
