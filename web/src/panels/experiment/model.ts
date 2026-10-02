import type { Experiment, ExperimentRun, SweepNew, SweepParameter } from "@/api/gen/types.gen";
import type { ParallelAxis, ParallelSpec, ScatterSpec } from "@/shell/charts";

// Pure helpers of the Experiment document: chart specs from the comparison rows (the API's numbers, R53), the sweep
// form's values, labels.

export const VAL_WER = "val WER";

/** run_0192ab34cdef… → 0192ab34 (enough to tell runs of one experiment apart). */
export function shortRun(id: string): string {
  return id.replace(/^run_/, "").slice(0, 8);
}

/** A parameter value as text: numbers compact, objects as JSON. */
export function showValue(v: unknown): string {
  if (v === undefined || v === null) return "—";
  if (typeof v === "number") return Number.isInteger(v) ? String(v) : String(Number(v.toPrecision(4)));
  if (typeof v === "object") return JSON.stringify(v);
  return String(v);
}

/** Parameters every run (that has a value) gives a finite number for — the scatter's x choices. */
export function numericParams(e: Experiment, rows: ExperimentRun[]): string[] {
  return (e.parameters ?? [])
    .map((p) => p.name)
    .filter((name) => {
      const vals = rows.map((r) => r.values[name]).filter((v) => v !== undefined && v !== null);
      return vals.length > 0 && vals.every((v) => typeof v === "number" && Number.isFinite(v));
    });
}

/** Sweep labels in order of start ("sweep 1" is the oldest; the API lists sweeps newest first). */
export function sweepLabels(e: Experiment): Map<string, { label: string; slot: number }> {
  const out = new Map<string, { label: string; slot: number }>();
  [...e.sweeps].reverse().forEach((s, i) => out.set(s.id, { label: `sweep ${i + 1}`, slot: i + 1 }));
  return out;
}

/** Parameter against the best validation WER, one series per sweep (manual runs on their own). */
export function scatterSpec(e: Experiment, rows: ExperimentRun[], param: string): ScatterSpec {
  const labels = sweepLabels(e);
  const groups = new Map<string, ScatterSpec["series"][number]>();
  for (const r of rows) {
    const x = r.values[param];
    if (typeof x !== "number" || r.bestValWer === undefined) continue;
    const key = r.sweepId ?? "manual";
    const meta = r.sweepId ? labels.get(r.sweepId) : undefined;
    let g = groups.get(key);
    if (!g) {
      g = { id: key, label: meta?.label ?? "manual runs", slot: meta?.slot ?? 0, points: [] };
      groups.set(key, g);
    }
    g.points.push({ x, y: r.bestValWer, label: `${shortRun(r.runId)}${r.best ? " ◆ best" : ""}` });
  }
  return { kind: "scatter", title: `${param} against validation WER`, xLabel: param, yLabel: VAL_WER, series: [...groups.values()] };
}

/** Parallel coordinates over the swept parameters and the best validation WER; the best run's line highlighted. */
export function parallelSpec(e: Experiment, rows: ExperimentRun[]): ParallelSpec | undefined {
  const swept = (e.parameters ?? []).filter((p) => p.swept).map((p) => p.name);
  if (swept.length === 0 || rows.length === 0) return undefined;
  const axes: ParallelAxis[] = swept.map((name) => {
    const vals = rows.map((r) => r.values[name]).filter((v) => v !== undefined && v !== null);
    if (vals.length > 0 && vals.every((v) => typeof v === "number")) {
      const nums = vals as number[];
      const lo = Math.min(...nums);
      const hi = Math.max(...nums);
      return { id: name, label: name, type: lo > 0 && hi / lo >= 10 ? "log" : "value" };
    }
    return { id: name, label: name, type: "category", categories: [...new Set(vals.map(showValue))] };
  });
  axes.push({ id: "valWer", label: VAL_WER, type: "value" });
  const labels = sweepLabels(e);
  return {
    kind: "parallel",
    title: "Swept parameters and validation WER",
    axes,
    lines: rows.map((r, i) => ({
      id: r.runId,
      label: `${shortRun(r.runId)}${r.best ? " ◆ best" : ""}`,
      slot: r.sweepId ? (labels.get(r.sweepId)?.slot ?? i) : 0,
      highlight: r.best,
      values: [
        ...axes.slice(0, -1).map((a) => {
          const v = r.values[a.id];
          if (v === undefined || v === null) return null;
          return a.type === "category" ? showValue(v) : (v as number);
        }),
        r.bestValWer ?? null,
      ],
    })),
  };
}

/**
 * The values of a sweep parameter as typed in the form: a JSON list body first (`0.0001, 0.0003` or
 * `{"profile": "clean"}, {"profile": "telephony"}`), else comma-separated words, numbers where they parse.
 */
export function parseValues(text: string): unknown[] {
  const t = text.trim();
  if (!t) return [];
  try {
    const v: unknown = JSON.parse(`[${t}]`);
    if (Array.isArray(v)) return v;
  } catch {
    // not JSON: plain words
  }
  return t
    .split(",")
    .map((s) => s.trim())
    .filter(Boolean)
    .map((s) => (s !== "" && Number.isFinite(Number(s)) ? Number(s) : s));
}

/** One parameter row of the sweep form. */
export type ParamRow = { name: string; values: string; min: string; max: string; scale: "linear" | "log"; integer: boolean };

export const emptyRow = (): ParamRow => ({ name: "", values: "", min: "", max: "", scale: "linear", integer: false });

export type SweepFormState = { mode: "grid" | "random"; rows: ParamRow[]; runs: string; cap: string; seed: string; steps: string };

const posInt = (s: string) => (s.trim() !== "" && Number.isInteger(Number(s)) && Number(s) > 0 ? Number(s) : undefined);

/** The sweeps.run body the form holds, or the first problem with it. */
export function sweepBody(f: SweepFormState): { body?: SweepNew; error?: string } {
  const parameters: SweepParameter[] = [];
  for (const [i, r] of f.rows.entries()) {
    const name = r.name.trim();
    if (!name) return { error: `Parameter ${i + 1} needs a name` };
    const values = parseValues(r.values);
    const ranged = f.mode === "random" && values.length === 0;
    if (!ranged && values.length === 0) return { error: `${name}: list the values to try` };
    if (ranged) {
      const min = Number(r.min);
      const max = Number(r.max);
      if (r.min.trim() === "" || r.max.trim() === "" || !Number.isFinite(min) || !Number.isFinite(max)) return { error: `${name}: give values, or a min and a max` };
      if (min > max) return { error: `${name}: min is above max` };
      if (r.scale === "log" && min <= 0) return { error: `${name}: a log scale needs min above zero` };
      parameters.push({ name, min, max, ...(r.scale === "log" ? { scale: "log" } : {}), ...(r.integer ? { integer: true } : {}) });
    } else {
      parameters.push({ name, values });
    }
  }
  if (parameters.length === 0) return { error: "Add a parameter to sweep" };
  const body: SweepNew = { mode: f.mode, parameters };
  const runs = posInt(f.runs);
  if (runs) body.runs = runs;
  const cap = Number(f.cap);
  if (f.cap.trim() !== "") {
    if (!(cap > 0)) return { error: "The GPU-hour cap must be above zero" };
    body.gpuHourCap = cap;
  }
  if (f.seed.trim() !== "") {
    const seed = Number(f.seed);
    if (!Number.isInteger(seed) || seed < 0) return { error: "The seed is a whole number" };
    body.seed = seed;
  }
  const steps = posInt(f.steps);
  if (steps) body.steps = steps;
  return { body };
}
