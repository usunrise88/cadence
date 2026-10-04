import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { exportsListQueryKey, mountsListQueryKey, textsGetQueryKey } from "@/api/gen/@tanstack/react-query.gen";
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

const cardHash = `b3:${"2".repeat(64)}`;
function seed(exports: unknown[] = []) {
  qc.setQueryData(textsGetQueryKey({ path: { hash: cardHash } }), {
    hash: cardHash,
    kind: "dataset-card",
    mediaType: "text/markdown",
    text: "# parlaspeech-sr\n\n- **Licence:** CC-BY-SA-4.0\n\n<script>alert(1)</script>\n",
    bytes: 2048,
    truncated: false,
    versionId: "ver_d",
  });
  qc.setQueryData(exportsListQueryKey({ path: { p: "demo" }, query: { version: "ver_d" } }), { items: exports });
  qc.setQueryData(mountsListQueryKey(), {
    items: [
      { id: "mnt_e", name: "exports", kind: "local", root: "/mnt/exports", readOnly: false },
      { id: "mnt_c", name: "corpora", kind: "local", root: "/mnt/corpora", readOnly: true },
    ],
  });
}

describe("Dataset version: frozen", () => {
  it("shows the quality checks, card, shards, charts and the leakage result", () => {
    seed();
    wrap(frozen);
    expect(document.querySelector('[data-slot="dataset-leakage"]')).toBeTruthy();
    expect(screen.getByText("Clipped segments")).toBeTruthy();
    expect(screen.getByText(/2 kB/)).toBeTruthy();
    // The card renders as Markdown (texts.get), raw HTML dropped.
    const card = document.querySelector('[data-slot="dataset-card-text"]');
    expect(card?.querySelector("h1")?.textContent).toBe("parlaspeech-sr");
    expect(card?.textContent).toContain("Licence: CC-BY-SA-4.0");
    expect(card?.textContent).not.toContain("alert(1)");
    expect(card?.querySelector("script")).toBeNull();
    expect(screen.getByText("✓ pinned")).toBeTruthy();
    expect(document.querySelector('[data-chart="histogram"]')?.textContent).toBe("Utterance duration");
    expect(document.querySelector("[data-utterances]")?.getAttribute("data-utterances")).toBe("ver_d");
  });

  it("charts the end-of-utterance gaps only when the version has them", () => {
    seed();
    const withEou = {
      ...frozen,
      dataset: {
        ...frozen.dataset,
        stats: { ...frozen.dataset!.stats, eou: { utterances: 577, withGap: 420, overlapping: 31, p50GapS: 0.62, p90GapS: 1.84, gapHistogram: { edges: [-1, 0, 1], counts: [31, 300, 89] } } },
      },
    } as unknown as DatasetVersion;
    const { unmount } = wrap(withEou);
    expect(document.querySelector('[data-chart="End-of-utterance gap"]')).toBeTruthy();
    unmount();
    wrap(frozen);
    expect(document.querySelector('[data-chart="End-of-utterance gap"]')).toBeNull();
  });

  it("lists the version's exports with state and files", () => {
    seed([
      {
        id: "dex_1",
        versionId: "ver_d",
        format: "nemo-manifest",
        projectId: "prj_1",
        target: "mount://exports/parlaspeech-sr/x/nemo-manifest",
        state: "done",
        pipelineRunId: "plr_9",
        files: 901,
        bytes: 3e8,
        copies: 900,
        createdBy: { kind: "user", id: "usr_admin", name: "admin" },
        createdAt: "2026-10-04T10:00:00Z",
        rev: 2,
      },
    ]);
    wrap(frozen);
    const row = document.querySelector('[data-export="dex_1"]');
    expect(row?.textContent).toContain("nemo-manifest");
    expect(row?.textContent).toContain("mount://exports/parlaspeech-sr/x/nemo-manifest");
    expect(row?.textContent).toContain("901");
    expect(row?.textContent).toContain("900 mount copies");
  });

  it("plans an export, then starts it on a writable mount", async () => {
    seed();
    runCommand.mockImplementation((_id: string, a: { dryRun?: boolean; body: { target?: string } }) =>
      Promise.resolve(
        a.dryRun
          ? {
              versionId: "ver_d",
              collection: "dataset/parlaspeech-sr",
              version: base.version,
              format: "lhotse-shar",
              projectId: "prj_1",
              project: "demo",
              target: a.body.target,
              stepKind: "shar_export@1",
              utterances: 900,
              hours: 9,
              bytes: 3e8,
              licence: "CC-BY-SA-4.0",
              sources: ["parlaspeech-sr"],
              approval: false,
              copies: false,
            }
          : { id: "dex_2", format: "lhotse-shar", state: "running", target: a.body.target, files: 0, bytes: 0, copies: 0, projectId: "prj_1", createdAt: "", createdBy: {}, rev: 1 },
      ),
    );
    wrap(frozen);
    act(() => useEditRequests.getState().request("export:dataset_version:ver_d"));
    const target = await screen.findByLabelText("Target");
    expect(screen.queryByRole("option", { name: /corpora/ })).toBeNull(); // read-only mounts are no target
    fireEvent.change(target, { target: { value: "mount:exports" } });
    expect((screen.getByRole("button", { name: "Export" }) as HTMLButtonElement).disabled).toBe(true); // plan first
    screen.getByRole("button", { name: "Plan" }).click();
    expect(await screen.findByText("shar_export@1 in demo")).toBeTruthy();
    const body = { version: "ver_d", format: "lhotse-shar", project: "demo", target: `mount://exports/parlaspeech-sr/${base.version}/lhotse-shar` };
    expect(runCommand).toHaveBeenCalledWith("datasets.export", { body, dryRun: true });
    screen.getByRole("button", { name: "Export" }).click();
    expect(await screen.findByText(/Export dex_2 started/)).toBeTruthy();
    expect(runCommand).toHaveBeenLastCalledWith("datasets.export", { body, dryRun: false });
  });

  it("asks for the Hub repository and says the push waits for an approval", async () => {
    seed();
    runCommand.mockImplementation((_id: string, a: { dryRun?: boolean }) =>
      Promise.resolve(
        a.dryRun
          ? { versionId: "ver_d", collection: "c", version: "v", format: "hf-hub", projectId: "p", project: "demo", target: "hf://datasets/org/sr", stepKind: "hf_push@1", utterances: 1, hours: 1, bytes: 1, licence: "CC-BY-4.0", sources: [], approval: true, copies: false }
          : { approvalId: "apr_1" },
      ),
    );
    wrap(frozen);
    screen.getByRole("button", { name: "Export…" }).click();
    fireEvent.change(await screen.findByLabelText("Format"), { target: { value: "hf-hub" } });
    expect((screen.getByRole("button", { name: "Plan" }) as HTMLButtonElement).disabled).toBe(true);
    fireEvent.change(screen.getByLabelText("Hub repository"), { target: { value: "org/sr" } });
    screen.getByRole("button", { name: "Plan" }).click();
    const ask = await screen.findByRole("button", { name: "Ask to export" });
    ask.click();
    expect(await screen.findByText(/waits for an approval \(apr_1\)/)).toBeTruthy();
    expect(runCommand).toHaveBeenLastCalledWith("datasets.export", { body: { version: "ver_d", format: "hf-hub", project: "demo", hubRepo: "org/sr" }, dryRun: false });
  });

  it("offers adopting another language as replay", async () => {
    seed();
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
