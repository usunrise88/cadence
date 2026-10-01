import { useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import type { ApprovalAccepted, EvictionPlan } from "@/api/gen/types.gen";
import { Button } from "@/components/ui/button";
import { cn } from "@/lib/utils";
import { errorMessage, openPanelById, runCommand, useTopic } from "@/shell/panel";
import { formatBytes } from "./BackupsSection";
import { SectionHeading, Table, Td, when } from "./ui";

// Content store (docs/spec/06-platform.md "Artifacts, metrics and logs", Retention): how full the store's disk is,
// and the superseded training states artifacts.evict would delete. The list is the command's dry run; "Evict" sends
// the real call, which always waits for an approval (for people too) — decided in Approvals or from Telegram.

const PLAN_KEY = ["settings", "storage", "evictionPlan"] as const;

/** The share of the disk free, 0–1; undefined without a reading. */
export function freeShare(plan: EvictionPlan | undefined): number | undefined {
  const d = plan?.disk;
  return d && d.totalBytes > 0 ? d.freeBytes / d.totalBytes : undefined;
}

export function StorageSection() {
  const qc = useQueryClient();
  const { data, isLoading, error: loadError } = useQuery({
    queryKey: PLAN_KEY,
    queryFn: () => runCommand("artifacts.evict", { dryRun: true }) as Promise<EvictionPlan>,
    staleTime: 30_000,
  });
  useTopic(["storage", "entity.artifact.*"], () => void qc.invalidateQueries({ queryKey: PLAN_KEY }));
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [approval, setApproval] = useState<string | null>(null);
  const evict = async () => {
    setBusy(true);
    setError(null);
    try {
      const res = (await runCommand("artifacts.evict", {})) as ApprovalAccepted;
      setApproval(res.approvalId);
    } catch (err) {
      setError(errorMessage(err));
    } finally {
      setBusy(false);
    }
  };
  const disk = data?.disk;
  const share = freeShare(data);
  const low = share !== undefined && disk !== undefined && share < disk.lowFreeFraction;
  const items = data?.artifacts ?? [];
  return (
    <section aria-labelledby="settings-storage" className="flex flex-col gap-3">
      <SectionHeading
        id="settings-storage"
        title="Content store"
        hint="Artifacts are kept until a person evicts them. Superseded training states (used only to resume) are the large part; evicting them always asks for an approval."
      >
        <Button size="xs" variant="outline" disabled={isLoading} onClick={() => void qc.invalidateQueries({ queryKey: PLAN_KEY })}>
          Refresh
        </Button>
      </SectionHeading>
      {disk ? (
        <div className="flex flex-col gap-1 rounded-md border p-3 text-xs" data-testid="store-disk">
          <div className="flex items-center gap-2">
            <span className={cn("font-medium tabular-nums", low && "text-status-failed-foreground")}>
              {formatBytes(disk.freeBytes)} free of {formatBytes(disk.totalBytes)}
            </span>
            <span className="text-muted-foreground tabular-nums">({Math.round((share ?? 0) * 100)} %)</span>
            {low ? <span className="text-status-failed-foreground">· below {Math.round(disk.lowFreeFraction * 100)} %: Cadence warns once a day</span> : null}
          </div>
          <div className="h-1.5 overflow-hidden rounded-full bg-muted" aria-hidden>
            <div className={cn("h-full", low ? "bg-status-failed" : "bg-status-done")} style={{ width: `${Math.round((1 - (share ?? 0)) * 100)}%` }} />
          </div>
        </div>
      ) : null}
      {isLoading ? <p className="text-xs text-muted-foreground">Loading…</p> : null}
      {loadError ? (
        <p role="alert" className="text-xs text-destructive">
          {errorMessage(loadError)}
        </p>
      ) : null}
      {data ? (
        <div className="flex flex-wrap items-center gap-2 text-xs">
          <span>
            {items.length === 0 ? "Nothing to evict." : `${items.length} training state${items.length === 1 ? "" : "s"} can be evicted, freeing ${formatBytes(data.bytesFreed)}.`}
          </span>
          {data.permanent && items.length > 0 ? <span className="text-status-warning-foreground">No backup mirror is configured: an eviction cannot be undone.</span> : null}
          <Button size="xs" className="ml-auto" disabled={busy || items.length === 0} data-command="artifacts.evict" onClick={() => void evict()}>
            Evict {items.length || ""}…
          </Button>
        </div>
      ) : null}
      {approval ? (
        <p role="status" className="text-xs text-muted-foreground">
          Waiting for approval {approval}.{" "}
          <button type="button" className="text-primary underline-offset-2 hover:underline" onClick={() => openPanelById("approvals")}>
            Open Approvals
          </button>
        </p>
      ) : null}
      {error ? (
        <p role="alert" className="text-xs text-destructive">
          {error}
        </p>
      ) : null}
      {items.length > 0 ? (
        <Table label="Evictable training states" head={["Recorded", "Run", "Size", "Why"]}>
          {items.map((a) => (
            <tr key={a.hash} data-artifact={a.hash}>
              <Td className="tabular-nums" title={a.hash}>
                {when(a.createdAt)}
              </Td>
              <Td className="font-mono">{a.runId ?? a.pipelineRunId ?? "—"}</Td>
              <Td className="tabular-nums">{formatBytes(a.size)}</Td>
              <Td className="text-muted-foreground">{a.reason ?? ""}</Td>
            </tr>
          ))}
        </Table>
      ) : null}
      {data && data.kept.length > 0 ? (
        <p className="text-xs text-muted-foreground">
          {data.kept.length} kept: {[...new Set(data.kept.map((k) => k.reason))].join("; ")}.
        </p>
      ) : null}
    </section>
  );
}
