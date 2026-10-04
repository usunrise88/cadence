import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, render, screen } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import type { Batch } from "@/api/gen/types.gen";
import { invitationsListQueryKey } from "@/api/gen/@tanstack/react-query.gen";
import { TooltipProvider } from "@/components/ui/tooltip";
import { annotationBatchEntity, batchToEntity } from "@/entities/annotation";
import { PanelContext } from "@/shell/panel/context";
import { AnnotationBatchPanel } from "./AnnotationBatchPanel";

const runCommand = vi.fn();
vi.mock("@/shell/panel/commands", async (orig) => ({ ...(await orig<object>()), runCommand: (...a: unknown[]) => runCommand(...a) }));
vi.mock("@/shell/panel/actions", async (orig) => ({ ...(await orig<object>()), useProject: () => "calls" }));

const batch = {
  id: "anb_1",
  projectId: "prj_1",
  name: "calls-golden",
  purpose: "golden-set",
  state: "open",
  rev: 2,
  role: "caller",
  stratify: ["campaign", "duration"],
  doubleShare: 0.1,
  seed: 3,
  contextS: 2,
  guidelines: { name: "default", path: "annotation/guidelines/default.md", commit: "0123456789abcdef" },
  frame: { segmentsHash: "b3:abc", source: "calls-test", segments: 80 },
  strata: [{ key: { campaign: "calls", duration: "0–2 s" }, frame: 40, sampled: 5 }],
  progress: { items: 10, pending: 0, agreed: 8, disputed: 0, adjudicated: 1, excluded: 1, annotations: 11, doubleItems: 1, doubleDone: 1 },
  agreement: { pairs: 1, refWords: 40, edits: 1, iaaWer: 0.025, target: 0.05, meets: true },
  adjudication: { queue: 0 },
  eou: { items: 10, p50GapS: 0.4, p90GapS: 0.9, overlaps: 1 },
  reviewers: [{ id: "usr_ana", name: "ana", role: "annotator", annotations: 4 }],
  goldenSet: "calls-golden",
  canFreeze: { ok: true, reasons: [] },
  createdBy: { kind: "user", id: "usr_admin" },
  createdAt: "2026-10-04T00:00:00Z",
  updatedAt: "2026-10-04T00:00:00Z",
} as unknown as Batch;

let qc: QueryClient;
beforeEach(() => {
  qc = new QueryClient({ defaultOptions: { queries: { retry: false, staleTime: Infinity } } });
  qc.setQueryData(invitationsListQueryKey({ path: { id: "anb_1" } }), { items: [] });
  runCommand.mockReset();
});
afterEach(() => cleanup());

function wrap(b: Batch) {
  return render(
    <QueryClientProvider client={qc}>
      <TooltipProvider>
        <PanelContext.Provider value={{ instanceId: "annotation-batch:annotation_batch:anb_1", panelId: "annotation-batch", visible: false }}>
          <AnnotationBatchPanel panelId="annotation-batch" instanceId="annotation-batch:annotation_batch:anb_1" doc="annotation_batch:anb_1" entity={batchToEntity(b)} />
        </PanelContext.Provider>
      </TooltipProvider>
    </QueryClientProvider>,
  );
}

describe("Annotation batch document", () => {
  it("shows progress, agreement against its target and the guidelines commit", () => {
    wrap(batch);
    expect(screen.getByText(/8 agreed · 1 adjudicated · 1 excluded · 0 disputed · 0 pending/)).toBeTruthy();
    expect(screen.getByText("2.50 %")).toBeTruthy();
    expect(screen.getByText(/target ≤ 5.00 %/)).toBeTruthy();
    expect(screen.getByText("0123456789ab")).toBeTruthy();
    expect(screen.getByText(/p50 0.4 s · p90 0.9 s over 10 items · 1 overlaps/)).toBeTruthy();
  });

  it("freezes through the approval and invites a reviewer with a link shown once", async () => {
    runCommand.mockResolvedValueOnce({ approvalId: "apr_9" });
    wrap(batch);
    screen.getByRole("button", { name: /Freeze \(approval\)/ }).click();
    expect(await screen.findByText(/Waiting for the admin's approval \(apr_9\)/)).toBeTruthy();
    expect(runCommand).toHaveBeenCalledWith("batches.freeze", { batch: { id: "anb_1", rev: 2 }, dryRun: false });

    runCommand.mockResolvedValueOnce({ id: "crd_1", batchId: "anb_1", reviewer: { id: "usr_b", name: "bo" }, role: "annotator", expiresAt: "2026-10-10T00:00:00Z", createdAt: "", token: "cri_x", url: "/#invitation=cri_x" });
    screen.getByRole("button", { name: /Invite a reviewer/ }).click();
    const name = await screen.findByPlaceholderText("ana");
    const { fireEvent } = await import("@testing-library/react");
    fireEvent.change(name, { target: { value: "bo" } });
    screen.getByRole("button", { name: "Create the link" }).click();
    const link = (await screen.findByLabelText("Invitation link")) as HTMLInputElement;
    expect(link.value).toMatch(/\/#invitation=cri_x$/);
    expect(runCommand).toHaveBeenLastCalledWith("invitations.new", { batch: "anb_1", body: { name: "bo", role: "annotator" } });
  });

  it("does not offer the freeze while items wait", () => {
    const open = { ...batch, progress: { ...batch.progress, pending: 3 }, canFreeze: { ok: false, reasons: ["3 item(s) still need annotations"] } } as Batch;
    wrap(open);
    expect((screen.getByRole("button", { name: /Freeze \(approval\)/ }) as HTMLButtonElement).disabled).toBe(true);
    const e = batchToEntity(open);
    expect(annotationBatchEntity.verbs[0]!.enabled!(e)).toBe("3 item(s) still need annotations");
    expect(annotationBatchEntity.nextStep(e).command).toBe("view.openTriage");
  });
});
