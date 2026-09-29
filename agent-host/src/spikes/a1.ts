// Spike A1 harness (docs/spikes/A1-acp-client.md): runs the lifecycle against the real agents.
//
//   npm run spike:a1 -- [--driver claude|opencode|all] [--claude-model haiku] [--opencode-model opencode/big-pickle]
//                       [--record]     # write redacted transcripts to test/fixtures/ for the contract tests
//
// Needs a signed-in Claude Code for `claude` and a reachable opencode provider for `opencode`. Prompts are tiny.

import { randomBytes } from "node:crypto";
import { mkdir, mkdtemp, readFile, rm, writeFile } from "node:fs/promises";
import { homedir, tmpdir } from "node:os";
import { join } from "node:path";
import { parseArgs } from "node:util";
import { drivers } from "../drivers/index.ts";
import type { Driver, DriverName, LaunchOptions } from "../drivers/types.ts";
import { type LifecycleResult, type Phase, type RecordedMessage, runLifecycle } from "./lifecycle.ts";
import { startMcpEcho } from "./mcp-echo.ts";
import { redactor } from "./redact.ts";

const FIXTURES = new URL("../../test/fixtures/", import.meta.url);

const { values } = parseArgs({
  options: {
    driver: { type: "string", default: "all" },
    "claude-model": { type: "string", default: "haiku" },
    "opencode-model": { type: "string", default: "opencode/big-pickle" },
    record: { type: "boolean", default: false },
    keep: { type: "boolean", default: false },
  },
});

// Both agents' rendered config files (R7 shape), written for every driver as the preset renderer will: Claude's
// default mode already asks for edits, shell and MCP tools; opencode.json asks for edits, shell and the cadence MCP tools. XDG dirs isolate
// opencode's state (sessions, logs) per run, as a per-session config dir would (R3); Claude Code ignores them.
async function prepareWorkspace(driver: DriverName): Promise<{ cwd: string; env: NodeJS.ProcessEnv }> {
  const root = await mkdtemp(join(tmpdir(), `a1-${driver}-`));
  const cwd = join(root, "workspace");
  await mkdir(join(cwd, ".claude"), { recursive: true });
  await writeFile(join(cwd, "notes.txt"), "alpha\n");
  await writeFile(join(cwd, ".claude", "settings.json"), JSON.stringify({ permissions: { defaultMode: "default" } }, null, 2));
  await writeFile(
    join(cwd, "opencode.json"),
    JSON.stringify({ $schema: "https://opencode.ai/config.json", permission: { edit: "ask", bash: "ask", "cadence_*": "ask" } }, null, 2),
  );
  const env: NodeJS.ProcessEnv = {};
  for (const x of ["CONFIG", "DATA", "STATE", "CACHE"]) env[`XDG_${x}_HOME`] = join(root, x.toLowerCase());
  return { cwd, env };
}

async function runOne(name: DriverName): Promise<LifecycleResult & { mcpLog: unknown; notesAfter: string }> {
  const driver: Driver = drivers[name];
  const token = `cst_${randomBytes(16).toString("hex")}`;
  const nonce = randomBytes(3).toString("hex");
  const mcp = await startMcpEcho(token, nonce);
  const { cwd, env } = await prepareWorkspace(name);
  const model = name === "claude" ? values["claude-model"] : values["opencode-model"];
  const recorded: Record<Phase, RecordedMessage[]> = { main: [], restore: [] };
  console.log(`\n=== ${name} (model ${model}) cwd ${cwd}`);
  try {
    const result = await runLifecycle({
      driver,
      cwd,
      mcp: { url: mcp.url, token },
      launch: (): Omit<LaunchOptions, "cwd"> => ({ env, thoughts: true, ...(model ? { model } : {}) }),
      record: (phase, msg) => recorded[phase].push(msg),
      log: (l) => console.log(l),
    });
    const notesAfter = await readFile(join(cwd, "notes.txt"), "utf8");
    console.log(`  mcp server: ${JSON.stringify(mcp.log)}`);
    console.log(`  notes.txt after: ${JSON.stringify(notesAfter)}`);
    if (values.record) {
      const redact = redactor({ cwd, home: homedir(), token, mcpUrl: mcp.url });
      for (const phase of ["main", "restore"] as const) {
        const lines = recorded[phase].map((r) => JSON.stringify(redact(r)));
        await writeFile(new URL(`${name}-${phase}.jsonl`, FIXTURES), `${lines.join("\n")}\n`);
      }
      const summary = redact({ ...result, mcpLog: mcp.log, notesAfter, model, nonce });
      await writeFile(new URL(`${name}-summary.json`, FIXTURES), `${JSON.stringify(summary, null, 2)}\n`);
    }
    return { ...result, mcpLog: mcp.log, notesAfter };
  } finally {
    await mcp.close();
    if (!values.keep) await rm(join(cwd, ".."), { recursive: true, force: true });
  }
}

const names: DriverName[] = values.driver === "all" ? ["claude", "opencode"] : [values.driver as DriverName];
for (const n of names) {
  try {
    const r = await runOne(n);
    console.log(`  acp update kinds: ${JSON.stringify(r.acpKinds)}`);
    console.log(`  host update kinds: ${JSON.stringify(r.hostKinds)}`);
    console.log(`  client fs calls: ${JSON.stringify(r.clientFsCalls)}`);
    console.log(`  diffs: ${JSON.stringify(r.diffs)}`);
  } catch (err) {
    console.error(`${n} failed:`, err);
    process.exitCode = 1;
  }
}
