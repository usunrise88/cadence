import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { jobLogsListQueryKey, jobsGetQueryKey } from "@/api/gen/@tanstack/react-query.gen";
import type { CadenceEvent, JobLogLine } from "@/api/gen/types.gen";
import { TooltipProvider } from "@/components/ui/tooltip";
import { PanelContext } from "@/shell/panel/context";
import { events } from "@/shell/registries";
import { useTrainingFocus } from "@/shell/training/focus";
import { LogsPanel } from "./LogsPanel";

const line = (seq: number, level: JobLogLine["level"], msg: string): JobLogLine => ({ seq, t: "2026-09-30T10:00:00Z", level, msg });

// Subscribing opens the shell's one stream; the test feeds events with events.inject instead.
class FakeSource {
  onopen = null;
  onerror = null;
  onmessage = null;
  close() {}
}

let qc: QueryClient;
beforeEach(() => {
  vi.stubGlobal("EventSource", FakeSource);
  qc = new QueryClient({ defaultOptions: { queries: { retry: false, staleTime: Infinity } } });
  qc.setQueryData(jobLogsListQueryKey({ path: { id: "job_1" }, query: { tail: true, limit: 2000 } }), { items: [line(1, "info", "loading data"), line(2, "warn", "slow step")], nextAfter: 2 });
  qc.setQueryData(jobLogsListQueryKey({ path: { id: "job_2" }, query: { tail: true, limit: 2000 } }), { items: [line(1, "info", "other job")], nextAfter: 1 });
  qc.setQueryData(jobsGetQueryKey({ path: { id: "job_1" } }), { id: "job_1", kind: "step", state: "running", progress: 0.2, attempt: 1, rev: 1, actor: { kind: "user", id: "u" }, createdAt: "", updatedAt: "" });
  useTrainingFocus.setState({ job: null, pipelineRun: null });
});
afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

function wrap() {
  return render(
    <QueryClientProvider client={qc}>
      <TooltipProvider>
        <PanelContext.Provider value={{ instanceId: "logs", panelId: "logs", visible: true }}>
          <LogsPanel panelId="logs" instanceId="logs" />
        </PanelContext.Provider>
      </TooltipProvider>
    </QueryClientProvider>,
  );
}

const logEvent = (jobId: string, lines: JobLogLine[], seq: number): CadenceEvent => ({
  seq,
  topic: `job.${jobId}.log`,
  type: "job.log",
  actor: { kind: "automation", id: "system" },
  at: "2026-09-30T10:00:00Z",
  payload: { jobId, lines, dropped: 0 },
});

describe("Logs", () => {
  it("waits for a job, then shows the focused job's log with level marks", () => {
    wrap();
    expect(screen.getByText("No job selected")).toBeTruthy();
    act(() => useTrainingFocus.getState().focusJob({ id: "job_1", label: "echo@1 · demo" }));
    const log = screen.getByRole("log", { name: "Log of echo@1 · demo" });
    expect(log.querySelector('[data-seq="2"]')!.getAttribute("data-level")).toBe("warn");
    expect(screen.getByText("2 lines")).toBeTruthy();
    expect(screen.getByText("running")).toBeTruthy();
  });

  it("appends live lines from job.{id}.log", async () => {
    wrap();
    act(() => useTrainingFocus.getState().focusJob({ id: "job_1" }));
    act(() => events.inject(logEvent("job_1", [line(3, "error", "CUDA out of memory")], 1001)));
    await waitFor(() => expect(screen.getByText("3 lines")).toBeTruthy());
    expect(screen.getByRole("log").querySelector('[data-seq="3"]')!.getAttribute("data-level")).toBe("error");
  });

  it("pins the job while the focus moves on", () => {
    wrap();
    act(() => useTrainingFocus.getState().focusJob({ id: "job_1", label: "first" }));
    fireEvent.click(screen.getByRole("button", { name: "Pin this job" }));
    act(() => useTrainingFocus.getState().focusJob({ id: "job_2", label: "second" }));
    expect(screen.getByRole("log", { name: "Log of first" })).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: "Unpin: follow the active job" }));
    expect(screen.getByRole("log", { name: "Log of second" }).textContent).toContain("other job");
  });

  it("follow turns off with the toggle", () => {
    wrap();
    act(() => useTrainingFocus.getState().focusJob({ id: "job_1" }));
    const follow = screen.getByRole("button", { name: "Follow" });
    expect(follow.getAttribute("aria-pressed")).toBe("true");
    fireEvent.click(follow);
    expect(follow.getAttribute("aria-pressed")).toBe("false");
  });
});
