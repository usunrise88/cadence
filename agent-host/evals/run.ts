// The evals runner (npm run evals, make evals): starts Postgres and the control plane (web/e2e/stack.sh) and the
// agent host in this process, runs every eval × driver on a fresh fixture project, grades what the Cadence API
// shows, writes evals/results/<timestamp>.json and prints the table. Exit code 1 when a run failed.
//
//   npm run evals                                   offline: the scripted agent in each driver's shape (CI)
//   npm run evals -- --eval gated-baseline --driver claude
//   CADENCE_LIVE_AGENTS=1 npm run evals             live: claude-agent-acp and `opencode acp` with real models
//
// Environment: CADENCE_LIVE_AGENTS (1 or all = both drivers, or a list: claude,opencode), CADENCE_LIVE_CLAUDE_MODEL
// (default sonnet), CADENCE_LIVE_OPENCODE_MODEL (default: the project's configured opencode model), E2E_PG_PORT and
// E2E_API_PORT (default 55437 / 18087), EVALS_KEEP_LOGS=1 keeps the stack and host logs of a passing run.

import { createWriteStream, existsSync } from "node:fs";
import { mkdir, rm, writeFile } from "node:fs/promises";
import { homedir } from "node:os";
import { join } from "node:path";
import { fileURLToPath } from "node:url";
import { parseArgs } from "node:util";
import type { AgentSessionNew } from "../src/api/gen/types.gen.ts";
import type { Logger } from "../src/host/log.ts";
import { CadenceApi } from "./api.ts";
import { EVALS } from "./evals.ts";
import { grade, metricsOf } from "./graders.ts";
import { startHost } from "./host.ts";
import { buildReport, EMPTY_METRICS, formatTable, reportName, runResult } from "./report.ts";
import { startStack } from "./stack.ts";
import type { AgentSession, Eval, EvalDriver, Observation, RunResult } from "./types.ts";

const RESULTS = fileURLToPath(new URL("./results/", import.meta.url));
const DRIVERS: readonly EvalDriver[] = ["claude", "opencode"];
const ADMIN = { username: "admin", password: "evals admin password" };

/** The drivers CADENCE_LIVE_AGENTS names; empty = offline. */
export function liveDrivers(v: string | undefined): EvalDriver[] {
  const s = (v ?? "").trim().toLowerCase();
  if (!s || s === "0" || s === "false") return [];
  if (s === "1" || s === "true" || s === "all") return [...DRIVERS];
  return s.split(",").map((x) => x.trim()).filter((x): x is EvalDriver => (DRIVERS as readonly string[]).includes(x));
}

function liveModel(d: EvalDriver): string | undefined {
  return d === "claude" ? (process.env.CADENCE_LIVE_CLAUDE_MODEL ?? "sonnet") : process.env.CADENCE_LIVE_OPENCODE_MODEL;
}

/** Why a live driver cannot run here (no login it could use), or undefined. */
function missingCredentials(d: EvalDriver): string | undefined {
  const env = process.env;
  const vol = env.CADENCE_AGENT_CREDENTIALS;
  if (vol && existsSync(join(vol, d === "claude" ? "claude/oauth-token" : "opencode/auth.json"))) return undefined;
  if (d === "claude") {
    const dir = env.CLAUDE_CONFIG_DIR ?? join(homedir(), ".claude");
    if (env.CLAUDE_CODE_OAUTH_TOKEN || env.ANTHROPIC_API_KEY || existsSync(join(dir, ".credentials.json"))) return undefined;
    return "no Claude login: sign in with `claude` or set CLAUDE_CODE_OAUTH_TOKEN";
  }
  if (liveModel(d)?.startsWith("opencode/")) return undefined; // OpenCode Zen's free models need no key
  const data = env.XDG_DATA_HOME ?? join(homedir(), ".local", "share");
  if (existsSync(join(data, "opencode", "auth.json"))) return undefined;
  return "no opencode provider login (opencode auth login) and no free opencode/* model in CADENCE_LIVE_OPENCODE_MODEL";
}

function fileLogger(path: string): Logger & { close(): void } {
  const out = createWriteStream(path, { flags: "a" });
  return {
    log(level, msg, attrs) {
      out.write(`${JSON.stringify({ time: new Date().toISOString(), level, msg, ...attrs })}\n`);
    },
    close: () => out.end(),
  };
}

const TERMINAL = new Set(["done", "failed", "cancelled"]);

/** The session answered the prompt: its first turn ended (or it stopped: ended, failed, paused). */
function answered(s: AgentSession): boolean {
  if (TERMINAL.has(s.state) || s.state === "paused") return true;
  return (s.turn ?? 0) >= 1 && !s.busy && !s.pendingControl && (s.state === "running" || s.state === "waiting_approval");
}

async function waitFor(api: CadenceApi, id: string, until: (s: AgentSession) => boolean, deadline: number): Promise<AgentSession> {
  for (;;) {
    const s = await api.session(id);
    if (until(s) || Date.now() > deadline) return s;
    await new Promise((r) => setTimeout(r, 250));
  }
}

let projectSeq = 0;

async function runOne(api: CadenceApi, e: Eval, driver: EvalDriver, live: boolean): Promise<RunResult> {
  const mode = live ? "live" : "offline";
  const base = { eval: e.id, driver, model: live ? (liveModel(driver) ?? "profile default") : "scripted", mode } as const;
  try {
    const slug = `ev-${++projectSeq}-${Date.now().toString(36)}`;
    const project = await api.newProject(slug, `Evals: ${e.id} (${driver})`);
    for (const m of e.fixture.mixes) await api.newMix(slug, m);

    const body: AgentSessionNew = { kind: e.kind, driver: driver === "claude" ? "claude-code" : "opencode", prompt: e.prompt };
    const model = live ? liveModel(driver) : undefined;
    if (model) body.model = model;
    const started = Date.now();
    const created = await api.newSession(slug, body);
    let s = await waitFor(api, created.id, answered, started + e.budget.wallSeconds * 1000);
    const wallMs = Date.now() - started;

    // Read before the end: ending a session denies its open permission requests.
    const approvals = await api.approvals(slug);
    if (!TERMINAL.has(s.state)) {
      await api.endSession(s);
      s = await waitFor(api, s.id, (x) => TERMINAL.has(x.state), Date.now() + 30_000);
    }
    const mixes: Observation["mixes"] = {};
    const all = await api.mixes(slug);
    for (const name of e.observe.mixes) {
      const mix = all.find((m) => m.name === name);
      mixes[name] = mix ? { mix, drafts: await api.drafts(mix.id) } : null;
    }
    const aliases: Observation["aliases"] = {};
    for (const name of e.observe.aliases) aliases[name] = await api.alias(slug, name);
    const o: Observation = {
      eval: e.id,
      driver,
      project: { slug, id: project.id },
      session: s,
      transcript: await api.transcript(s.id),
      audit: (await api.audit(slug)).filter((a) => a.actor.sessionId === s.id),
      approvals,
      mixes,
      aliases,
      wallMs,
    };
    return runResult({
      ...base,
      model: live ? s.model : "scripted",
      graders: grade(e.graders, o),
      metrics: metricsOf(o),
      session: { id: s.id, state: s.state, project: slug },
    });
  } catch (err) {
    return runResult({ ...base, graders: [], metrics: EMPTY_METRICS, error: err instanceof Error ? err.message : String(err) });
  }
}

async function main(): Promise<number> {
  const { values } = parseArgs({
    options: { eval: { type: "string", multiple: true }, driver: { type: "string", multiple: true }, list: { type: "boolean" } },
  });
  if (values.list) {
    for (const e of EVALS) console.log(`${e.id}\t${e.kind}\t${e.title}`);
    return 0;
  }
  const pick = (values.eval ?? []).flatMap((v) => v.split(","));
  const evals = pick.length ? EVALS.filter((e) => pick.includes(e.id)) : [...EVALS];
  if (pick.length && evals.length !== pick.length) throw new Error(`unknown eval in ${pick.join(", ")}; --list shows them`);
  const live = liveDrivers(process.env.CADENCE_LIVE_AGENTS);
  const wanted = (values.driver ?? []).flatMap((v) => v.split(","));
  let drivers = live.length ? live : [...DRIVERS];
  if (wanted.length) drivers = drivers.filter((d) => wanted.includes(d));
  if (!drivers.length) throw new Error("no driver to run (check --driver and CADENCE_LIVE_AGENTS)");

  const startedAt = new Date();
  const name = reportName(startedAt);
  await mkdir(RESULTS, { recursive: true });
  const stackLog = join(RESULTS, name.replace(/\.json$/, ".stack.log"));
  const hostLog = fileLogger(join(RESULTS, name.replace(/\.json$/, ".host.log")));
  const mode = live.length ? "live" : "offline";
  console.log(`cadence evals (${mode}): ${evals.length} eval(s) × ${drivers.join(", ")}; starting Postgres and the control plane…`);

  const stack = await startStack({
    pgPort: Number(process.env.E2E_PG_PORT ?? 55437),
    apiPort: Number(process.env.E2E_API_PORT ?? 18087),
    logFile: stackLog,
  });
  const runs: RunResult[] = [];
  try {
    const api = new CadenceApi(stack.url);
    await api.signIn(ADMIN.username, ADMIN.password);
    const host = await startHost({ url: stack.url, token: stack.hostToken, offline: !live.length, log: hostLog });
    try {
      for (const e of evals) {
        for (const d of drivers) {
          const missing = live.length ? missingCredentials(d) : undefined;
          const r = missing
            ? runResult({ eval: e.id, driver: d, model: liveModel(d) ?? "profile default", mode: "live", graders: [], metrics: EMPTY_METRICS, error: missing })
            : await runOne(api, e, d, live.length > 0);
          runs.push(r);
          console.log(`  ${r.pass ? "PASS" : "FAIL"}  ${e.id} × ${d}${r.error ? ` (${r.error})` : ""}`);
        }
      }
    } finally {
      await host.stop();
    }
  } finally {
    await stack.stop();
    hostLog.close();
  }
  const report = buildReport({ startedAt: startedAt.toISOString(), finishedAt: new Date().toISOString(), mode, controlPlane: stack.url, runs });
  const out = join(RESULTS, name);
  await writeFile(out, `${JSON.stringify(report, null, 2)}\n`);
  console.log(`\n${formatTable(report)}\n\nresults: ${out}`);
  const failed = report.summary.failed > 0;
  if (failed || process.env.EVALS_KEEP_LOGS === "1") console.log(`logs: ${stackLog}, ${join(RESULTS, name.replace(/\.json$/, ".host.log"))}`);
  else await Promise.all([rm(stackLog, { force: true }), rm(join(RESULTS, name.replace(/\.json$/, ".host.log")), { force: true })]);
  return failed ? 1 : 0;
}

if (import.meta.url === `file://${process.argv[1]}`) {
  main().then(
    (code) => process.exit(code),
    (err: unknown) => {
      process.stderr.write(`cadence evals: ${err instanceof Error ? err.message : String(err)}\n`);
      process.exit(2);
    },
  );
}
