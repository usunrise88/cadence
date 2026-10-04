import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import type { DatasetVersion } from "@/api/gen/types.gen";
import { TooltipProvider } from "@/components/ui/tooltip";
import { datasetVersionToEntity } from "@/entities/data";
import { useEditRequests } from "@/shell/entity/edits";
import { PanelContext } from "@/shell/panel/context";
import { DatasetVersionPanel } from "./DatasetVersionPanel";

const runCommand = vi.fn();
vi.mock("@/shell/panel/commands", async (orig) => ({ ...(await orig<object>()), runCommand: (...a: unknown[]) => runCommand(...a) }));
vi.mock("@/shell/panel/actions", async (orig) => ({ ...(await orig<object>()), useProject: () => "demo", openDocument: vi.fn() }));
// ECharts needs a canvas, and the utterance search its own query: both are checked on their own (shell/data tests).
vi.mock("@/shell/charts", () => ({ AnalyticsChart: ({ spec }: { spec: { kind: string; title: string } }) => <div data-chart={spec.kind}>{spec.title}</div> }));
vi.mock("@/shell/data/UtteranceSearch", () => ({ UtteranceSearch: ({ dataset }: { dataset?: string }) => <div data-utterances={dataset} />, searchQuery: () => ({}) }));

const base = {
  id: "ver_d",
  kind: "dataset_version",
  collectionId: "reg_1",
  name: "dataset/parlaspeech-sr",
  version: "2026-10-03.abcdefabcdef",
  tags: ["locale:sr-RS"],
  licence: "CC-BY-SA-4.0",
  fingerprint: "f".repeat(64),
  actor: { kind: "user", id: "usr_admin", name: "admin" },
  createdAt: "2026-10-03T10:00:00Z",
  updatedAt: "2026-10-03T10:00:00Z",
  usedBy: [],
};

const draft = {
  ...base,
  state: "draft",
  dataset: {
    source: "mount://corpora/parlaspeech-sr/2026-10-01",
    locales: ["sr-RS"],
    splits: [{ name: "train", utterances: 900, hours: 9 }],
    hours: 9,
    utterances: 900,
    bytes: 0,
    fixture: false,
    frozen: false,
    segments: { hash: `b3:${"1".repeat(64)}`, type: "segments" },
  },
} as unknown as DatasetVersion;

const frozen = {
  ...base,
  state: "frozen",
  dataset: {
    ...draft.dataset,
    frozen: true,
    freeze: { pipelineRunId: "plr_1", frozenAt: "2026-10-03T11:00:00Z" },
    quality: {
      passed: false,
      checks: [
        { name: "silence_share", status: "pass", value: 0.1, threshold: 0.4 },
        { name: "clipping_share", status: "warn", value: 0.05, threshold: 0.01, message: "5 % of segments clip" },
      ],
    },
    card: { hash: `b3:${"2".repeat(64)}`, bytes: 2048 },
    stats: { durationHistogram: { edges: [0, 5], counts: [100, 800] }, durationPercentiles: { p5: 1, p50: 6, p95: 9 } },
    shards: [{ index: 0, hash: `b3:${"3".repeat(64)}`, utterances: 900, bytes: 3e8, seconds: 32400, location: "cas", pinned: true }],
  },
} as unknown as DatasetVersion;

let qc: QueryClient;
beforeEach(() => {
  qc = new QueryClient({ defaultOptions: { queries: { retry: false, staleTime: Infinity } } });
  runCommand.mockReset();
});
afterEach(() => cleanup());

function wrap(v: DatasetVersion) {
  return render(
    <QueryClientProvider client={qc}>
      <TooltipProvider>
        <PanelContext.Provider value={{ instanceId: `dataset-version:dataset_version:${v.id}`, panelId: "dataset-version", visible: false }}>
          <DatasetVersionPanel panelId="dataset-version" instanceId={`dataset-version:dataset_version:${v.id}`} doc={`dataset_version:${v.id}`} entity={datasetVersionToEntity(v)} />
        </PanelContext.Provider>
      </TooltipProvider>
    </QueryClientProvider>,
  );
}

describe("Dataset version: a draft", () => {
  it("checks leakage first, then freezes", async () => {
    runCommand.mockImplementation((_id: string, a: { dryRun?: boolean }) =>
      Promise.resolve(a.dryRun ? { version: draft, leakage: { passed: true, goldenSets: 3 } } : { jobId: "job_1" }),
    );
    wrap(draft);
    expect(screen.getByText(/Indexed in place/)).toBeTruthy();
    act(() => useEditRequests.getState().request("freeze:dataset_version:ver_d"));
    expect(await screen.findByText(/Leakage check passed against 3 golden sets/)).toBeTruthy();
    expect(runCommand).toHaveBeenCalledWith("datasets.freeze", { version: "ver_d", dryRun: true });
    screen.getByRole("button", { name: "Freeze" }).click();
    expect(await screen.findByText(/Freezing \(job job_1\)/)).toBeTruthy();
    expect(runCommand).toHaveBeenLastCalledWith("datasets.freeze", { version: "ver_d", dryRun: false });
  });

  it("shows the leakage refusal with the overlaps", async () => {
    const { ProblemError } = await import("@/api/client");
    runCommand.mockRejectedValue(
      new ProblemError({
        type: "https://cadence.local/help/errors/golden-set-leakage",
        title: "Golden set leakage",
        status: 422,
        detail: "2 utterances are in golden sets",
        errors: [{ path: "/overlaps/0", message: "golden-set/sr-calls holds 2 of its utterances" }],
      }),
    );
    wrap(draft);
    screen.getByRole("button", { name: "Freeze…" }).click();
    await waitFor(() => expect(document.querySelector('[data-slot="freeze-problem"]')?.getAttribute("data-leakage")).toBe("true"));
    expect(screen.getByText(/holds 2 of its utterances/)).toBeTruthy();
    expect((screen.getByRole("button", { name: "Freeze" }) as HTMLButtonElement).disabled).toBe(true);
  });

  it("previews hours per language and split after filters", async () => {
    runCommand.mockResolvedValue({
      versionId: "ver_d",
      frozen: false,
      utterances: 800,
      hours: 8,
      cells: [{ language: "sr-RS", split: "train", utterances: 800, hours: 8 }],
      dropped: { maxDuration: 100 },
    });
    wrap(draft);
    screen.getByRole("button", { name: "Preview with filters…" }).click();
    const max = await screen.findByLabelText("Max duration (s)");
    fireEvent.change(max, { target: { value: "30" } });
    screen.getByRole("button", { name: "Preview" }).click();
    expect(await screen.findByText(/Kept 800 utterances/)).toBeTruthy();
    expect(screen.getByText(/dropped 100 by maxDuration/)).toBeTruthy();
    expect(runCommand).toHaveBeenCalledWith("datasets.preview", { body: expect.objectContaining({ version: "ver_d" }) });
  });
});

describe("Dataset version: frozen", () => {
  it("shows the quality checks, card, shards, charts and the leakage result", () => {
    wrap(frozen);
    expect(document.querySelector('[data-slot="dataset-leakage"]')).toBeTruthy();
    expect(screen.getByText("Clipped segments")).toBeTruthy();
    expect(screen.getByText(/2 kB/)).toBeTruthy();
    expect(screen.getByText("✓ pinned")).toBeTruthy();
    expect(document.querySelector('[data-chart="histogram"]')?.textContent).toBe("Utterance duration");
    expect(document.querySelector("[data-utterances]")?.getAttribute("data-utterances")).toBe("ver_d");
  });

  it("offers adopting another language as replay", async () => {
    const { ProblemError } = await import("@/api/client");
    runCommand.mockImplementation((_id: string, a: { purpose?: string }) =>
      a.purpose === "replay"
        ? Promise.resolve({ projectId: "prj_1" })
        : Promise.reject(new ProblemError({ type: "https://cadence.local/help/errors/locale-mismatch", title: "x", status: 422, detail: "dataset/parlaspeech-sr is in sr-RS" })),
    );
    wrap(frozen);
    screen.getByRole("button", { name: /Adopt into demo/ }).click();
    const replay = await screen.findByRole("button", { name: "Check as replay" });
    replay.click();
    expect(await screen.findByText(/Checked: its licence and languages allow it/)).toBeTruthy();
    screen.getByRole("button", { name: "Adopt" }).click();
    expect(await screen.findByText(/Adopted as replay/)).toBeTruthy();
    expect(runCommand).toHaveBeenLastCalledWith("projects.adopt", { project: "demo", version: "ver_d", dryRun: false, purpose: "replay" });
  });
});
