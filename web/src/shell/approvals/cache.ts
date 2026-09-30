import { useQuery, type QueryClient } from "@tanstack/react-query";
import { approvalsGetQueryKey, approvalsListOptions } from "@/api/gen/@tanstack/react-query.gen";
import type { Approval, ApprovalList } from "@/api/gen/types.gen";

// Approvals in the query cache. Events on the `approvals` topic carry the whole approval, so every approvals.list
// query is patched in place (docs/spec/06-platform.md "Real-time model"); nothing is refetched.

export const PENDING_QUERY = { state: "pending", limit: 500 } as const;
export const DECIDED_QUERY = { state: "decided", limit: 50 } as const;

type ListFilter = { state?: "pending" | "approved" | "denied" | "decided"; project?: string; limit?: number };

function matchesState(a: Approval, state: ListFilter["state"]): boolean {
  if (!state) return true;
  if (state === "decided") return a.state !== "pending";
  return a.state === state;
}

/** API order: pending first (oldest first), then decided (newest decision first). */
export function compareApprovals(a: Approval, b: Approval): number {
  const ap = a.state === "pending";
  const bp = b.state === "pending";
  if (ap !== bp) return ap ? -1 : 1;
  if (ap) return a.createdAt.localeCompare(b.createdAt);
  return (b.decidedAt ?? b.createdAt).localeCompare(a.decidedAt ?? a.createdAt);
}

/**
 * Merges changed approvals into one list answer under its filter: a newer revision replaces the old one, an
 * approval that left the filter (a pending one decided) is removed, a new one that matches is inserted in order.
 * Returns undefined when the list cannot be patched (a project filter given as a slug) and must be refetched.
 */
export function mergeApprovals(list: ApprovalList, changed: Approval[], filter: ListFilter = {}): ApprovalList | undefined {
  if (filter.project && !filter.project.startsWith("prj_")) return undefined;
  const byId = new Map(list.items.map((a) => [a.id, a]));
  for (const a of changed) {
    const old = byId.get(a.id);
    if (old && old.rev > a.rev) continue; // an older event after a newer answer
    const fits = matchesState(a, filter.state) && (!filter.project || a.projectId === filter.project);
    if (fits) byId.set(a.id, a);
    else byId.delete(a.id);
  }
  const items = [...byId.values()].sort(compareApprovals);
  return { ...list, items: filter.limit ? items.slice(0, filter.limit) : items };
}

type ListKey = { _id?: string; query?: ListFilter };

/** Patches every cached approvals.list and approvals.get answer with the changed approvals. */
export function patchApprovals(qc: QueryClient, changed: Approval[]): void {
  if (changed.length === 0) return;
  const lists = qc.getQueryCache().findAll({ predicate: (q) => (q.queryKey[0] as ListKey | undefined)?._id === "approvalsList" });
  for (const q of lists) {
    const old = q.state.data as ApprovalList | undefined;
    if (!old) continue;
    const next = mergeApprovals(old, changed, (q.queryKey[0] as ListKey).query);
    if (next) qc.setQueryData(q.queryKey, next);
    else void qc.invalidateQueries({ queryKey: q.queryKey, exact: true });
  }
  for (const a of changed) {
    const key = approvalsGetQueryKey({ path: { id: a.id } });
    const old = qc.getQueryData<Approval>(key);
    if (old && old.rev <= a.rev) qc.setQueryData(key, a);
  }
}

/** Pending approvals of every project and the registry (the status bar badge and the Approvals panel). */
export function usePendingApprovals() {
  return useQuery({ ...approvalsListOptions({ query: PENDING_QUERY }), refetchInterval: 60_000 });
}

/** The most recent decisions, newest first. */
export function useDecidedApprovals(enabled = true) {
  return useQuery({ ...approvalsListOptions({ query: DECIDED_QUERY }), enabled });
}
