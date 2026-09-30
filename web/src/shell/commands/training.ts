import { EditPencil, Eye, Pause, Play, Plus, Restart, Xmark } from "iconoir-react";
import { commandHeaders, commandHeadersAt } from "@/api/client";
import { jobsCancel, jobsEdit, jobsGet, jobsPause, jobsResume, mixesPreview, pipelineRunsCancel, pipelineRunsRetry, recipesEdit, recipesNew } from "@/api/gen/sdk.gen";
import type { Job, MixNew, MixPreview, PipelineRun, PipelineRunRetry, PipelineRunSummary, Recipe, RecipeEdit, RecipeNew } from "@/api/gen/types.gen";
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
/** runs.new from a mix (the Mix document's launch button). The command itself arrives with the Run panel (phase 2, Part B). */
export type RunFromMixArgs = { mix: { id: string; rev: number } };

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
  "runs.new": { args: RunFromMixArgs; result: unknown };
  "recipes.new": { args: RecipeNewArgs; result: Recipe };
  "recipes.edit": { args: RecipeEditArgs; result: Recipe };
};

function need<T>(args: unknown, what: string): T {
  if (!args) throw new Error(`${what}: run it from its panel`);
  return args as T;
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
  ];
  for (const c of list) commands.register(c);
}
