import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import {
  artifactsGetQueryKey,
  defaultsGetQueryKey,
  jobLogsListQueryKey,
  pipelineRunsGetQueryKey,
  pipelineRunsListQueryKey,
  stepKindsGetQueryKey,
} from "@/api/gen/@tanstack/react-query.gen";
import type { Defaults, PipelineRun, StepKindVersion } from "@/api/gen/types.gen";
import { TooltipProvider } from "@/components/ui/tooltip";
import { PanelContext } from "@/shell/panel/context";
import { useTrainingFocus } from "@/shell/training/focus";
import { PipelineRunPanel } from "./PipelineRunPanel";

const runCommand = vi.fn();
const openDocument = vi.fn();
vi.mock("@/shell/panel/commands", async (orig) => ({ ...(await orig<object>()), runCommand: (...a: unknown[]) => runCommand(...a) }));
vi.mock("@/shell/panel/actions", async (orig) => ({ ...(await orig<object>()), useProject: () => "demo", openDocument: (d: string) => openDocument(d) }));

const hash = `b3:${"cd".repeat(32)}`;
const run: PipelineRun = {
  id: "plr_1",
  projectId: "prj_1",
  pipeline: "train-stage",
  source: "repository",
  version: "abc",
  commit: "abcdef1234",
  state: "failed",
  rev: 3,
  actor: { kind: "user", id: "usr_admin", name: "admin" },
  createdAt: "2026-09-30T10:00:00Z",
  updatedAt: "2026-09-30T10:05:00Z",
  finishedAt: "2026-09-30T10:05:00Z",
  inputs: { text: { hash, type: "text", size: 12 } },
  steps: [
    {
      id: "pls_2",
      step: "train",
      position: 1,
      kind: "tally",
      kindVersion: "1",
      stepKindVersionId: "ver_tally",
      state: "failed",
      params: { steps: 500, precision: "bf16" },
      departures: [{ param: "steps", value: 500, default: 3000 }],
      in: { text: "first.text" },
      produces: { tally: "tally" },
      attempts: 2,
      jobId: "job_2",
      error: { type: "oom", message: "CUDA out of memory" },
      attemptLog: [
        { attempt: 1, jobId: "job_1b", reason: "initial", state: "failed", error: { type: "oom", message: "CUDA out of memory" } },
        { attempt: 2, jobId: "job_2", reason: "oom", batchScale: 0.75, state: "failed", error: { type: "oom", message: "CUDA out of memory" } },
      ],
    },
    { id: "pls_1", step: "first", position: 0, kind: "echo", kindVersion: "1", state: "done", params: {}, departures: [], in: {}, produces: { text: "text" }, attempts: 1, attemptLog: [], outputs: { text: { hash, type: "text", size: 12 } } },
  ],
};

const kind = {
  id: "ver_tally",
  stepKind: {
    name: "tally",
    version: "1",
    params: {
      type: "object",
      properties: {
        steps: { type: "integer", "x-cadence": { defaultRef: "training.steps", default: 3000, description: "Optimiser steps", source: "defaults.yaml" } },
        precision: { type: "string", enum: ["bf16", "fp16"], "x-cadence": { default: "bf16", description: "Precision", source: "defaults.yaml" } },
      },
    },
  },
} as unknown as StepKindVersion;

let qc: QueryClient;
beforeEach(() => {
  qc = new QueryClient({ defaultOptions: { queries: { retry: false, staleTime: Infinity } } });
  qc.setQueryData(pipelineRunsListQueryKey({ path: { p: "demo" }, query: { limit: 50 } }), { items: [run] });
  qc.setQueryData(pipelineRunsGetQueryKey({ path: { id: "plr_1" } }), run);
  qc.setQueryData(stepKindsGetQueryKey({ path: { id: "ver_tally" } }), kind);
  qc.setQueryData(defaultsGetQueryKey(), { training: { steps: { value: 3000, description: "Optimiser steps", source: "Community kit" } } } as unknown as Defaults);
  qc.setQueryData(jobLogsListQueryKey({ path: { id: "job_2" }, query: { tail: true, limit: 2000 } }), {
    items: [
      { seq: 1, t: "2026-09-30T10:01:00Z", level: "info", msg: "step 1" },
      { seq: 2, t: "2026-09-30T10:02:00Z", level: "error", msg: "CUDA out of memory" },
    ],
    nextAfter: 2,
  });
  qc.setQueryData(artifactsGetQueryKey({ path: { hash }, query: { content: true } }), { hash, type: "text", size: 12, directory: false, meta: {}, createdAt: "2026-09-30T10:00:00Z", encoding: "utf8", content: "hello world\n" });
  runCommand.mockReset();
  openDocument.mockReset();
  useTrainingFocus.setState({ job: null, pipelineRun: null });
});
afterEach(() => cleanup());

function wrap() {
  return render(
    <QueryClientProvider client={qc}>
      <TooltipProvider>
        <PanelContext.Provider value={{ instanceId: "pipeline-run", panelId: "pipeline-run", visible: false }}>
          <PipelineRunPanel panelId="pipeline-run" instanceId="pipeline-run" />
        </PanelContext.Provider>
      </TooltipProvider>
    </QueryClientProvider>,
  );
}

describe("Pipeline run", () => {
  it("lists steps in order with state, departures and attempts; opens the pipeline file", () => {
    wrap();
    const steps = [...document.querySelectorAll("[data-step]")].map((e) => [e.getAttribute("data-step"), e.getAttribute("data-state")]);
    expect(steps).toEqual([
      ["first", "done"],
      ["train", "failed"],
    ]);
    const train = document.querySelector('[data-step="train"]') as HTMLElement;
    expect(train.textContent).toContain("1 departure");
    expect(train.textContent).toContain("attempt 2");
    expect(train.textContent).toContain("oom: CUDA out of memory");
    fireEvent.click(screen.getByRole("button", { name: /Open pipeline file/ }));
    expect(openDocument).toHaveBeenCalledWith("recipe:pipelines/train-stage.yaml");
  });

  it("opens a step: parameters from the schema, departures, attempts and its log; focuses its job", () => {
    wrap();
    const train = document.querySelector('[data-step="train"]') as HTMLElement;
    fireEvent.click(within(train).getByRole("button", { name: /train/ }));
    expect(useTrainingFocus.getState().job).toEqual({ id: "job_2", label: "train-stage · train" });
    const form = within(train).getByRole("group", { name: "Parameters of train" });
    expect(form.querySelector('[data-field="steps"]')!.getAttribute("data-departs")).toBe("true");
    expect(form.querySelector('[data-field="precision"]')!.getAttribute("data-departs")).toBeNull();
    expect(within(train).getByRole("table", { name: "Attempts of train" }).textContent).toContain("0.75×");
    const log = within(train).getByRole("log", { name: "Log of train" });
    expect(log.textContent).toContain("CUDA out of memory");
    expect(log.querySelector('[data-level="error"]')).toBeTruthy();
  });

  it("previews an artifact's content", () => {
    wrap();
    const first = document.querySelector('[data-step="first"]') as HTMLElement;
    fireEvent.click(within(first).getByRole("button", { name: /first/ }));
    fireEvent.click(within(first).getByRole("button", { name: /b3:cdcd/ }));
    expect(within(first).getByText("hello world")).toBeTruthy();
  });

  it("retries a failed step with the run's revision", async () => {
    runCommand.mockResolvedValue({ ...run, state: "running" });
    wrap();
    const train = document.querySelector('[data-step="train"]') as HTMLElement;
    fireEvent.click(within(train).getByRole("button", { name: "Retry" }));
    await waitFor(() => expect(runCommand).toHaveBeenCalledWith("pipelineRuns.retry", { run, body: { step: "train" } }));
    await waitFor(() => expect(document.querySelector("[data-run]")!.getAttribute("data-state")).toBe("running"));
  });

  it("follows the pipeline run focused elsewhere", () => {
    const other = { ...run, id: "plr_9", pipeline: "import", steps: [] };
    qc.setQueryData(pipelineRunsGetQueryKey({ path: { id: "plr_9" } }), other);
    wrap();
    act(() => useTrainingFocus.getState().focusPipelineRun("plr_9"));
    expect(document.querySelector("[data-run]")!.getAttribute("data-run")).toBe("plr_9");
  });
});
