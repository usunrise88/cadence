import { create } from "zustand";

// The active job and the active pipeline run (docs/spec/11-ui-panels.md: Logs shows "the active job", Pipeline run
// "any pipeline run"). Jobs and pipeline runs are not documents, so the selection bus does not carry them: the panel
// that shows one (a Queue & GPU row, a Pipeline run step, later the Run document) focuses it here, and Logs and
// Pipeline run follow unless the person picked one themselves.

export type FocusedJob = { id: string; label?: string };

type FocusState = {
  job: FocusedJob | null;
  pipelineRun: string | null;
  focusJob(job: FocusedJob | null): void;
  focusPipelineRun(id: string | null): void;
};

export const useTrainingFocus = create<FocusState>((set) => ({
  job: null,
  pipelineRun: null,
  focusJob: (job) => set((s) => (s.job?.id === job?.id && s.job?.label === job?.label ? s : { job })),
  focusPipelineRun: (id) => set((s) => (s.pipelineRun === id ? s : { pipelineRun: id })),
}));

/** The job Logs follows. */
export function useFocusedJob(): FocusedJob | null {
  return useTrainingFocus((s) => s.job);
}

/** The pipeline run the Pipeline run panel follows. */
export function useFocusedPipelineRun(): string | null {
  return useTrainingFocus((s) => s.pipelineRun);
}

export function focusJob(job: FocusedJob | null): void {
  useTrainingFocus.getState().focusJob(job);
}

export function focusPipelineRun(id: string | null): void {
  useTrainingFocus.getState().focusPipelineRun(id);
}
