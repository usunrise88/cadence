import { Play, Plus } from "iconoir-react";
import { commandHeaders } from "@/api/client";
import { experimentsNew, sweepsRun } from "@/api/gen/sdk.gen";
import type {
  ApprovalAccepted,
  Experiment,
  ExperimentNew,
  Sweep,
  SweepNew,
  SweepPlan,
} from "@/api/gen/types.gen";
import { docRef, type EntityData } from "@/shell/entity/manifest";
import { useEditRequests } from "@/shell/entity/edits";
import { openDocument, openPanelById } from "@/shell/panel/actions";
import { commands } from "@/shell/registries";
import type { Command } from "./registry";

// Commands behind the Experiment document (phase 3 · stream X; docs/spec/11-ui-panels.md "Experiment"): each is
// exactly one API operation. Evaluate best and Register best run evals.new and models.register from
// ./evaluation, which asks the Experiment document (REGISTER_REQUEST) when the header runs it without a body. Without a body, experiments.new opens the Experiment panel's form and sweeps.run asks the
// open Experiment document for its sweep form (as runs.stage does); register best and evaluate best come from the
// document, which knows the best run's checkpoint.

/** experiments.new: without a body the Experiment panel shows its form. */
export type ExperimentNewArgs = { project: string; body: ExperimentNew; dryRun?: boolean };
/** sweeps.run on an experiment (its revision is the If-Match); from the header `entity` stands in for it. */
export type SweepRunArgs = { experiment?: Pick<Experiment, "id" | "rev">; entity?: EntityData; body?: SweepNew; dryRun?: boolean };

export type ExperimentCommands = {
  "experiments.new": { args: ExperimentNewArgs | undefined; result: Experiment | undefined };
  "sweeps.run": { args: SweepRunArgs; result: SweepPlan | Sweep | ApprovalAccepted | undefined };
};

/** Requests that only the open Experiment document can complete: its sweep form, its register-best confirmation. */
export const REGISTER_REQUEST = "register:";

function experimentOf(a: SweepRunArgs): Pick<Experiment, "id" | "rev"> {
  const e = a.experiment ?? (a.entity ? { id: a.entity.id, rev: a.entity.rev ?? 0 } : undefined);
  if (!e) throw new Error("Run sweep: open an experiment first");
  return e;
}

const dry = (d?: boolean) => (d ? { dryRun: true } : undefined);

export function registerExperimentCommands(): void {
  const list: Command[] = [
    {
      id: "experiments.new",
      operation: "experiments.new",
      title: "New experiment…",
      group: "Project",
      icon: Plus,
      enabled: (ctx) => (ctx.project ? true : "Open a project first"),
      run: async (_ctx, args) => {
        const a = args as ExperimentNewArgs | undefined;
        if (!a?.body) {
          openPanelById("experiment"); // its empty state is the New experiment form
          return undefined;
        }
        const { data } = await experimentsNew({ path: { p: a.project }, body: a.body, query: dry(a.dryRun), headers: commandHeaders(), throwOnError: true });
        return data;
      },
    },
    {
      id: "sweeps.run",
      operation: "sweeps.run",
      title: "Run sweep",
      group: "Edit",
      icon: Play,
      hidden: true,
      run: async (_ctx, args) => {
        const a = (args ?? {}) as SweepRunArgs;
        const e = experimentOf(a);
        if (!a.body) {
          // From the header or the palette: the Experiment document's sweep form (dry run first).
          const doc = docRef("experiment", e.id);
          openDocument(doc);
          useEditRequests.getState().request(doc);
          return undefined;
        }
        const { data } = await sweepsRun({ path: { id: e.id }, body: a.body, query: dry(a.dryRun), headers: commandHeaders(e.rev), throwOnError: true });
        return data;
      },
    },
  ];
  for (const c of list) commands.register(c);
}
