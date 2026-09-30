import { describe, expect, it } from "vitest";
import { QueryClient } from "@tanstack/react-query";
import { approvalsListQueryKey } from "@/api/gen/@tanstack/react-query.gen";
import type { ApprovalList } from "@/api/gen/types.gen";
import { DECIDED_QUERY, mergeApprovals, patchApprovals, PENDING_QUERY } from "./cache";
import { actorLabel, countdown, decisionLine, estimateLine } from "./format";
import { rememberSessions } from "@/shell/agents/labels";
import { session } from "@/shell/agents/testdata";
import { approval } from "./testdata";

const list = (...items: ReturnType<typeof approval>[]): ApprovalList => ({ items });

describe("mergeApprovals", () => {
  it("inserts a new pending approval in order (oldest first)", () => {
    const a = approval({ id: "apr_a", createdAt: "2026-09-30T10:00:00Z" });
    const b = approval({ id: "apr_b", createdAt: "2026-09-30T09:00:00Z" });
    expect(mergeApprovals(list(a), [b], { state: "pending" })!.items.map((x) => x.id)).toEqual(["apr_b", "apr_a"]);
  });

  it("drops a decided approval from the pending list and adds it to the decided one, newest decision first", () => {
    const a = approval({ id: "apr_a" });
    const decided = approval({ id: "apr_a", state: "approved", rev: 2, decidedAt: "2026-09-30T12:00:00Z" });
    const older = approval({ id: "apr_o", state: "denied", rev: 2, decidedAt: "2026-09-30T11:00:00Z" });
    expect(mergeApprovals(list(a), [decided], { state: "pending" })!.items).toEqual([]);
    expect(mergeApprovals(list(older), [decided], { state: "decided" })!.items.map((x) => x.id)).toEqual(["apr_a", "apr_o"]);
  });

  it("ignores an event older than what the list holds", () => {
    const newer = approval({ id: "apr_a", state: "approved", rev: 3 });
    const stale = approval({ id: "apr_a", rev: 1 });
    expect(mergeApprovals(list(newer), [stale], {})!.items[0]!.rev).toBe(3);
  });

  it("filters by project id, and asks for a refetch when the filter is a slug", () => {
    const other = approval({ id: "apr_x", projectId: "prj_other" });
    expect(mergeApprovals(list(), [other], { project: "prj_mine" })!.items).toEqual([]);
    expect(mergeApprovals(list(), [other], { project: "mine" })).toBeUndefined();
  });

  it("keeps the limit", () => {
    const items = [1, 2, 3].map((i) => approval({ id: `apr_${i}`, createdAt: `2026-09-30T0${i}:00:00Z` }));
    expect(mergeApprovals(list(), items, { limit: 2 })!.items).toHaveLength(2);
  });
});

describe("patchApprovals", () => {
  it("patches every cached list under its own filter", () => {
    const qc = new QueryClient();
    const pendingKey = approvalsListQueryKey({ query: PENDING_QUERY });
    const decidedKey = approvalsListQueryKey({ query: DECIDED_QUERY });
    qc.setQueryData(pendingKey, list(approval()));
    qc.setQueryData(decidedKey, list());
    patchApprovals(qc, [approval({ state: "denied", rev: 2, decidedAt: "2026-09-30T11:00:00Z" })]);
    expect(qc.getQueryData<ApprovalList>(pendingKey)!.items).toEqual([]);
    expect(qc.getQueryData<ApprovalList>(decidedKey)!.items.map((a) => a.state)).toEqual(["denied"]);
  });
});

describe("format", () => {
  const now = new Date("2026-09-30T10:00:00Z").getTime();
  it("counts down to expiry", () => {
    expect(countdown("2026-10-01T09:12:00Z", now)).toMatchObject({ label: "23 h 12 min", urgent: false });
    expect(countdown("2026-09-30T10:04:10Z", now)).toMatchObject({ label: "4 min 10 s", urgent: true });
    expect(countdown("2026-09-30T09:59:00Z", now)).toMatchObject({ label: "expired", expired: true });
  });
  it("labels requesters by kind", () => {
    expect(actorLabel({ kind: "automation", id: "crd_1", name: "ci" })).toBe("Automation · ci");
    expect(actorLabel({ kind: "agent", id: "crd_2", sessionId: "ses_0123456789abcdef" })).toBe("Agent · session ses_…abcdef");
    rememberSessions([session({ id: "ses_known", number: 5, driver: "opencode" })]);
    expect(actorLabel({ kind: "agent", id: "crd_2", sessionId: "ses_known" })).toBe("opencode · session 5");
    expect(actorLabel({ kind: "user", id: "usr_admin", name: "admin" })).toBe("admin");
  });
  it("describes estimates and decisions", () => {
    expect(estimateLine(approval())).toBeUndefined();
    expect(estimateLine(approval({ estimate: { gpuHours: 3, remainingGpuHours: 12 } }))).toBe("3.0 GPU-h · 12 GPU-h left today");
    expect(decisionLine(approval({ state: "approved", decision: { grant: "session" }, decidedBy: { kind: "user", id: "u", name: "admin" } }))).toBe("Approved for the session by admin");
    expect(decisionLine(approval({ state: "denied", decision: { grant: "once", expired: true } }))).toBe("Expired");
  });
});
