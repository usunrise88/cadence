import type { GateConfig, GateDeparture, Gates } from "@/api/gen/types.gen";

// The Project home's gate (gates.get): the effective gate as rows people read, and what departs from defaults.yaml.

const pp = (v: number) => `${Math.round(v * 1000) / 10} pp`;

function show(v: unknown): string {
  if (v === undefined || v === null || v === "") return "—";
  if (Array.isArray(v)) return v.length ? v.map(show).join(", ") : "none";
  if (typeof v === "boolean") return v ? "yes" : "no";
  if (typeof v === "object") return JSON.stringify(v);
  return String(v);
}

/** The effective gate as label/value rows. */
export function gateRows(c: GateConfig): [string, string][] {
  // Unnamed sets split by language (evals.Target / Replay): the project's languages are targets, the others replay.
  const named = !!(c.target?.goldenSets?.length || c.replay?.goldenSets?.length);
  const target = `${c.target?.goldenSets?.length ? c.target.goldenSets.join(", ") : "golden sets in the project's languages"} · ${c.target?.rule ?? "beat-baseline"}`;
  const replaySets = c.replay?.goldenSets?.length ? c.replay.goldenSets.join(", ") : named ? "" : "golden sets in other languages";
  const replay = replaySets ? `${replaySets} · at most +${pp(c.replay?.maxRegression ?? 0)}` : "none";
  const s = c.significance;
  return [
    ["Primary profile", c.primaryProfile || "—"],
    ["Target", target],
    ["Replay (no regression)", replay],
    ["Deletions and insertions", c.deletionsInsertions ? "checked" : "not checked"],
    ["Significance", s ? `${s.samples ?? "—"} resamples · ${s.level !== undefined ? `${Math.round(s.level * 100)} %` : "—"} interval · seed ${s.seed ?? "—"}` : "—"],
  ];
}

/** One departure from defaults.yaml as a line. */
export function departureLine(d: GateDeparture): string {
  return `${d.param}: ${show(d.value)} (default ${show(d.default)})`;
}

/** The If-Match gates.edit expects: the commit that last changed gates.yaml, or "defaults" when there is no file. */
export function gateETag(g: Pick<Gates, "exists" | "commit">): string {
  return g.exists && g.commit ? g.commit : "defaults";
}

type EvalRow = { gate?: { verdict: "passed" | "failed" }; status: string };

/** The Evaluation block's line on the Project home: adopted golden sets, evals (newest first) and the newest verdict. */
export function evaluationSummary(goldenSets: number, evals: EvalRow[]): { count: number; text: string; verdict?: "passed" | "failed" } {
  const verdict = evals.find((e) => e.gate)?.gate?.verdict;
  const running = evals.filter((e) => e.status === "queued" || e.status === "running").length;
  const parts = [`${goldenSets} golden set${goldenSets === 1 ? "" : "s"} adopted`, `${evals.length} eval${evals.length === 1 ? "" : "s"}`];
  if (running) parts.push(`${running} running`);
  return { count: evals.length, text: parts.join(" · "), verdict };
}

/** Where the gate comes from, in words. */
export function gateSource(g: Pick<Gates, "exists" | "commit" | "path">): string {
  return g.exists ? `${g.path} at ${g.commit ? g.commit.slice(0, 7) : "main"}` : `No ${g.path}: the defaults apply`;
}
