import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import {
  approvalsGetQueryKey,
  deploymentsListQueryKey,
  deploymentTargetsListQueryKey,
  promotionsGetQueryKey,
  promotionsListQueryKey,
} from "@/api/gen/@tanstack/react-query.gen";
import type { Deployment, ModelExport, ModelVersion } from "@/api/gen/types.gen";
import { PanelContext } from "@/shell/panel/context";
import { DeploymentsSection } from "./DeploymentsSection";
import { benchmarkSpec, ExportsSection } from "./ExportsSection";

const runCommand = vi.fn();
vi.mock("@/shell/panel/commands", async (orig) => ({ ...(await orig<object>()), runCommand: (...a: unknown[]) => runCommand(...a) }));
vi.mock("@/shell/panel/actions", async (orig) => ({ ...(await orig<object>()), useProject: () => "demo", openDocument: vi.fn() }));
vi.mock("@/shell/charts", () => ({ AnalyticsChart: ({ spec }: { spec: { kind: string; title: string } }) => <div data-chart={spec.kind}>{spec.title}</div> }));

const actor = { kind: "user" as const, id: "usr_admin", name: "admin" };

const exported: ModelExport = {
  id: "mex_1",
  modelVersionId: "ver_m",
  profile: "80ms",
  format: "fmt-a",
  state: "exported",
  deployableHash: `b3:${"a".repeat(64)}`,
  projectId: "prj_1",
  parity: { state: "passed", werDelta: 0, identicalShare: 0.99, compared: "tokens" },
  benchmarks: [{ state: "done", streams: 32, budgetMs: 100, p95ChunkLatencyMs: 19.5, verdict: "passed", maxStreamsWithinBudget: 128, finishedAt: "2026-10-05T10:00:00Z" }],
  rev: 2,
  createdBy: actor,
  createdAt: "2026-10-05T09:00:00Z",
  updatedAt: "2026-10-05T10:00:00Z",
};

const model = {
  id: "ver_m",
  kind: "model",
  name: "model/demo",
  version: "2026-10-05.abc",
  state: "frozen",
  usedBy: [],
  exports: [exported],
} as unknown as ModelVersion;

const shadow: Deployment = {
  id: "dep_1",
  projectId: "prj_1",
  modelVersionId: "ver_m",
  modelVersion: "model/demo 2026-10-05.abc",
  exportId: "mex_1",
  profile: "80ms",
  format: "fmt-a",
  targetId: "dtg_s",
  targetName: "staging",
  stage: "shadow",
  state: "active",
  decoding: { boostLists: [] },
  shadow: { hours: 4.5, calls: 12, utterances: 300, nights: 2, minHours: 20, divergence: { wer: 0.05, ci: [0.04, 0.06] } },
  history: [],
  rev: 1,
  createdBy: actor,
  createdAt: "2026-10-05T09:00:00Z",
  updatedAt: "2026-10-05T09:00:00Z",
};

const target = { id: "dtg_p", name: "era-production", kind: "delivery", slots: ["asr-he-il"], state: "active", serves: [], server: { kind: "x", version: "1" }, rev: 1, createdBy: actor, createdAt: "", updatedAt: "" };

let qc: QueryClient;
beforeEach(() => {
  qc = new QueryClient({ defaultOptions: { queries: { retry: false, staleTime: Infinity } } });
  runCommand.mockReset();
  qc.setQueryData(deploymentTargetsListQueryKey(), { items: [target], signingKeys: [] });
});
afterEach(() => cleanup());

function wrap(node: React.ReactNode) {
  return render(
    <QueryClientProvider client={qc}>
      <PanelContext.Provider value={{ instanceId: "model:model:ver_m", panelId: "model", visible: false }}>{node}</PanelContext.Provider>
    </QueryClientProvider>,
  );
}

const listKey = deploymentsListQueryKey({ path: { p: "demo" }, query: { version: "ver_m", state: "all" } });

describe("Model document: exports", () => {
  it("shows parity and the benchmark, and charts p95 against the budget", () => {
    wrap(<ExportsSection m={model} />);
    expect(screen.getByText(/99.0% identical/)).toBeTruthy();
    expect(screen.getByText(/p95 19.5 ms at 32 streams/)).toBeTruthy();
    expect(document.querySelector('[data-chart="bar"]')?.textContent).toBe("p95 chunk latency against the budget");
    const spec = benchmarkSpec([exported]);
    expect(spec?.kind === "bar" && spec.series.map((s) => s.values[0])).toEqual([19.5, 100]);
    expect(benchmarkSpec([{ ...exported, benchmarks: [] }])).toBeUndefined();
  });

  it("plans an export before running it", async () => {
    runCommand.mockImplementation((_id: string, a: { dryRun?: boolean }) =>
      Promise.resolve(a.dryRun ? { profiles: [{ profile: "160ms", action: "export" }], estimate: { known: true, gpuHours: 0.1 } } : { pipelineRun: { id: "plr_9" } }),
    );
    wrap(<ExportsSection m={model} />);
    fireEvent.click(screen.getByRole("button", { name: "Export…" }));
    fireEvent.change(screen.getByLabelText("Latency profile"), { target: { value: "160ms" } });
    fireEvent.click(screen.getByRole("button", { name: "Plan" }));
    expect(await screen.findByText(/160ms: export/)).toBeTruthy();
    expect(runCommand).toHaveBeenCalledWith("models.export", { project: "demo", body: { version: "ver_m", profiles: ["160ms"] }, dryRun: true });
    fireEvent.click(screen.getByRole("button", { name: "Export" }));
    expect(await screen.findByText(/Started pipeline run plr_9/)).toBeTruthy();
  });
});

describe("Model document: deployments", () => {
  it("promotes through the confirm modal: checks first, then the approval decided by the person", async () => {
    qc.setQueryData(listKey, { items: [shadow] });
    qc.setQueryData(approvalsGetQueryKey({ path: { id: "apr_1" } }), { id: "apr_1", rev: 1, state: "pending" });
    let ready = false;
    runCommand.mockImplementation((id: string, a: { dryRun?: boolean }) => {
      if (id === "approvals.approve") return Promise.resolve({ id: "apr_1", state: "approved", result: { status: 200, body: { record: { id: "prm_1", kind: "promotion", seq: 2 } } } });
      if (!a.dryRun) return Promise.resolve({ approvalId: "apr_1" });
      return Promise.resolve({
        deploymentId: "dep_1",
        kind: "promotion",
        stage: "canary",
        targetName: "era-production",
        slot: "asr-he-il",
        trafficShare: 0.05,
        ready,
        checks: [
          { name: "parity", state: "passed", detail: "ΔWER +0.0000" },
          ready ? { name: "shadow", state: "passed" } : { name: "shadow", state: "failed", problemType: "shadow-volume-short", detail: "4.5 of 20 h" },
        ],
      });
    });
    wrap(<DeploymentsSection m={model} />);
    expect(screen.getByText(/4.5 h of 20 h replayed/)).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: "Promote…" }));
    fireEvent.change(await screen.findByLabelText("Delivery target"), { target: { value: "era-production" } });
    fireEvent.change(screen.getByLabelText("Slot"), { target: { value: "asr-he-il" } });
    fireEvent.change(screen.getByLabelText("Reason"), { target: { value: "beats production" } });
    fireEvent.click(screen.getByRole("button", { name: "Check" }));
    expect(await screen.findByText("shadow-volume-short")).toBeTruthy();
    expect((screen.getByRole("button", { name: "Confirm promotion" }) as HTMLButtonElement).disabled).toBe(true);
    expect(runCommand).toHaveBeenCalledWith("deployments.promote", {
      deployment: shadow,
      body: { stage: "canary", reason: "beats production", target: "era-production", slot: "asr-he-il" },
      dryRun: true,
    });
    ready = true;
    fireEvent.click(screen.getByRole("button", { name: "Check" }));
    await waitFor(() => expect((screen.getByRole("button", { name: "Confirm promotion" }) as HTMLButtonElement).disabled).toBe(false));
    fireEvent.click(screen.getByRole("button", { name: "Confirm promotion" }));
    expect(await screen.findByText(/Signed promotion record prm_1/)).toBeTruthy();
    expect(runCommand).toHaveBeenCalledWith("approvals.approve", expect.objectContaining({ approval: expect.objectContaining({ id: "apr_1" }) }));
  });

  it("confirms a pending delivery with its receipt and shows a mismatch", async () => {
    const pending: Deployment = { ...shadow, stage: "canary", state: "pending-delivery", targetId: "dtg_p", targetName: "era-production", slot: "asr-he-il",
      pending: { recordId: "prm_1", kind: "promotion", stage: "canary", targetId: "dtg_p", slot: "asr-he-il" } };
    qc.setQueryData(listKey, { items: [pending] });
    qc.setQueryData(promotionsGetQueryKey({ path: { id: "prm_1" } }), {
      id: "prm_1", kind: "promotion", seq: 2, hash: "f".repeat(64), rev: 1, state: "pending",
      delivery: { state: "ready", script: "#!/bin/sh\n", smoke: { utterances: 20, required: 20 }, bundleUrl: "/api/promotions/prm_1/delivery?sig=x" },
    });
    qc.setQueryData(promotionsListQueryKey({ path: { id: "dtg_p" }, query: { project: "demo", slot: "asr-he-il" } }), {
      targetId: "dtg_p", targetName: "era-production", intact: true,
      items: [{ id: "prm_0", seq: 1, kind: "genesis", hash: "0".repeat(64), verified: true }, { id: "prm_1", seq: 2, kind: "promotion", hash: "f".repeat(64), verified: true, state: "pending" }],
    });
    const { ProblemError } = await import("@/api/client");
    runCommand.mockRejectedValueOnce(
      new ProblemError({ type: "https://cadence.local/help/errors/promotion-receipt-mismatch", title: "Promotion receipt mismatch", status: 422, detail: "the smoke check passed 18 of 20" }),
    );
    wrap(<DeploymentsSection m={model} />);
    expect(screen.getByRole("link", { name: "Download the bundle" }).getAttribute("href")).toContain("/delivery?sig=");
    expect(document.querySelector('[data-slot="promotion-chain"]')?.getAttribute("data-intact")).toBe("true");
    fireEvent.change(screen.getByLabelText("Receipt"), { target: { value: "CADENCE-RECEIPT 1 fff" } });
    fireEvent.click(screen.getByRole("button", { name: "Confirm delivery" }));
    expect(await screen.findByText(/The receipt does not match: the smoke check passed 18 of 20/)).toBeTruthy();
    expect(runCommand).toHaveBeenCalledWith("promotions.verify", { record: "prm_1", rev: 1, receipt: "CADENCE-RECEIPT 1 fff" });
    runCommand.mockResolvedValueOnce({ id: "prm_1", closedBy: "prm_2" });
    fireEvent.change(screen.getByLabelText("Receipt"), { target: { value: "CADENCE-RECEIPT 1 fff ok" } });
    fireEvent.click(screen.getByRole("button", { name: "Confirm delivery" }));
    expect(await screen.findByText(/Confirmed: prm_2/)).toBeTruthy();
  });
});
