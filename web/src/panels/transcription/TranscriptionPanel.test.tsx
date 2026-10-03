import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import {
  baseModelsListOptions,
  checkpointsListOptions,
  langpacksListOptions,
  modelFamiliesListOptions,
  modelsListOptions,
  projectsGetOptions,
} from "@/api/gen/@tanstack/react-query.gen";
import type { TranscriptionSession } from "@/api/gen/types.gen";
import { TooltipProvider } from "@/components/ui/tooltip";
import { PanelContext } from "@/shell/panel/context";
import { TranscriptionPanel } from "./TranscriptionPanel";

const runCommand = vi.fn();
vi.mock("@/shell/panel/commands", async (orig) => ({ ...(await orig<object>()), runCommand: (...a: unknown[]) => runCommand(...a) }));
vi.mock("@/shell/panel/actions", async (orig) => ({ ...(await orig<object>()), useProject: () => "demo" }));

class FakeSocket {
  static last: FakeSocket | null = null;
  binaryType = "blob";
  readyState = 0;
  bufferedAmount = 0;
  sent: unknown[] = [];
  url: string;
  onopen: ((ev: Event) => void) | null = null;
  onmessage: ((ev: MessageEvent) => void) | null = null;
  onclose: ((ev: CloseEvent) => void) | null = null;
  onerror: ((ev: Event) => void) | null = null;
  constructor(url: string) {
    this.url = url;
    FakeSocket.last = this;
  }
  send(d: unknown) {
    this.sent.push(d);
  }
  close() {
    this.readyState = 3;
  }
  receive(m: unknown) {
    this.onmessage?.({ data: JSON.stringify(m) } as MessageEvent);
  }
}

const SESSION: TranscriptionSession = {
  id: "trs_00000000-0000-0000-0000-000000000000",
  projectId: "prj_1",
  jobId: "job_1",
  state: "queued",
  streamUrl: "/api/transcriptions/trs_00000000-0000-0000-0000-000000000000/stream?ticket=t",
  ticket: "t",
  input: { kind: "span", utteranceId: "utt_1", start: 1, end: 3 },
  targets: [
    { target: "A", kind: "base_model", id: "ver_b", label: "base-model/nemotron 2026-09-01", family: "f", profile: "160ms", language: "he-IL", weightsKey: "w1" },
    { target: "B", kind: "base_model", id: "ver_b", label: "base-model/nemotron 2026-09-01", family: "f", profile: "1120ms", language: "he-IL", weightsKey: "w1" },
  ],
  pace: "realtime",
  blind: true,
  family: "f",
  liveKind: "live@1",
  reservationMb: 6000,
  limits: { sessionSeconds: 900, idleSeconds: 300, ticketSeconds: 60, frameMs: 20, maxFileSeconds: 900, maxFileBytes: 300_000_000, maxMessageBytes: 65536, queueWaitSeconds: 900 },
  allowance: { gpuHoursPerDay: 1, usedGpuHours: 0.1, remainingGpuHours: 0.9 },
  createdAt: "2026-10-02T10:00:00Z",
};

let qc: QueryClient;
beforeEach(() => {
  qc = new QueryClient({ defaultOptions: { queries: { retry: false, staleTime: Infinity } } });
  qc.setQueryData(baseModelsListOptions().queryKey, { items: [{ id: "ver_b", name: "base-model/nemotron", version: "2026-09-01", baseModel: { familyId: "f" } }] } as never);
  qc.setQueryData(modelsListOptions().queryKey, { items: [] });
  qc.setQueryData(modelFamiliesListOptions().queryKey, { items: [] });
  qc.setQueryData(checkpointsListOptions({ path: { p: "demo" }, query: { kept: true, limit: 100 } }).queryKey, { items: [], keepTopK: 3 });
  qc.setQueryData(projectsGetOptions({ path: { p: "demo" } }).queryKey, { locales: ["he-IL"] } as never);
  qc.setQueryData(langpacksListOptions({ path: { p: "demo" } }).queryKey, { items: [], shipped: [] });
  runCommand.mockReset();
  vi.stubGlobal("WebSocket", FakeSocket);
});
afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

function wrap() {
  return render(
    <QueryClientProvider client={qc}>
      <TooltipProvider>
        <PanelContext.Provider value={{ instanceId: "transcription", panelId: "transcription", visible: true }}>
          <TranscriptionPanel panelId="transcription" instanceId="transcription" />
        </PanelContext.Provider>
      </TooltipProvider>
    </QueryClientProvider>,
  );
}

describe("Transcription panel", () => {
  it("opens a session, waits in the queue, streams lanes and keeps blind lanes unnamed until the pick", async () => {
    runCommand.mockResolvedValue(SESSION);
    wrap();
    fireEvent.change(screen.getByLabelText("Input"), { target: { value: "span" } });
    fireEvent.change(screen.getByLabelText("Utterance span"), { target: { value: "utt_1#t=1,3" } });
    fireEvent.change(screen.getByLabelText("Target 1: model"), { target: { value: "ver_b" } });
    fireEvent.click(screen.getByLabelText("Blind"));
    fireEvent.click(screen.getByRole("button", { name: /Go live/ }));
    await waitFor(() => expect(FakeSocket.last).not.toBeNull());
    expect(runCommand).toHaveBeenCalledWith("transcriptions.new", {
      project: "demo",
      body: { input: { kind: "span", utteranceId: "utt_1", start: 1, end: 3, channel: undefined }, targets: [{ baseModelVersionId: "ver_b" }], pace: "realtime", blind: true },
    });
    const sock = FakeSocket.last!;
    expect(sock.url).toMatch(/^ws:\/\/.*\/api\/transcriptions\/trs_.*\/stream\?ticket=t$/);
    await act(async () => {
      sock.readyState = 1;
      sock.onopen?.(new Event("open"));
    });
    await waitFor(() => expect(sock.sent).toHaveLength(1));
    expect(JSON.parse(sock.sent[0] as string)).toEqual({ type: "start", input: { kind: "span" } });
    act(() => sock.receive({ type: "waiting", state: "queued", position: 2, reason: "no card has 6000 MB free" }));
    expect(screen.getByText(/place 2: no card has 6000 MB free/)).toBeTruthy();
    act(() => sock.receive({ type: "started", targets: [{ target: "A", profile: "160ms", chunkMs: 160, language: "he-IL", loadS: 22 }], resampler: "polyphase" }));
    act(() => sock.receive({ type: "partial", target: "A", segment: 0, seq: 1, text: "שלו", audioEnd: 0.3 }));
    act(() => sock.receive({ type: "final", target: "B", segment: 0, seq: 1, text: "שלום 123", words: [{ word: "שלום", start: 0, end: 0.4 }, { word: "123", start: 0.4, end: 0.8, confidence: 0.4 }], endpoint: "eou", audioEnd: 0.9, space: true }));
    expect(screen.getByText("שלו")).toBeTruthy();
    expect(screen.getByText("123").className).toMatch(/opacity-50/);
    expect(screen.queryByText(/1120ms/)).toBeNull(); // blind: labels hidden
    fireEvent.click(screen.getAllByRole("button", { name: "This one is better" })[1]!);
    expect(screen.getByText(/1120ms/)).toBeTruthy();
    fireEvent.change(screen.getByLabelText(/Reference/), { target: { value: "שלום 124" } });
    expect(screen.getByText(/WER 50.0 %/)).toBeTruthy();
  });

  it("explains a refused session", async () => {
    runCommand.mockRejectedValue(new Error("you already have an open session"));
    wrap();
    fireEvent.change(screen.getByLabelText("Input"), { target: { value: "span" } });
    fireEvent.change(screen.getByLabelText("Utterance span"), { target: { value: "utt_1" } });
    fireEvent.change(screen.getByLabelText("Target 1: model"), { target: { value: "ver_b" } });
    fireEvent.click(screen.getByRole("button", { name: /Go live/ }));
    expect(await screen.findByText("you already have an open session")).toBeTruthy();
  });
});
