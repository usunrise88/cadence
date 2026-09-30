// Results: one RunResult per eval × driver, the JSON report (evals/results/<timestamp>.json) and a table for people.

import type { EvalReport, GraderResult, RunMetrics, RunResult } from "./types.ts";

export const EMPTY_METRICS: RunMetrics = {
  turns: 0,
  inputTokens: 0,
  outputTokens: 0,
  cachedReadTokens: 0,
  wallMs: 0,
  toolCalls: 0,
  mcpCalls: 0,
  permissionRequests: 0,
};

export function runResult(base: Omit<RunResult, "pass">): RunResult {
  const pass = !base.error && base.graders.length > 0 && base.graders.every((g) => g.pass);
  return { ...base, pass };
}

export function buildReport(r: Omit<EvalReport, "summary">): EvalReport {
  const passed = r.runs.filter((x) => x.pass).length;
  return { ...r, summary: { runs: r.runs.length, passed, failed: r.runs.length - passed } };
}

/** A file name for the report that sorts by time: 2026-09-30T12-04-05Z.json */
export function reportName(at: Date): string {
  return `${at.toISOString().replace(/\.\d+Z$/, "Z").replace(/:/g, "-")}.json`;
}

function pad(s: string, n: number): string {
  return s.length >= n ? s : s + " ".repeat(n - s.length);
}

function tokens(m: RunMetrics): string {
  const k = (x: number) => (x >= 10_000 ? `${Math.round(x / 1000)}k` : String(x));
  return `${k(m.inputTokens)}/${k(m.outputTokens)}`;
}

/** The table: one row per eval × driver, then every failed grader with its detail. */
export function formatTable(report: EvalReport): string {
  const head = ["eval", "driver", "model", "result", "graders", "turns", "tokens in/out", "tools (mcp)", "wall"];
  const rows = report.runs.map((r) => [
    r.eval,
    r.driver,
    r.model,
    r.pass ? "PASS" : "FAIL",
    `${r.graders.filter((g) => g.pass).length}/${r.graders.length}`,
    String(r.metrics.turns),
    tokens(r.metrics),
    `${r.metrics.toolCalls} (${r.metrics.mcpCalls})`,
    `${(r.metrics.wallMs / 1000).toFixed(1)} s`,
  ]);
  const widths = head.map((h, i) => Math.max(h.length, ...rows.map((row) => row[i]!.length)));
  const line = (cells: string[]) => cells.map((c, i) => pad(c, widths[i]!)).join("  ").trimEnd();
  const out = [line(head), line(widths.map((w) => "-".repeat(w))), ...rows.map(line)];
  const failures = report.runs.flatMap((r) => {
    const failed: Array<{ id: string } & GraderResult> = r.graders.filter((g) => !g.pass);
    const lines = failed.map((g) => `  ${r.eval} × ${r.driver}: ${g.id} — ${g.detail}`);
    if (r.error) lines.unshift(`  ${r.eval} × ${r.driver}: error — ${r.error}`);
    return lines;
  });
  if (failures.length) out.push("", "Failed:", ...failures);
  out.push("", `${report.summary.passed}/${report.summary.runs} passed (${report.mode})`);
  return out.join("\n");
}
