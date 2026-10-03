import type { DatasetVersion, Eval, Run } from "@/api/gen/types.gen";

// The setup checklist (docs/spec/11-ui-panels.md "Getting started"): each step's state comes from data that already
// exists, never from a separate "onboarding" record. Steps whose block has not shipped show the phase they arrive in.
// The run and gate steps read the current project's runs and evals; the checklist retires once a gate passed.

export type StepState = "done" | "todo" | "later";

export type SetupStep = {
  id: "admin" | "mount" | "project" | "dataset" | "run" | "gate";
  title: string;
  detail: string;
  state: StepState;
  /** The command that does the step: `<entity>.<verb>` (MCP tool and palette entry) or a screen. */
  command: string;
  /** Phase the step's block arrives in, while it is not available yet. */
  phase?: number;
  /** What "Run the step" passes the command (the gate step: the newest finished eval, as a header would). */
  args?: { entity: { id: string; rev: number; name: string; state: string } };
  /** Why the step's command cannot run yet, shown instead of the button. */
  blocked?: string;
};

export type SetupFacts = {
  signedIn: boolean;
  projects: number;
  datasets: Pick<DatasetVersion, "state" | "actor">[];
  /** The current project's runs and evals (newest first); undefined without a project. */
  runs?: Pick<Run, "status">[];
  evals?: Pick<Eval, "id" | "rev" | "status" | "gate">[];
};

/** The first gate passed: the checklist has done its job (spec 11: shown until the first gate passes). */
export function retired(f: Pick<SetupFacts, "evals">): boolean {
  return (f.evals ?? []).some((e) => e.gate?.verdict === "passed");
}

function runStep(f: SetupFacts): Pick<SetupStep, "state" | "blocked"> {
  if ((f.runs ?? []).some((r) => r.status === "done")) return { state: "done" };
  if (!f.runs) return { state: "todo", blocked: "Open a project first" };
  return { state: "todo" };
}

function gateStep(f: SetupFacts): Pick<SetupStep, "state" | "args" | "blocked"> {
  if (retired(f)) return { state: "done" };
  if (!f.evals) return { state: "todo", blocked: "Open a project first" };
  // The newest finished eval that has not passed: its gate is the step (a failed verdict may pass after gates.yaml
  // changes, or a newer eval comes along).
  const e = f.evals.find((x) => x.status === "done");
  if (!e) return { state: "todo", blocked: "Evaluate a checkpoint first (Checkpoints → Evaluate); the gate reads a finished eval" };
  // As from the Eval report's header: the command reports the verdict (or why it could not run) as a notice.
  return { state: "todo", args: { entity: { id: e.id, rev: e.rev, name: e.id, state: e.status } } };
}

/** Bundled fixture versions are registered by Cadence itself; the first dataset a person or agent froze counts. */
export const BUNDLED_ACTOR = "cadence";

export function setupSteps(f: SetupFacts): SetupStep[] {
  const frozen = f.datasets.some((d) => d.state === "frozen" && d.actor.id !== BUNDLED_ACTOR);
  return [
    { id: "admin", title: "Set up the admin account", detail: "First start: the admin password, then optional two-factor sign-in (Settings → Security).", state: f.signedIn ? "done" : "todo", command: "auth.setup" },
    { id: "mount", title: "Attach the call recordings", detail: "A mount with the audio the project adapts to; the wizard lets you skip it until then.", state: "later", command: "mounts.new", phase: 4 },
    { id: "project", title: "Create a project", detail: "Three fields — name, language, recordings — everything else from defaults.", state: f.projects > 0 ? "done" : "todo", command: "projects.new" },
    { id: "dataset", title: "Freeze the first dataset version", detail: "An immutable, fingerprinted selection the first mix trains on.", state: frozen ? "done" : "later", command: "datasets.freeze", phase: frozen ? undefined : 4 },
    { id: "run", title: "Finish the first training run", detail: "The playbook “Fine-tune from a dataset version” runs it: the estimate first, a dry run before every spending step.", command: "playbooks.run", ...runStep(f) },
    { id: "gate", title: "Pass the first gate", detail: "The eval matrix against the project's golden sets; this checklist retires when it passes.", command: "evals.gate", ...gateStep(f) },
  ];
}

export const DISMISS_KEY = "cadence.gettingStarted.dismissed";

export function isDismissed(): boolean {
  try {
    return localStorage.getItem(DISMISS_KEY) === "1";
  } catch {
    return false;
  }
}

export function setDismissed(v: boolean): void {
  try {
    if (v) localStorage.setItem(DISMISS_KEY, "1");
    else localStorage.removeItem(DISMISS_KEY);
  } catch {
    /* storage unavailable: the panel just shows again next time */
  }
}
