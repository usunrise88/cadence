import assert from "node:assert/strict";
import { describe, test } from "node:test";
import type { AgentSession } from "./api.ts";
import { isWaitTool, playbookPreamble } from "./playbook.ts";

const base: Omit<AgentSession, "kind"> = {
  id: "ses_1", number: 1, projectId: "prj_1", project: "hebrew", driver: "claude-code", model: "sonnet", preset: "guardrails-default",
  state: "running", branch: "session/ses_1", merge: { state: "none" }, autoMerge: "when-clean",
  budget: { turns: 10, tokens: 1000, tokensPerTurn: 100 }, use: { turns: 0, inputTokens: 0, outputTokens: 0 }, references: [],
  startedBy: { kind: "user", id: "usr_admin" }, rev: 1, createdAt: "2026-09-30T00:00:00Z", updatedAt: "2026-09-30T00:00:00Z",
};

const range = (value: number) => ({ value, low: value / 2, high: value * 1.5 });

const playbookSession: AgentSession = {
  ...base,
  kind: "playbook",
  playbook: {
    name: "finetune-from-dataset", title: "Fine-tune from a dataset version", state: "running", inputs: {},
    estimate: { basis: "mixed", plusMinus: 0.5, gpuHours: range(0.24), durationSeconds: range(860), steps: [] },
    plan: [
      { id: "mix", title: "Mix the dataset with replay", command: "mixes.new", state: "done", note: "mix_1", spending: false },
      { id: "calibrate", title: "Calibrate", command: "runs.calibrate", state: "pending", spending: true },
      { id: "watch", title: "Wait for the run", command: "jobs.wait", until: "terminal", state: "pending", spending: false },
      { id: "eval", title: "Eval matrix", command: "evals.new", phase: 3, state: "skipped", note: "arrives in roadmap phase 3", spending: false },
    ],
  },
};

describe("playbook sessions", () => {
  test("the preamble lists the plan with its state and the rules", () => {
    const p = playbookPreamble(playbookSession);
    assert.ok(p);
    assert.match(p, /playbook session: "Fine-tune from a dataset version"\. Estimate 0\.24 GPU-hours \(0\.12–0\.36, basis mixed\)/);
    assert.match(p, /1\. Mix the dataset with replay \[mixes\.new\] — done \(mix_1\)/);
    assert.match(p, /2\. Calibrate \[runs\.calibrate; spends GPU time: dry run first\]$/m);
    assert.match(p, /3\. Wait for the run \[jobs\.wait; ticks when the job has ended\]/);
    assert.match(p, /4\. Eval matrix \[evals\.new\] — skipped/);
    assert.match(p, /dryRun=true/);
  });

  test("other sessions get no preamble", () => {
    assert.equal(playbookPreamble({ ...base, kind: "interactive" }), undefined);
    assert.equal(playbookPreamble({ ...base, kind: "playbook" }), undefined, "a playbook session without its playbook");
  });

  test("waits are not runaways, whatever the agent calls them", () => {
    for (const t of ["jobs.wait", "jobs_wait", "mcp__cadence__jobs_wait", "cadence_jobs_wait"]) assert.ok(isWaitTool(t), t);
    for (const t of ["mixes.get", "jobs_waitlist", "await"]) assert.ok(!isWaitTool(t), t);
  });
});
