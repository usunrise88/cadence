import { describe, expect, it, vi } from "vitest";
import type { CadenceEvent } from "@/api/gen/types.gen";
import { approval } from "@/shell/approvals/testdata";
import { applyNoticeBatch, noticeFor } from "./live";

const actor = { kind: "user" as const, id: "usr_admin", name: "admin" };
const ev = (over: Partial<CadenceEvent>): CadenceEvent => ({ seq: 1, topic: "approvals", type: "x", actor, at: "2026-09-30T10:00:00Z", ...over });

describe("noticeFor", () => {
  it("announces an approval request with requester, reason and estimate, linking Approvals", () => {
    const a = approval({ actor: { kind: "automation", id: "crd_1", name: "ci" }, estimate: { gpuHours: 2 } });
    const n = noticeFor(ev({ type: "approval.requested", payload: { approval: a } }))!;
    expect(n.title).toBe("Approval requested: aliases.set");
    expect(n.detail).toContain("Automation · ci");
    expect(n.detail).toContain("2.0 GPU-h");
    expect(n.open).toEqual({ label: "Open Approvals", panel: "approvals" });
    expect(n.seq).toBe(1);
  });

  it("records decisions, expiry included", () => {
    const denied = approval({ state: "denied", decision: { grant: "once", note: "not now" } });
    expect(noticeFor(ev({ type: "approval.decided", payload: { approval: denied } }))).toMatchObject({ title: "Denied: aliases.set", detail: "not now" });
    const expired = approval({ state: "denied", decision: { grant: "once", expired: true } });
    expect(noticeFor(ev({ type: "approval.decided", payload: { approval: expired } }))!.title).toBe("Approval expired: aliases.set");
  });

  it("reports job failures and completions, not progress", () => {
    const job = { id: "job_1", kind: "bootstrap", progress: 1, attempt: 1, rev: 3, actor, createdAt: "", updatedAt: "" };
    expect(noticeFor(ev({ topic: "job.job_1", type: "job.state_changed", payload: { job: { ...job, state: "failed", error: "boom" } } }))).toMatchObject({ level: "error", title: "Job failed: bootstrap", detail: "boom" });
    expect(noticeFor(ev({ topic: "job.job_1", type: "job.state_changed", payload: { job: { ...job, state: "done" } } }))!.level).toBe("success");
    expect(noticeFor(ev({ topic: "job.job_1", type: "job.state_changed", payload: { job: { ...job, state: "running" } } }))).toBeUndefined();
    expect(noticeFor(ev({ topic: "job.job_1", type: "job.progress", payload: { job: { ...job, state: "running" } } }))).toBeUndefined();
  });

  it("reports a revoked credential", () => {
    const credential = { id: "crd_1", kind: "api_key", name: "ci", scope: {}, rev: 2, createdAt: "" };
    expect(noticeFor(ev({ topic: "entity.credential.crd_1", type: "credential.revoked", payload: { credential } }))).toMatchObject({ level: "warning", title: "Credential revoked: ci" });
  });
});

describe("applyNoticeBatch", () => {
  it("patches approvals from the approvals topic only, then pushes notices", () => {
    const a = approval();
    const patch = vi.fn();
    const push = vi.fn();
    applyNoticeBatch(
      [ev({ seq: 1, type: "approval.requested", payload: { approval: a } }), ev({ seq: 2, topic: "entity.approval.apr_0001", type: "approval.requested", payload: { approval: a } })],
      patch,
      push,
    );
    expect(patch).toHaveBeenCalledWith([a]);
    expect(push).toHaveBeenCalledTimes(1);
  });
});
