import type { DatasetVersion } from "@/api/gen/types.gen";

// The setup checklist (docs/spec/11-ui-panels.md "Getting started"): each step's state comes from data that already
// exists, never from a separate "onboarding" record. Steps whose block has not shipped show the phase they arrive in.

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
};

export type SetupFacts = {
  signedIn: boolean;
  projects: number;
  datasets: Pick<DatasetVersion, "state" | "actor">[];
};

/** Bundled fixture versions are registered by Cadence itself; the first dataset a person or agent froze counts. */
export const BUNDLED_ACTOR = "cadence";

export function setupSteps(f: SetupFacts): SetupStep[] {
  const frozen = f.datasets.some((d) => d.state === "frozen" && d.actor.id !== BUNDLED_ACTOR);
  return [
    { id: "admin", title: "Set up the admin account", detail: "First start: the admin password, then optional two-factor sign-in (Settings → Security).", state: f.signedIn ? "done" : "todo", command: "auth.setup" },
    { id: "mount", title: "Attach the call recordings", detail: "A mount with the audio the project adapts to; the wizard lets you skip it until then.", state: "later", command: "mounts.new", phase: 4 },
    { id: "project", title: "Create a project", detail: "Three fields — name, language, recordings — everything else from defaults.", state: f.projects > 0 ? "done" : "todo", command: "projects.new" },
    { id: "dataset", title: "Freeze the first dataset version", detail: "An immutable, fingerprinted selection the first mix trains on.", state: frozen ? "done" : "later", command: "datasets.freeze", phase: frozen ? undefined : 4 },
    { id: "run", title: "Finish the first training run", detail: "A dry run shows the estimate first; nothing spends GPU time unseen.", state: "later", command: "runs.new", phase: 2 },
    { id: "gate", title: "Pass the first gate", detail: "The eval matrix against the project's golden sets; this checklist retires when it passes.", state: "later", command: "evals.gate", phase: 3 },
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
