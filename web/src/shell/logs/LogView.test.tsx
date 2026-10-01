import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, cleanup, render, screen, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { jobLogsListQueryKey } from "@/api/gen/@tanstack/react-query.gen";
import type { CadenceEvent, JobLogLine } from "@/api/gen/types.gen";
import { TooltipProvider } from "@/components/ui/tooltip";
import { PanelContext } from "@/shell/panel/context";
import { events } from "@/shell/registries";
import { LogView } from "./LogView";

const line = (seq: number, msg = `line ${seq}`): JobLogLine => ({ seq, t: "2026-09-30T10:00:00Z", level: "info", msg });

// What the worker holds: the history jobLogs.list answers with.
let server: JobLogLine[] = [];
const sdk = vi.hoisted(() => ({ jobLogsList: vi.fn() }));
vi.mock("@/api/gen/sdk.gen", async (orig) => ({ ...(await orig<object>()), ...sdk }));

class FakeSource {
  onopen = null;
  onerror = null;
  onmessage = null;
  close() {}
}

let qc: QueryClient;
beforeEach(() => {
  vi.stubGlobal("EventSource", FakeSource);
  sdk.jobLogsList.mockReset();
  sdk.jobLogsList.mockImplementation(() => Promise.resolve({ data: { items: [...server], nextAfter: server.length } }));
  server = [line(1), line(2)];
  qc = new QueryClient({ defaultOptions: { queries: { retry: false, staleTime: Infinity } } });
  qc.setQueryData(jobLogsListQueryKey({ path: { id: "job_1" }, query: { tail: true, limit: 2000 } }), { items: [line(1), line(2)], nextAfter: 2 });
});
afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

function view(visible: boolean) {
  return (
    <QueryClientProvider client={qc}>
      <TooltipProvider>
        <PanelContext.Provider value={{ instanceId: "logs", panelId: "logs", visible }}>
          <LogView jobId="job_1" />
        </PanelContext.Provider>
      </TooltipProvider>
    </QueryClientProvider>
  );
}

const live = (lines: JobLogLine[], seq: number): CadenceEvent => ({
  seq,
  topic: "job.job_1.log",
  type: "job.log",
  actor: { kind: "automation", id: "system" },
  at: "2026-09-30T10:00:00Z",
  payload: { jobId: "job_1", lines, dropped: 0 },
});

const seqs = () => [...screen.getByRole("log").querySelectorAll("[data-seq]")].map((n) => Number(n.getAttribute("data-seq")));

describe("LogView", () => {
  it("keeps the lines streamed before a remount", async () => {
    const first = render(view(true));
    await waitFor(() => expect(sdk.jobLogsList).toHaveBeenCalledTimes(1));
    server = [...server, line(3)];
    act(() => events.inject(live([line(3)], 2001)));
    await waitFor(() => expect(seqs()).toEqual([1, 2, 3]));
    first.unmount();
    server = [...server, line(4)];
    render(view(true));
    await waitFor(() => expect(seqs()).toEqual([1, 2, 3, 4]));
  });

  it("reads the history again when the panel is shown after lines arrived while hidden", async () => {
    const r = render(view(true));
    await waitFor(() => expect(sdk.jobLogsList).toHaveBeenCalledTimes(1));
    await waitFor(() => expect(seqs()).toEqual([1, 2]));
    r.rerender(view(false));
    // Hidden: no subscription, so this line never reaches the view.
    server = [...server, line(3), line(4)];
    act(() => events.inject(live([line(3), line(4)], 2002)));
    expect(sdk.jobLogsList).toHaveBeenCalledTimes(1);
    r.rerender(view(true));
    await waitFor(() => expect(sdk.jobLogsList).toHaveBeenCalledTimes(2));
    await waitFor(() => expect(seqs()).toEqual([1, 2, 3, 4]));
  });
});
