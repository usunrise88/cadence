// The report: pass/fail per run, the summary, the file name and the table people read.

import assert from "node:assert/strict";
import { describe, test } from "node:test";
import { buildReport, EMPTY_METRICS, formatTable, reportName, runResult } from "./report.ts";
import { liveDrivers } from "./run.ts";
import type { RunResult } from "./types.ts";

const metrics = { ...EMPTY_METRICS, turns: 1, inputTokens: 12_345, outputTokens: 150, wallMs: 2800, toolCalls: 7, mcpCalls: 6 };

function run(over: Partial<Omit<RunResult, "pass">> = {}): RunResult {
  return runResult({
    eval: "gate-mix-temperature",
    driver: "claude",
    model: "scripted",
    mode: "offline",
    graders: [
      { id: "draft:he-smoke.temperature", pass: true, detail: "rev 5" },
      { id: "within-budget", pass: true, detail: "ok" },
    ],
    metrics,
    ...over,
  });
}

describe("runResult", () => {
  test("passes only when every grader passed and nothing went wrong", () => {
    assert.equal(run().pass, true);
    assert.equal(run({ graders: [{ id: "a", pass: true, detail: "" }, { id: "b", pass: false, detail: "no" }] }).pass, false);
    assert.equal(run({ error: "bootstrap failed" }).pass, false);
    assert.equal(run({ graders: [] }).pass, false, "a run nobody graded did not pass");
  });
});

describe("buildReport", () => {
  test("summarises the runs", () => {
    const r = buildReport({ startedAt: "a", finishedAt: "b", mode: "offline", controlPlane: "http://x", runs: [run(), run({ driver: "opencode", error: "x" })] });
    assert.deepEqual(r.summary, { runs: 2, passed: 1, failed: 1 });
    assert.deepEqual(JSON.parse(JSON.stringify(r)), r, "the report is plain JSON");
  });
});

describe("reportName", () => {
  test("sorts by time and has no colons", () => {
    assert.equal(reportName(new Date("2026-09-30T12:04:05.123Z")), "2026-09-30T12-04-05Z.json");
  });
});

describe("formatTable", () => {
  test("one aligned row per eval × driver, the failures with their detail, the summary", () => {
    const failing = run({ driver: "opencode", model: "minimax/MiniMax-M2", mode: "live", graders: [{ id: "through-mcp", pass: false, detail: "mixes.edit: no tool call (not MCP)" }] });
    const broken = run({ eval: "gated-baseline", graders: [], metrics: EMPTY_METRICS, error: "no Claude login" });
    const report = buildReport({ startedAt: "a", finishedAt: "b", mode: "live", controlPlane: "http://x", runs: [run(), failing, broken] });
    const lines = formatTable(report).split("\n");
    assert.match(lines[0]!, /^eval\s+driver\s+model\s+result\s+graders\s+turns\s+tokens in\/out\s+tools \(mcp\)\s+wall$/);
    assert.match(lines[2]!, /^gate-mix-temperature\s+claude\s+scripted\s+PASS\s+2\/2\s+1\s+12k\/150\s+7 \(6\)\s+2\.8 s$/);
    assert.match(lines[3]!, /opencode\s+minimax\/MiniMax-M2\s+FAIL\s+0\/1/);
    // Columns line up: every row starts its driver column where the header does.
    const col = lines[0]!.indexOf("driver");
    for (const l of lines.slice(2, 5)) assert.match(l.slice(col), /^(claude|opencode)/);
    const text = lines.join("\n");
    assert.match(text, /Failed:\n {2}gate-mix-temperature × opencode: through-mcp — mixes\.edit: no tool call \(not MCP\)/);
    assert.match(text, /gated-baseline × claude: error — no Claude login/);
    assert.match(lines.at(-1)!, /^1\/3 passed \(live\)$/);
  });
});

describe("liveDrivers", () => {
  test("CADENCE_LIVE_AGENTS: unset or 0 is offline, 1 or all both drivers, or a list", () => {
    assert.deepEqual(liveDrivers(undefined), []);
    assert.deepEqual(liveDrivers("0"), []);
    assert.deepEqual(liveDrivers("1"), ["claude", "opencode"]);
    assert.deepEqual(liveDrivers("all"), ["claude", "opencode"]);
    assert.deepEqual(liveDrivers("opencode"), ["opencode"]);
    assert.deepEqual(liveDrivers("claude, opencode, gemini"), ["claude", "opencode"]);
  });
});
