import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import {
  deploymentsGetQueryKey,
  deploymentsListQueryKey,
  shadowReplaysGetQueryKey,
  shadowReplaysListQueryKey,
} from "@/api/gen/@tanstack/react-query.gen";
import type { Deployment, ShadowReplay } from "@/api/gen/types.gen";
import { PanelContext } from "@/shell/panel/context";
import { useSelection } from "@/shell/selection/store";
import { divergenceSpec, followedTarget, ShadowPanel } from "./ShadowPanel";

const runCommand = vi.fn();
const openDiffSegment = vi.fn();
const openAudio = vi.fn();
vi.mock("@/shell/panel/commands", async (orig) => ({ ...(await orig<object>()), runCommand: (...a: unknown[]) => runCommand(...a) }));
vi.mock("@/shell/panel/actions", async (orig) => ({ ...(await orig<object>()), useProject: () => "demo" }));
vi.mock("@/shell/diff/segment", async (orig) => ({ ...(await orig<object>()), openDiffSegment: (s: unknown) => openDiffSegment(s) }));
vi.mock("@/shell/audio", async (orig) => ({ ...(await orig<object>()), openAudio: (t: unknown) => openAudio(t) }));
vi.mock("@/shell/charts", async (orig) => ({ ...(await orig<object>()), AnalyticsChart: ({ spec }: { spec: { title: string } }) => <div data-testid="chart">{spec.title}</div> }));

const dep = {
  id: "dep_1",
  projectId: "prj_1",
  modelVersionId: "ver_m",
  modelVersion: "model/hebrew 2026-11-02.ab12cd",
  exportId: "mex_1",
  profile: "80ms",
  format: "fmt",
  targetId: "dtg_s",
  targetName: "staging",
  stage: "shadow",
  state: "active",
  decoding: { boostLists: [] },
  history: [],
  rev: 1,
  createdBy: { kind: "user", id: "usr_admin" },
  createdAt: "2026-11-01T00:00:00Z",
  updatedAt: "2026-11-01T00:00:00Z",
  shadow: { hours: 6, calls: 120, utterances: 900, nights: 2, minHours: 20, against: { kind: "model", versionId: "ver_p", label: "model/hebrew 2026-10-01.aa", deploymentId: "dep_0" } },
} as Deployment;

const nights: ShadowReplay[] = [
  { id: "srp_3", deploymentId: "dep_1", projectId: "prj_1", night: "2026-11-03", trigger: "nightly", state: "skipped", reason: "no calls not replayed yet", createdAt: "" },
  { id: "srp_2", deploymentId: "dep_1", projectId: "prj_1", night: "2026-11-02", trigger: "nightly", state: "done", calls: 60, hours: 3, utterances: 450, divergence: { wer: 0.08, ci: [0.06, 0.1] }, createdAt: "" },
  { id: "srp_1", deploymentId: "dep_1", projectId: "prj_1", night: "2026-11-01", trigger: "nightly", state: "done", calls: 60, hours: 3, utterances: 450, divergence: { wer: 0.1 }, textsEvictedAt: "2027-02-01T00:00:00Z", createdAt: "" },
];

const night2: ShadowReplay = {
  ...nights[1]!,
  against: { kind: "model", label: "model/hebrew 2026-10-01.aa", decodedBy: "serve" },
  worst: [{ audio: "b3:" + "a".repeat(64), call: "mount://calls/2026/11/02/c7.wav", start: 1.5, end: 4, duration: 2.5, wer: 0.5, candidate: "shalom olam", current: "shalom haolam" }],
};

let qc: QueryClient;
beforeEach(() => {
  runCommand.mockReset();
  openDiffSegment.mockReset();
  openAudio.mockReset();
  qc = new QueryClient({ defaultOptions: { queries: { retry: false, staleTime: Infinity } } });
  qc.setQueryData(deploymentsListQueryKey({ path: { p: "demo" } }), { items: [dep] });
  qc.setQueryData(deploymentsGetQueryKey({ path: { id: "dep_1" } }), dep);
  qc.setQueryData(shadowReplaysListQueryKey({ path: { id: "dep_1" } }), { items: nights });
  qc.setQueryData(shadowReplaysGetQueryKey({ path: { id: "srp_2" } }), night2);
  useSelection.setState({ activeDoc: null, selections: {}, pins: {} });
});
afterEach(cleanup);

function wrap() {
  return render(
    <QueryClientProvider client={qc}>
      <PanelContext.Provider value={{ instanceId: "shadow", panelId: "shadow", visible: false }}>
        <ShadowPanel panelId="shadow" instanceId="shadow" />
      </PanelContext.Provider>
    </QueryClientProvider>,
  );
}

describe("Shadow", () => {
  it("follows a deployment or a model document", () => {
    expect(followedTarget("deployment:dep_1")).toEqual({ deployment: "dep_1" });
    expect(followedTarget("model:ver_m")).toEqual({ model: "ver_m" });
    expect(followedTarget("eval:evl_1")).toEqual({});
  });

  it("charts the finished nights oldest first with their intervals", () => {
    const spec = divergenceSpec(nights);
    expect(spec.series[0]!.points.map((p) => [p.x, p.y, p.low, p.label])).toEqual([
      [1, 0.1, undefined, "2026-11-01"],
      [2, 0.08, 0.06, "2026-11-02"],
    ]);
  });

  it("shows the progress, the nights and the worst segments, which open in Diff and Audio", async () => {
    wrap();
    expect(screen.getByText("model/hebrew 2026-10-01.aa")).toBeTruthy();
    expect(screen.getByRole("progressbar", { name: "Shadow hours toward a canary" }).getAttribute("aria-valuenow")).toBe("6");
    expect(screen.getByTestId("chart").textContent).toBe("Divergence per night");
    const table = screen.getByRole("table", { name: "Nights" });
    expect(table.textContent).toContain("no calls not replayed yet");
    expect(table.textContent).toContain("Texts cleared after retention");
    const worst = await screen.findByTestId("shadow-worst");
    expect(worst.textContent).toContain("shalom haolam");
    fireEvent.click(screen.getByRole("button", { name: /Open c7.wav 1.5–4.0 s in Diff and Audio/ }));
    expect(openDiffSegment).toHaveBeenCalledWith(
      expect.objectContaining({ audio: night2.worst![0]!.audio, ref: "shalom haolam", hyp: "shalom olam", refLabel: "model/hebrew 2026-10-01.aa", hypLabel: dep.modelVersion }),
    );
    expect(openAudio).toHaveBeenCalledWith({ utterance: night2.worst![0]!.audio });
  });

  it("replays now after its dry run", async () => {
    runCommand.mockImplementation((_id: string, a: { dryRun?: boolean }) =>
      Promise.resolve(a.dryRun ? { ...nights[0], state: "planned", calls: 40, hours: 2.5, against: { label: "prod" } } : { approvalId: "apr_9" }),
    );
    wrap();
    fireEvent.click(screen.getByRole("button", { name: /Replay now/ }));
    expect((await screen.findByTestId("replay-plan")).textContent).toContain("40 calls");
    expect(runCommand).toHaveBeenCalledWith("shadowReplays.new", { deploymentId: "dep_1", dryRun: true });
    fireEvent.click(screen.getByRole("button", { name: "Start replay" }));
    await waitFor(() => expect(runCommand).toHaveBeenCalledWith("shadowReplays.new", { deploymentId: "dep_1", dryRun: false }));
    expect(await screen.findByText(/waits for an approval \(apr_9\)/)).toBeTruthy();
  });

  it("is empty without shadow deployments", () => {
    qc.setQueryData(deploymentsListQueryKey({ path: { p: "demo" } }), { items: [] });
    wrap();
    expect(screen.getByText("No shadow deployment")).toBeTruthy();
  });
});
