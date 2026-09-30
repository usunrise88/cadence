// The session manager against a scripted ACP agent (test/script-agent.ts), a real git repository standing in for
// the control plane's (per-turn commit and push), and an in-memory control plane: lifecycle, the credential scan,
// runaway and stuck-turn pauses on a fake clock, the permission round trip, budgets, resume, read-only sessions.

import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import { existsSync } from "node:fs";
import { mkdtemp, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { after, afterEach, before, describe, test } from "node:test";
import { fileURLToPath } from "node:url";
import { driverFor } from "../src/drivers/index.ts";
import type { AgentSession, ControlPlane, HostAsk, HostDecision, HostEntry, HostReport, HostStart, HostWork } from "../src/host/api.ts";
import { FakeClock } from "../src/host/clock.ts";
import { jsonLogger, silentLogger } from "../src/host/log.ts";
import { SessionManager } from "../src/host/manager.ts";
import type { HostSession } from "../src/host/session.ts";

const SCRIPT = fileURLToPath(new URL("./script-agent.ts", import.meta.url));

function git(cwd: string, ...args: string[]): string {
  return execFileSync("git", args, {
    cwd,
    env: { ...process.env, GIT_CONFIG_GLOBAL: "/dev/null", GIT_CONFIG_NOSYSTEM: "1", GIT_AUTHOR_NAME: "t", GIT_AUTHOR_EMAIL: "t@x", GIT_COMMITTER_NAME: "t", GIT_COMMITTER_EMAIL: "t@x" },
  }).toString();
}

class FakeCP implements ControlPlane {
  reports: Array<{ id: string; body: HostReport }> = [];
  asks: HostAsk[] = [];
  answer: (a: HostAsk) => HostDecision = () => ({ outcome: "allow_once" });

  claim(): Promise<HostWork> {
    return Promise.resolve({ start: [], messages: [], controls: [], decisions: [] });
  }
  report(id: string, body: HostReport): Promise<AgentSession> {
    this.reports.push({ id, body: structuredClone(body) });
    return Promise.resolve({} as AgentSession);
  }
  ask(_id: string, body: HostAsk): Promise<HostDecision> {
    this.asks.push(body);
    return Promise.resolve(this.answer(body));
  }

  entries(): HostEntry[] {
    // The latest state of every entry, in first-seen order (as the control plane's upserts keep them).
    const out = new Map<string, HostEntry>();
    for (const r of this.reports) for (const e of r.body.entries ?? []) out.set(e.key, e);
    return [...out.values()];
  }
  states(): string[] {
    return this.reports.flatMap((r) => (r.body.state ? [`${r.body.state.state}${r.body.state.busy ? "*" : ""}`] : []));
  }
  last(pred: (b: HostReport) => boolean): HostReport | undefined {
    return this.reports.map((r) => r.body).filter(pred).at(-1);
  }
}

async function waitFor(what: string, pred: () => boolean, ms = 10_000): Promise<void> {
  const end = Date.now() + ms;
  while (!pred()) {
    if (Date.now() > end) throw new Error(`timed out waiting for ${what}`);
    await new Promise((r) => setTimeout(r, 10));
  }
}

let root: string;
let origin: string;

before(async () => {
  root = await mkdtemp(join(tmpdir(), "host-test-"));
  origin = join(root, "origin.git");
  git(root, "init", "-q", "--bare", "-b", "main", origin);
  const seed = join(root, "seed");
  git(root, "clone", "-q", origin, seed);
  execFileSync("sh", ["-c", "echo facts > AGENTS.md"], { cwd: seed });
  git(seed, "add", ".");
  git(seed, "commit", "-q", "-m", "bootstrap");
  git(seed, "push", "-q", "origin", "HEAD:main");
});

after(async () => {
  await rm(root, { recursive: true, force: true });
});

let seq = 0;

function startFor(opts: { kind?: "interactive" | "read-only"; budget?: Partial<HostStart["budget"]>; stuck?: number } = {}): HostStart {
  const id = `ses_${++seq}`;
  const branch = opts.kind === "read-only" ? "" : `session/${id}`;
  if (branch) git(root, "--git-dir", origin, "branch", branch, "main");
  const budget = { turns: 10, tokens: 1_000_000, tokensPerTurn: 100_000, ...opts.budget };
  const session = {
    id, number: seq, projectId: "prj_1", project: "demo", kind: opts.kind ?? "interactive", driver: "claude-code", model: "haiku",
    preset: "guardrails-default", state: "created", busy: false, turn: 0, branch, merge: { state: "none" }, autoMerge: "when-clean",
    budget, use: { turns: 0, inputTokens: 0, outputTokens: 0 }, references: [], startedBy: { kind: "user", id: "usr_admin" },
    rev: 1, createdAt: "", updatedAt: "",
  } as unknown as AgentSession;
  return { session, token: "cst_test", cloneUrl: "/git/demo.git", mcpUrl: "/mcp", budget, clocks: { stuckTurnSeconds: opts.stuck ?? 300, identicalCalls: 3 } };
}

const managers: SessionManager[] = [];

// Stop every agent a test left running (a failed assertion must not leave child processes behind).
afterEach(async () => {
  await Promise.all(managers.splice(0).map((m) => m.shutdown()));
});

function harness(): { cp: FakeCP; clock: FakeClock; manager: SessionManager; dataDir: string } {
  const cp = new FakeCP();
  const clock = new FakeClock();
  const dataDir = join(root, `data-${++seq}`);
  const manager = new SessionManager({
    cp, clock, hostId: "host-1", baseUrl: "http://127.0.0.1:9", dataDir, hostEnv: process.env, log: process.env.HOST_TEST_LOG ? jsonLogger("debug") : silentLogger, driverFor,
    launch: () => ({ command: process.execPath, args: [SCRIPT] }), repoUrl: () => origin, flushMs: 5, capacity: 4, version: "test",
  });
  managers.push(manager);
  return { cp, clock, manager, dataDir };
}

function run(h: ReturnType<typeof harness>, start: HostStart): HostSession {
  h.manager.dispatch({ start: [start], messages: [], controls: [], decisions: [] });
  const s = h.manager.sessions.get(start.session.id);
  assert.ok(s);
  return s;
}

function say(h: ReturnType<typeof harness>, start: HostStart, text: string, id = `msg_${++seq}`): void {
  h.manager.dispatch({ start: [], messages: [{ id, sessionId: start.session.id, kind: "user_message", text }], controls: [], decisions: [] });
}

function control(h: ReturnType<typeof harness>, start: HostStart, action: "cancel" | "pause" | "resume" | "end"): void {
  h.manager.dispatch({ start: [], messages: [], controls: [{ id: `ctl_${++seq}`, sessionId: start.session.id, action }], decisions: [] });
}

const turnsEnded = (cp: FakeCP): number => cp.entries().filter((e) => e.kind === "turn" && e.turnInfo?.state === "ended").length;

describe("session manager", () => {
  test("lifecycle: clone, spawn with the MCP server, a turn, commit and push, end", async () => {
    const h = harness();
    const st = startFor();
    const s = run(h, st);
    await waitFor("running", () => h.cp.states().includes("running"));
    const running = h.cp.last((b) => b.state?.state === "running");
    assert.match(running?.acpSessionId ?? "", /^script-/);
    say(h, st, "edit notes.txt hello world");
    await waitFor("the turn", () => turnsEnded(h.cp) === 1 && h.cp.states().at(-1) === "running");
    await s.idle();
    const entries = h.cp.entries();
    const msg = entries.find((e) => e.kind === "agent_message");
    assert.equal(msg?.text, "edited");
    assert.equal(msg?.final, true);
    const edit = entries.find((e) => e.kind === "tool_call");
    assert.equal(edit?.toolCall?.class, "edit");
    assert.equal(edit?.toolCall?.status, "completed");
    assert.deepEqual(edit?.toolCall?.diffs?.map((d) => d.path), ["notes.txt"], "diff paths are relative to the worktree");
    const commit = entries.find((e) => e.kind === "commit");
    assert.deepEqual(commit?.commit?.files, ["notes.txt"]);
    assert.equal(git(root, "--git-dir", origin, "rev-parse", st.session.branch).trim(), commit?.commit?.sha, "the turn is pushed");
    assert.match(git(root, "--git-dir", origin, "log", "-1", "--format=%s", st.session.branch), /^cadence: turn 1 of session ses_/);
    assert.equal(git(root, "--git-dir", origin, "show", `${st.session.branch}:notes.txt`), "hello world\n");
    assert.equal(h.cp.last((b) => b.use !== undefined)?.use?.turns, 1);
    assert.deepEqual(h.cp.states().slice(-2), ["running*", "running"]);
    // The token is only in git's environment: never in .git/config.
    const wt = join(h.dataDir, "sessions", st.session.id, "worktree");
    assert.doesNotMatch(git(wt, "config", "--list", "--local"), /cst_|extraheader/i);
    control(h, st, "end");
    await waitFor("done", () => h.cp.states().at(-1) === "done");
    await waitFor("cleanup", () => !existsSync(join(h.dataDir, "sessions", st.session.id)));
    assert.equal(h.manager.sessions.size, 0);
  });

  test("a staged credential refuses the commit and tells the agent", async () => {
    const h = harness();
    const st = startFor();
    const s = run(h, st);
    await waitFor("running", () => h.cp.states().includes("running"));
    const before = git(root, "--git-dir", origin, "rev-parse", st.session.branch).trim();
    say(h, st, "secret");
    await waitFor("the turn", () => turnsEnded(h.cp) === 1 && h.cp.states().at(-1) === "running");
    await s.idle();
    const commit = h.cp.entries().find((e) => e.kind === "commit");
    assert.equal(commit?.commit?.refused, true);
    assert.deepEqual(commit?.commit?.findings?.map((f) => [f.path, f.kind]), [["leak.txt", "cst_"]]);
    assert.ok(!JSON.stringify(h.cp.reports).includes("cst_aaaa"), "the value is never reported");
    assert.ok(h.cp.entries().some((e) => e.kind === "notice" && e.level === "error" && /not committed/.test(e.text ?? "")));
    assert.equal(git(root, "--git-dir", origin, "rev-parse", st.session.branch).trim(), before, "nothing was pushed");
    say(h, st, "hello");
    await waitFor("the second turn", () => turnsEnded(h.cp) === 2 && h.cp.states().at(-1) === "running");
    await s.idle();
    const reply = h.cp.entries().filter((e) => e.kind === "agent_message").at(-1);
    assert.match(reply?.text ?? "", /were not committed because leak\.txt/, "the next prompt says why");
    control(h, st, "end");
    await waitFor("done", () => h.cp.states().at(-1) === "done");
  });

  test("three identical tool calls pause the session as a runaway; resume restores the ACP session", async () => {
    const h = harness();
    const st = startFor();
    run(h, st);
    await waitFor("running", () => h.cp.states().includes("running"));
    const acp = h.cp.last((b) => b.acpSessionId !== undefined)?.acpSessionId;
    say(h, st, "loop");
    await waitFor("the pause", () => h.cp.states().at(-1) === "paused");
    const paused = h.cp.last((b) => b.state?.state === "paused");
    assert.equal(paused?.state?.reason?.code, "runaway");
    assert.match(paused?.state?.reason?.message ?? "", /cadence\.mixes_edit was called 3 times in a row/);
    assert.match(paused?.note ?? "", /runaway/);
    h.manager.dispatch({
      start: [], messages: [], decisions: [],
      controls: [{ id: "c1", sessionId: st.session.id, action: "resume", resume: { acpSessionId: acp ?? "" } }],
    });
    await waitFor("running again", () => h.cp.states().at(-1) === "running");
    assert.ok(h.cp.entries().some((e) => e.kind === "notice" && /restored \(ACP session\/resume\)/.test(e.text ?? "")));
    say(h, st, "hello");
    await waitFor("a turn after the resume", () => turnsEnded(h.cp) === 2);
    control(h, st, "end");
    await waitFor("done", () => h.cp.states().at(-1) === "done");
  });

  test("the same tool with different arguments streamed into pending calls is not a runaway", async () => {
    const h = harness();
    const st = startFor();
    run(h, st);
    await waitFor("running", () => h.cp.states().includes("running"));
    say(h, st, "edits");
    await waitFor("the turn", () => turnsEnded(h.cp) === 1);
    assert.ok(!h.cp.states().includes("paused"), "four edits with different values never pause");
    control(h, st, "end");
    await waitFor("done", () => h.cp.states().at(-1) === "done");
  });

  test("a stuck turn pauses on the clock; a pending permission never does; the decision comes back", async () => {
    const h = harness();
    const st = startFor({ stuck: 300 });
    run(h, st);
    await waitFor("running", () => h.cp.states().includes("running"));
    // A permission a person decides: the clock runs far past the stuck limit while it waits.
    h.cp.answer = () => ({ outcome: "pending", approvalId: "apr_1", sessionId: st.session.id });
    say(h, st, "ask pip install torch");
    await waitFor("the ask", () => h.cp.asks.length === 1);
    assert.equal(h.cp.asks[0]?.toolCall.shell?.command, "pip install torch");
    assert.equal(h.cp.asks[0]?.toolCall.class, "shell");
    h.clock.advance(3_600_000);
    await new Promise((r) => setTimeout(r, 50));
    assert.ok(!h.cp.states().includes("paused"), "waiting for a person never pauses");
    h.manager.dispatch({ start: [], messages: [], controls: [], decisions: [{ sessionId: st.session.id, approvalId: "apr_1", outcome: "allow_always" }] });
    await waitFor("the answer", () => h.cp.entries().some((e) => e.text === "permission:always"));
    // An answer the preset gives at once.
    h.cp.answer = () => ({ outcome: "reject_once", rule: "shell.deny" });
    say(h, st, "ask curl x");
    await waitFor("the rejection", () => h.cp.entries().some((e) => e.text === "permission:no") && turnsEnded(h.cp) === 2 && h.cp.states().at(-1) === "running");
    // Now a turn that sends nothing: the stuck clock cancels and pauses it.
    say(h, st, "hang");
    await waitFor("the turn start", () => h.cp.states().at(-1) === "running*" && h.cp.entries().some((e) => e.key === "t3:start"));
    h.clock.advance(299_000);
    await new Promise((r) => setTimeout(r, 30));
    assert.notEqual(h.cp.states().at(-1), "paused");
    h.clock.advance(2_000);
    await waitFor("the stuck pause", () => h.cp.states().at(-1) === "paused");
    assert.equal(h.cp.last((b) => b.state?.state === "paused")?.state?.reason?.code, "stuck_turn");
    control(h, st, "end");
    await waitFor("done", () => h.cp.states().at(-1) === "done");
  });

  test("Cadence tools the preset allows are pre-allowed in the agent: no permission round trip; others still ask", async () => {
    const h = harness();
    const st = { ...startFor(), allowedTools: ["mixes.get", "mixes.edit"] };
    run(h, st);
    await waitFor("running", () => h.cp.states().includes("running"));
    say(h, st, "mcp mixes_edit");
    await waitFor("the first turn", () => h.cp.entries().some((e) => e.text?.startsWith("mcp:")) && turnsEnded(h.cp) === 1);
    assert.ok(h.cp.entries().some((e) => e.text === "mcp:pre-allowed"));
    assert.equal(h.cp.asks.length, 0, "an allowed Cadence tool never reaches the policy engine");
    h.cp.answer = () => ({ outcome: "reject_once", rule: "admin-only" });
    say(h, st, "mcp secrets_new");
    await waitFor("the second turn", () => turnsEnded(h.cp) === 2);
    assert.equal(h.cp.asks.length, 1, "a tool the preset does not allow still asks");
    assert.equal(h.cp.asks[0]?.toolCall.class, "mcp");
    assert.match(h.cp.asks[0]?.toolCall.operation ?? h.cp.asks[0]?.toolCall.title ?? "", /secrets[._]new/);
    assert.ok(h.cp.entries().some((e) => e.text === "mcp:asked:no"));
    control(h, st, "end");
    await waitFor("done", () => h.cp.states().at(-1) === "done");
  });

  test("a cancelled turn withdraws its pending permission", async () => {
    const h = harness();
    const st = startFor();
    const s = run(h, st);
    await waitFor("running", () => h.cp.states().includes("running"));
    h.cp.answer = () => ({ outcome: "pending", approvalId: "apr_9" });
    say(h, st, "ask rm -rf build");
    await waitFor("the ask", () => h.cp.asks.length === 1);
    control(h, st, "cancel");
    await waitFor("the turn end", () => turnsEnded(h.cp) === 1);
    await s.idle();
    assert.ok(h.cp.reports.some((r) => r.body.withdraw?.includes("apr_9")));
    control(h, st, "end");
    await waitFor("done", () => h.cp.states().at(-1) === "done");
  });

  test("budgets: a turn over its token limit, the session's turns", async () => {
    const h = harness();
    const st = startFor({ budget: { tokensPerTurn: 1000, turns: 2 } });
    run(h, st);
    await waitFor("running", () => h.cp.states().includes("running"));
    say(h, st, "usage 5000");
    await waitFor("the pause", () => h.cp.states().at(-1) === "paused");
    assert.equal(h.cp.last((b) => b.state?.state === "paused")?.state?.reason?.code, "turn_tokens");
    h.manager.dispatch({ start: [], messages: [], decisions: [], controls: [{ id: "c", sessionId: st.session.id, action: "resume", budget: { turns: 2, tokens: 1_000_000, tokensPerTurn: 100_000 } }] });
    await waitFor("running again", () => h.cp.states().at(-1) === "running");
    say(h, st, "hello");
    await waitFor("the turn budget pause", () => h.cp.states().at(-1) === "paused" && turnsEnded(h.cp) === 2);
    assert.equal(h.cp.last((b) => b.state?.state === "paused")?.state?.reason?.code, "budget_turns");
    control(h, st, "end");
    await waitFor("done", () => h.cp.states().at(-1) === "done");
  });

  test("thought chunks coalesce into one entry per block", async () => {
    const h = harness();
    const st = startFor();
    const s = run(h, st);
    await waitFor("running", () => h.cp.states().includes("running"));
    say(h, st, "think");
    await waitFor("the turn", () => turnsEnded(h.cp) === 1 && h.cp.states().at(-1) === "running");
    await s.idle();
    const thoughts = h.cp.entries().filter((e) => e.kind === "thought");
    assert.equal(thoughts.length, 1);
    assert.equal(thoughts[0]?.final, true);
    assert.equal(thoughts[0]?.text, Array.from({ length: 50 }, (_, i) => `t${i} `).join(""));
    const sent = h.cp.reports.flatMap((r) => r.body.entries ?? []).filter((e) => e.kind === "thought").length;
    assert.ok(sent < 50, `${sent} thought entries sent for 50 chunks`);
    control(h, st, "end");
    await waitFor("done", () => h.cp.states().at(-1) === "done");
  });

  test("a read-only session answers one message, commits nothing and ends", async () => {
    const h = harness();
    const st = startFor({ kind: "read-only" });
    run(h, st);
    await waitFor("running", () => h.cp.states().includes("running"));
    say(h, st, "edit x.txt no");
    await waitFor("done", () => h.cp.states().at(-1) === "done");
    assert.ok(!h.cp.entries().some((e) => e.kind === "commit"));
    assert.equal(git(root, "--git-dir", origin, "log", "-1", "--format=%s", "main").trim(), "bootstrap");
  });
});
