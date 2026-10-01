import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { computeListQueryKey, projectsListQueryKey, queueEntriesListQueryKey } from "@/api/gen/@tanstack/react-query.gen";
import type { ComputeHost, QueueEntry } from "@/api/gen/types.gen";
import { TooltipProvider } from "@/components/ui/tooltip";
import { PanelContext } from "@/shell/panel/context";
import { useTrainingFocus } from "@/shell/training/focus";
import { QueueGpuPanel } from "./QueueGpuPanel";

const runCommand = vi.fn();
const openPanelById = vi.fn();
vi.mock("@/shell/panel/commands", async (orig) => ({ ...(await orig<object>()), runCommand: (...a: unknown[]) => runCommand(...a) }));
vi.mock("@/shell/panel/actions", async (orig) => ({ ...(await orig<object>()), useProject: () => "demo", openPanelById: (id: string) => openPanelById(id) }));

const host: ComputeHost = {
  id: "cmp_1",
  name: "staging",
  description: "",
  health: { state: "healthy" },
  rev: 1,
  createdAt: "",
  updatedAt: "",
  cards: [
    {
      index: 0,
      name: "RTX PRO 5000",
      cardClass: "blackwell-48gb",
      memoryGb: 48,
      memoryCapGb: 24,
      allowedJobKinds: ["training", "eval"],
      windows: { training: [{ days: ["mon", "tue", "wed", "thu", "fri"], start: "20:00", end: "08:00" }] },
      telemetry: { index: 0, memoryUsedMb: 24000, memoryTotalMb: 49152, utilization: 0.1, reportedAt: "2026-09-30T10:00:00Z" },
    },
  ],
};

const entry = (jobId: string, over: Partial<QueueEntry>): QueueEntry => ({
  jobId,
  projectId: "prj_1",
  kind: "echo",
  kindVersion: "1",
  jobKind: "data",
  state: "waiting",
  priority: 0,
  enqueuedAt: "2026-09-30T10:00:00Z",
  attempt: 1,
  ...over,
});

const items: QueueEntry[] = [
  entry("job_train", {
    kind: "tally",
    jobKind: "training",
    state: "running",
    pipelineRunId: "plr_1",
    lease: { id: "lse_1", workerId: "wrk_1", host: "staging", card: 0, memoryCapMb: 24576, startedAt: "", heartbeatAt: "", progress: 0.4 },
  }),
  entry("job_a", { priority: 2 }),
  entry("job_b", { priority: 1 }),
  entry("job_p", { state: "paused" }),
];

let qc: QueryClient;
beforeEach(() => {
  qc = new QueryClient({ defaultOptions: { queries: { retry: false, staleTime: Infinity } } });
  qc.setQueryData(queueEntriesListQueryKey({ query: { project: "demo" } }), { items });
  qc.setQueryData(queueEntriesListQueryKey(), { items });
  qc.setQueryData(computeListQueryKey(), { items: [host] });
  qc.setQueryData(projectsListQueryKey(), { items: [{ id: "prj_1", slug: "demo" }] });
  runCommand.mockReset().mockResolvedValue({});
  openPanelById.mockReset();
  useTrainingFocus.setState({ job: null, pipelineRun: null });
});
afterEach(() => cleanup());

function wrap() {
  return render(
    <QueryClientProvider client={qc}>
      <TooltipProvider>
        <PanelContext.Provider value={{ instanceId: "queue-gpu", panelId: "queue-gpu", visible: false }}>
          <QueueGpuPanel panelId="queue-gpu" instanceId="queue-gpu" />
        </PanelContext.Provider>
      </TooltipProvider>
    </QueryClientProvider>,
  );
}

describe("Queue & GPU", () => {
  it("shows the card with its training slot, memory split and windows, and the queue in order", () => {
    const { container } = wrap();
    const card = container.querySelector('[data-card="staging#0"]') as HTMLElement;
    expect(within(card).getByText(/Training slot/).textContent).toContain("demo · tally");
    expect(card.querySelector('[data-slot="memory"]')!.textContent).toContain("Cadence");
    expect(card.textContent).toContain("training: Mon–Fri 20:00–08:00 (next day) (instance time)");
    expect(within(card).getByText("40 %")).toBeTruthy();
    const waiting = screen.getByRole("list", { name: "Waiting jobs" });
    expect([...waiting.querySelectorAll("[data-job]")].map((e) => e.getAttribute("data-job"))).toEqual(["job_a", "job_b", "job_p"]);
    expect(screen.getByText("4 jobs")).toBeTruthy();
  });

  it("reorders, pauses, resumes and cancels through one command each", async () => {
    wrap();
    fireEvent.click(screen.getAllByRole("button", { name: "Move echo up" })[1]!); // job_b passes job_a
    await waitFor(() => expect(runCommand).toHaveBeenCalledWith("jobs.edit", { jobId: "job_b", priority: 3 }));
    expect(screen.getAllByRole("button", { name: "Move echo up" })[0]).toHaveProperty("disabled", true);

    const a = document.querySelector('[data-job="job_a"]') as HTMLElement;
    fireEvent.click(within(a).getByRole("button", { name: "Pause" }));
    await waitFor(() => expect(runCommand).toHaveBeenCalledWith("jobs.pause", { jobId: "job_a" }));
    const p = document.querySelector('[data-job="job_p"]') as HTMLElement;
    fireEvent.click(within(p).getByRole("button", { name: "Resume" }));
    await waitFor(() => expect(runCommand).toHaveBeenCalledWith("jobs.resume", { jobId: "job_p" }));

    // Cancel asks once more.
    fireEvent.click(within(a).getByRole("button", { name: "Cancel" }));
    expect(runCommand).not.toHaveBeenCalledWith("jobs.cancel", expect.anything());
    fireEvent.click(within(a).getByRole("button", { name: "Confirm cancel" }));
    await waitFor(() => expect(runCommand).toHaveBeenCalledWith("jobs.cancel", { jobId: "job_a" }));
  });

  it("focuses a job for Logs and its pipeline run", () => {
    wrap();
    const t = document.querySelector('[data-job="job_train"]') as HTMLElement;
    fireEvent.click(within(t).getByRole("button", { name: "Logs" }));
    expect(useTrainingFocus.getState().job).toEqual({ id: "job_train", label: "tally@1 · demo" });
    expect(useTrainingFocus.getState().pipelineRun).toBe("plr_1");
    expect(openPanelById).toHaveBeenCalledWith("logs");
  });

  it("shows a command's error in place", async () => {
    runCommand.mockRejectedValueOnce(new Error("job moved on"));
    wrap();
    const a = document.querySelector('[data-job="job_a"]') as HTMLElement;
    fireEvent.click(within(a).getByRole("button", { name: "Pause" }));
    expect(await within(a).findByRole("alert")).toHaveProperty("textContent", "job moved on");
  });
});
