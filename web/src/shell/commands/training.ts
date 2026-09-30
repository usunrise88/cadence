import { Combine, EditPencil, Eye, Forward, Pause, Play, Plus, Restart, StatUp, Xmark } from "iconoir-react";
import { commandHeaders, commandHeadersAt } from "@/api/client";
import { jobsCancel, jobsEdit, jobsGet, jobsPause, jobsResume, mixesPreview, pipelineRunsCancel, pipelineRunsRetry, recipesEdit, recipesNew, runsNew, runsResume, runsStage, checkpointsAverage } from "@/api/gen/sdk.gen";
import type {
  ApprovalAccepted,
  CheckpointAverage,
  CheckpointAverageStarted,
  Run,
  RunEstimate,
  RunNew,
  RunStage,
} from "@/api/gen/types.gen";
import type { Job, MixNew, MixPreview, PipelineRun, PipelineRunRetry, PipelineRunSummary, Recipe, RecipeEdit, RecipeNew } from "@/api/gen/types.gen";
import { docRef, type EntityData } from "@/shell/entity/manifest";
import { useEditRequests } from "@/shell/entity/edits";
import { openDocument } from "@/shell/panel/actions";
import { notify, notifyError } from "@/shell/notifications/store";
import { commands } from "@/shell/registries";
import type { Command } from "./registry";

// Commands behind the phase-2 panels (Queue & GPU, Pipeline run, the full Mix, the Recipe forms): each is exactly
// one API operation, run by panels through runCommand with typed arguments. They need arguments a palette cannot supply
// (the job or run they act on), so they are hidden from it.

/**
 * A step job to act on. A queue entry has no revision: without `rev` the command reads the job first (jobs.get) and
 * sends that revision as If-Match; a change made in between answers 412 like any other conflict.
 */
export type JobArgs = { jobId: string; rev?: number };
export type JobEditArgs = JobArgs & { priority: number };
export type PipelineRunArgs = { run: Pick<PipelineRunSummary, "id" | "rev"> };
export type PipelineRunRetryArgs = PipelineRunArgs & { body?: PipelineRunRetry };
export type MixPreviewArgs = { project: string; body: MixNew };
/** runs.new: a dry run answers the estimate (RunEstimate), a real one the run, or an approval when it is gated. */
export type RunNewArgs = { project: string; body: RunNew; dryRun?: boolean };
/** A run to act on: its revision is the If-Match. From the document header, `entity` stands in for it. */
export type RunArgs = { run?: Pick<Run, "id" | "rev">; entity?: EntityData };
export type RunStageArgs = RunArgs & { body?: RunStage; dryRun?: boolean };
export type CheckpointAverageArgs = { runId: string; body: CheckpointAverage; dryRun?: boolean };

/** recipes.edit: `expect` is the commit that last changed the file (history[0].sha), sent as If-Match. */
export type RecipeEditArgs = { project: string; path: string; expect: string; body: RecipeEdit };
export type RecipeNewArgs = { project: string; body: RecipeNew };

export type TrainingCommands = {
  "jobs.edit": { args: JobEditArgs; result: Job };
  "jobs.pause": { args: JobArgs; result: Job };
  "jobs.resume": { args: JobArgs; result: Job };
  "jobs.cancel": { args: JobArgs; result: Job };
  "pipelineRuns.retry": { args: PipelineRunRetryArgs; result: PipelineRun };
  "pipelineRuns.cancel": { args: PipelineRunArgs; result: PipelineRun };
  "mixes.preview": { args: MixPreviewArgs; result: MixPreview };
  "runs.new": { args: RunNewArgs; result: RunEstimate | Run | ApprovalAccepted };
  "runs.resume": { args: RunArgs; result: Run | ApprovalAccepted | undefined };
  "runs.stage": { args: RunStageArgs; result: RunEstimate | Run | ApprovalAccepted | undefined };
  "checkpoints.average": { args: CheckpointAverageArgs; result: CheckpointAverageStarted | ApprovalAccepted };
  "recipes.new": { args: RecipeNewArgs; result: Recipe };
  "recipes.edit": { args: RecipeEditArgs; result: Recipe };
};

function need<T>(args: unknown, what: string): T {
  if (!args) throw new Error(`${what}: run it from its panel`);
  return args as T;
}

function runOf(a: RunArgs, what: string): Pick<Run, "id" | "rev"> {
  const r = a.run ?? (a.entity ? { id: a.entity.id, rev: a.entity.rev ?? 0 } : undefined);
  if (!r) throw new Error(`${what}: open a run first`);
  return r;
}

async function revOf(a: JobArgs): Promise<number> {
  if (a.rev !== undefined) return a.rev;
  const { data } = await jobsGet({ path: { id: a.jobId }, throwOnError: true });
  return data.rev;
}

export function registerTrainingCommands(): void {
  const list: Command[] = [
    {
      id: "jobs.edit",
      operation: "jobs.edit",
      title: "Change job priority",
      group: "Edit",
      icon: EditPencil,
      hidden: true,
      run: async (_ctx, args) => {
        const a = need<JobEditArgs>(args, "Change job priority");
        const { data } = await jobsEdit({ path: { id: a.jobId }, body: { priority: a.priority }, headers: commandHeaders(await revOf(a)), throwOnError: true });
        return data;
      },
    },
    {
      id: "jobs.pause",
      operation: "jobs.pause",
      title: "Pause job",
      group: "Edit",
      icon: Pause,
      hidden: true,
      run: async (_ctx, args) => {
        const a = need<JobArgs>(args, "Pause job");
        const { data } = await jobsPause({ path: { id: a.jobId }, headers: commandHeaders(await revOf(a)), throwOnError: true });
        return data;
      },
    },
    {
      id: "jobs.resume",
      operation: "jobs.resume",
      title: "Resume job",
      group: "Edit",
      icon: Play,
      hidden: true,
      run: async (_ctx, args) => {
        const a = need<JobArgs>(args, "Resume job");
        const { data } = await jobsResume({ path: { id: a.jobId }, headers: commandHeaders(await revOf(a)), throwOnError: true });
        return data;
      },
    },
    {
      id: "jobs.cancel",
      operation: "jobs.cancel",
      title: "Cancel job",
      group: "Edit",
      icon: Xmark,
      hidden: true,
      run: async (_ctx, args) => {
        const a = need<JobArgs>(args, "Cancel job");
        const { data } = await jobsCancel({ path: { id: a.jobId }, headers: commandHeaders(await revOf(a)), throwOnError: true });
        return data;
      },
    },
    {
      id: "pipelineRuns.retry",
      operation: "pipelineRuns.retry",
      title: "Retry pipeline step",
      group: "Edit",
      icon: Restart,
      hidden: true,
      run: async (_ctx, args) => {
        const a = need<PipelineRunRetryArgs>(args, "Retry pipeline step");
        const { data } = await pipelineRunsRetry({ path: { id: a.run.id }, body: a.body ?? {}, headers: commandHeaders(a.run.rev), throwOnError: true });
        return data;
      },
    },
    {
      id: "pipelineRuns.cancel",
      operation: "pipelineRuns.cancel",
      title: "Cancel pipeline run",
      group: "Edit",
      icon: Xmark,
      hidden: true,
      run: async (_ctx, args) => {
        const a = need<PipelineRunArgs>(args, "Cancel pipeline run");
        const { data } = await pipelineRunsCancel({ path: { id: a.run.id }, headers: commandHeaders(a.run.rev), throwOnError: true });
        return data;
      },
    },
    {
      id: "mixes.preview",
      operation: "mixes.preview",
      title: "Preview mix",
      group: "Project",
      icon: Eye,
      hidden: true,
      run: async (_ctx, args) => {
        const a = need<MixPreviewArgs>(args, "Preview mix");
        const { data } = await mixesPreview({ path: { p: a.project }, body: a.body, headers: commandHeaders(), throwOnError: true });
        return data;
      },
    },
    {
      id: "recipes.new",
      operation: "recipes.new",
      title: "New recipe file",
      group: "Project",
      icon: Plus,
      hidden: true,
      run: async (_ctx, args) => {
        const a = need<RecipeNewArgs>(args, "New recipe file");
        const { data } = await recipesNew({ path: { p: a.project }, body: a.body, headers: commandHeaders(), throwOnError: true });
        return data;
      },
    },
    {
      id: "recipes.edit",
      operation: "recipes.edit",
      title: "Commit recipe file",
      group: "Edit",
      icon: EditPencil,
      hidden: true,
      run: async (_ctx, args) => {
        const a = need<RecipeEditArgs>(args, "Commit recipe file");
        const { data } = await recipesEdit({ path: { p: a.project, path: a.path }, body: a.body, headers: commandHeadersAt(a.expect), throwOnError: true });
        return data;
      },
    },
    {
      id: "runs.new",
      operation: "runs.new",
      title: "New training run",
      group: "Project",
      icon: Play,
      hidden: true,
      run: async (_ctx, args) => {
        const a = need<RunNewArgs>(args, "New training run");
        const { data } = await runsNew({ path: { p: a.project }, body: a.body, query: a.dryRun ? { dryRun: true } : undefined, headers: commandHeaders(), throwOnError: true });
        return data;
      },
    },
    {
      id: "runs.resume",
      operation: "runs.resume",
      title: "Resume from checkpoint",
      group: "Edit",
      icon: Forward,
      hidden: true,
      run: async (_ctx, args) => {
        const a = (args ?? {}) as RunArgs;
        const r = runOf(a, "Resume from checkpoint");
        try {
          const { data } = await runsResume({ path: { id: r.id }, headers: commandHeaders(r.rev), throwOnError: true });
          if ("approvalId" in data) notify({ level: "info", title: "Resume waits for an approval", detail: data.approvalId });
          return data;
        } catch (err) {
          // From the document header nobody awaits the result: say why here (no training state yet, a moved run).
          if (a.entity) {
            notifyError("Resume from checkpoint failed", err);
            return undefined;
          }
          throw err;
        }
      },
    },
    {
      id: "runs.stage",
      operation: "runs.stage",
      title: "New stage from checkpoint",
      group: "Edit",
      icon: StatUp,
      hidden: true,
      run: async (_ctx, args) => {
        const a = (args ?? {}) as RunStageArgs;
        const r = runOf(a, "New stage from checkpoint");
        if (!a.body) {
          // From the header or the palette: the Run document's stage form asks for the explicit peak LR.
          const doc = docRef("run", r.id);
          openDocument(doc);
          useEditRequests.getState().request(doc);
          return undefined;
        }
        const { data } = await runsStage({ path: { id: r.id }, body: a.body, query: a.dryRun ? { dryRun: true } : undefined, headers: commandHeaders(r.rev), throwOnError: true });
        return data;
      },
    },
    {
      id: "checkpoints.average",
      operation: "checkpoints.average",
      title: "Average checkpoints",
      group: "Edit",
      icon: Combine,
      hidden: true,
      run: async (_ctx, args) => {
        const a = need<CheckpointAverageArgs>(args, "Average checkpoints");
        const { data } = await checkpointsAverage({ path: { id: a.runId }, body: a.body, query: a.dryRun ? { dryRun: true } : undefined, headers: commandHeaders(), throwOnError: true });
        return data;
      },
    },
  ];
  for (const c of list) commands.register(c);
}
