// Playbooks in the UI (docs/spec/03-pipelines-defaults.md "Playbooks", R16): the start form is built from the
// playbook's inputs (the contract's PlaybookInput), the estimate is shown before the session starts, and a playbook
// session's plan is the server's (AgentSession.playbook), ticked from its commands.

import type { AgentPlaybook, Playbook, PlaybookEstimate, PlaybookInput, PlaybookPlanItem } from "@/api/gen/types.gen";

/** The form's value of an input: text as the person types it. */
export type PlaybookFormValues = Record<string, string>;

/** Inputs the person fills in: those without a project fact (a registry input from the project is shown, not asked). */
export function askedInputs(p: Playbook): PlaybookInput[] {
  return p.inputs.filter((i) => !i.from);
}

function asText(v: unknown): string {
  if (v === undefined || v === null) return "";
  if (Array.isArray(v)) return v.map(asText).join(", ");
  return String(v);
}

/** The start values of the form: every asked input's default, as text ("" for a required one). */
export function initialValues(p: Playbook): PlaybookFormValues {
  const out: PlaybookFormValues = {};
  for (const i of askedInputs(p)) out[i.name] = asText(i.default);
  return out;
}

/** A field's problem, or undefined: required and empty, not a number, outside the safe range. */
export function inputProblem(i: PlaybookInput, raw: string): string | undefined {
  const v = raw.trim();
  if (!v) return i.required ? "Required" : undefined;
  if (i.type === "integer" || i.type === "number") {
    const n = Number(v);
    if (!Number.isFinite(n)) return "Not a number";
    if (i.type === "integer" && !Number.isInteger(n)) return "A whole number";
    if (i.min !== undefined && n < i.min) return `At least ${i.min}`;
    if (i.max !== undefined && n > i.max) return `At most ${i.max}`;
  }
  return undefined;
}

/** The inputs of playbooks.run from the form: typed values, lists split on commas and new lines, defaults omitted. */
export function runInputs(p: Playbook, values: PlaybookFormValues): Record<string, unknown> {
  const out: Record<string, unknown> = {};
  for (const i of askedInputs(p)) {
    const raw = (values[i.name] ?? "").trim();
    if (!raw || raw === asText(i.default)) continue;
    switch (i.type) {
      case "integer":
      case "number":
        out[i.name] = Number(raw);
        break;
      case "boolean":
        out[i.name] = raw === "true";
        break;
      case "dataset_version":
      case "base_model": {
        const list = raw
          .split(/[\n,]/)
          .map((x) => x.trim())
          .filter(Boolean);
        out[i.name] = i.multiple ? list : list[0];
        break;
      }
      default:
        out[i.name] = raw;
    }
  }
  return out;
}

const fmt = (n: number) => (n >= 10 ? n.toFixed(1) : n.toFixed(2));

/** One line for an estimate: "0.93 GPU-hours (0.47–1.40, ±50%, table), about 36 min". */
export function estimateLine(e: PlaybookEstimate): string {
  const g = e.gpuHours;
  if (e.basis === "none" && g.value === 0) return "No GPU time";
  const parts = [`${fmt(g.value)} GPU-hours (${fmt(g.low)}–${fmt(g.high)}`];
  if (e.plusMinus > 0) parts.push(`, ±${Math.round(e.plusMinus * 100)}%`);
  parts.push(`, ${e.basis})`);
  if (e.durationSeconds.value > 0) parts.push(`, about ${Math.max(1, Math.round(e.durationSeconds.value / 60))} min`);
  return parts.join("");
}

/** The daily budget note of an estimate, or undefined. */
export function budgetNote(e: PlaybookEstimate): string | undefined {
  if (!e.budget) return undefined;
  const b = e.budget.gpuHoursPerProjectPerDay;
  return e.budget.withinDailyBudget ? `within the ${b} GPU-hour daily budget` : `over the ${b} GPU-hour daily budget: spending steps will ask a person`;
}

/** How far a plan is: done of the steps that count (skipped ones do not), and the item running now. */
export function planProgress(plan: readonly PlaybookPlanItem[]): {
  done: number;
  total: number;
  current?: PlaybookPlanItem;
  failed: boolean;
} {
  const counted = plan.filter((p) => p.state !== "skipped");
  return {
    done: counted.filter((p) => p.state === "done").length,
    total: counted.length,
    current: plan.find((p) => p.state === "running" || p.state === "pending"),
    failed: plan.some((p) => p.state === "failed"),
  };
}

/** The state line of a session's playbook. */
export function playbookStateLabel(pb: AgentPlaybook): string {
  const { done, total } = planProgress(pb.plan);
  switch (pb.state) {
    case "done":
      return `complete · ${done}/${total}`;
    case "stopped":
      return `stopped${pb.stop ? ` (${pb.stop.on} ${pb.stop.when})` : ""} · ${done}/${total}`;
    default:
      return `${done}/${total} done`;
  }
}
