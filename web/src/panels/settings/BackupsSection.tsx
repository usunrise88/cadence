import { useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { backupsListOptions, backupsListQueryKey } from "@/api/gen/@tanstack/react-query.gen";
import type { Backup, RestoreTest } from "@/api/gen/types.gen";
import { Button } from "@/components/ui/button";
import { cn } from "@/lib/utils";
import { errorMessage, runCommand, useTopic } from "@/shell/panel";
import { Chip, SectionHeading, Table, Td, when } from "./ui";

// Backups (docs/spec/06-platform.md "Operations": nightly pg_dump and content-store sync, a weekly restore into a
// scratch database with a report; 24 h RPO, 1 h RTO): the schedule and retention, "Back up now", the last restore
// test's report and the sets with their own restore tests.

/** 1536 → "1.5 KB". */
export function formatBytes(n: number | undefined): string {
  if (n === undefined) return "—";
  const units = ["B", "KB", "MB", "GB", "TB"];
  let v = n;
  let i = 0;
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024;
    i++;
  }
  return `${i === 0 ? v : v.toFixed(1)} ${units[i]}`;
}

export function stateTone(state: string): string {
  if (state === "succeeded" || state === "passed") return "text-status-done-foreground";
  if (state === "failed") return "text-status-failed-foreground";
  return "text-status-warning-foreground";
}

/** Whether a set can be restore-tested now. */
export function canRestore(b: Backup): boolean {
  return b.state === "succeeded" && !b.prunedAt && b.restoreTest?.state !== "running";
}

export function BackupsSection() {
  const qc = useQueryClient();
  const { data, isLoading } = useQuery(backupsListOptions());
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [status, setStatus] = useState<string | null>(null);
  useTopic(["backups"], () => void qc.invalidateQueries({ queryKey: backupsListQueryKey() }));
  const run = async (what: () => Promise<{ jobId: string }>, label: string) => {
    setBusy(true);
    setError(null);
    setStatus(null);
    try {
      const { jobId } = await what();
      setStatus(`${label} (job ${jobId}).`);
      void qc.invalidateQueries({ queryKey: backupsListQueryKey() });
    } catch (err) {
      setError(errorMessage(err));
    } finally {
      setBusy(false);
    }
  };
  const sc = data?.schedule;
  const items = data?.items ?? [];
  const last = data?.lastRestoreTest;
  return (
    <section aria-labelledby="settings-backups" className="flex flex-col gap-3">
      <SectionHeading id="settings-backups" title="Backups" hint="Nightly pg_dump plus the content store, and a weekly restore into a scratch database that proves the set works.">
        <Button size="xs" disabled={busy} data-command="backups.new" onClick={() => void run(() => runCommand("backups.new", undefined), "Backup queued")}>
          Back up now
        </Button>
      </SectionHeading>
      {sc ? (
        <dl className="grid gap-x-4 gap-y-1 rounded-md border p-3 text-xs @md:grid-cols-2" aria-label="Backup schedule">
          <div className="flex gap-1">
            <dt className="text-muted-foreground">Nightly at</dt>
            <dd>
              {sc.nightlyAt} {sc.timezone} · next {when(sc.nextBackupAt)}
            </dd>
          </div>
          <div className="flex gap-1">
            <dt className="text-muted-foreground">Restore test</dt>
            <dd>
              {sc.restoreTestWeekday}s at {sc.restoreTestAt} · next {when(sc.nextRestoreTestAt)}
            </dd>
          </div>
          <div className="flex gap-1">
            <dt className="text-muted-foreground">Kept</dt>
            <dd>
              {sc.keepNightly} nightly + {sc.keepWeekly} weekly sets
            </dd>
          </div>
          <div className="flex min-w-0 gap-1">
            <dt className="text-muted-foreground">Directory</dt>
            <dd className="truncate font-mono" title={sc.directory}>
              {sc.directory}
            </dd>
          </div>
        </dl>
      ) : null}
      {status ? (
        <p role="status" className="text-xs text-muted-foreground">
          {status}
        </p>
      ) : null}
      {error ? (
        <p role="alert" className="text-xs text-destructive">
          {error}
        </p>
      ) : null}
      {last ? <RestoreReport backupId={last.backupId} report={last.report} /> : sc ? <p className="text-xs text-muted-foreground">No restore test yet.</p> : null}
      {isLoading ? <p className="text-xs text-muted-foreground">Loading…</p> : null}
      {!isLoading && items.length === 0 ? <p className="text-xs text-muted-foreground">No backup sets yet.</p> : null}
      {items.length > 0 ? (
        <Table label="Backup sets" head={["Taken", "Trigger", "State", "Dump", "New blobs", "Restore test", ""]}>
          {items.map((b) => (
            <tr key={b.id} data-backup={b.id}>
              <Td className="tabular-nums" title={b.id}>
                {when(b.createdAt)}
              </Td>
              <Td>{b.trigger}</Td>
              <Td className={stateTone(b.state)} title={b.error}>
                {b.state}
                {b.prunedAt ? <span className="ml-1 text-muted-foreground">· files removed</span> : null}
              </Td>
              <Td className="tabular-nums">{formatBytes(b.dumpBytes)}</Td>
              <Td className="tabular-nums">{b.casCopied !== undefined ? `${b.casCopied} (${formatBytes(b.casBytesCopied)})` : "—"}</Td>
              <Td className={cn(b.restoreTest && stateTone(b.restoreTest.state))} title={b.restoreTest?.error}>
                {b.restoreTest ? `${b.restoreTest.state} · ${when(b.restoreTest.startedAt)}` : "—"}
              </Td>
              <Td>
                <Button
                  size="xs"
                  variant="outline"
                  disabled={busy || !canRestore(b)}
                  data-command="backups.verify"
                  onClick={() => void run(() => runCommand("backups.verify", { backup: b }), "Restore test queued")}
                >
                  Restore test
                </Button>
              </Td>
            </tr>
          ))}
        </Table>
      ) : null}
    </section>
  );
}

export function RestoreReport({ backupId, report }: { backupId: string; report: RestoreTest }) {
  const mismatched = (report.tables ?? []).filter((t) => t.backedUp !== t.restored);
  return (
    <div className="flex flex-col gap-2 rounded-md border p-3" role="group" aria-label="Last restore test">
      <div className="flex flex-wrap items-center gap-1.5 text-xs">
        <h4 className="mr-1 font-semibold">Last restore test</h4>
        <Chip tone={report.state === "passed" ? "accent" : report.state === "failed" ? "warning" : "neutral"}>{report.state}</Chip>
        <span className="text-muted-foreground">
          {when(report.startedAt)}
          {report.durationMs !== undefined ? ` · ${(report.durationMs / 1000).toFixed(1)} s` : ""} · set <span className="font-mono">{backupId}</span>
        </span>
      </div>
      <p className="text-xs text-muted-foreground">
        {report.migrationVersion !== undefined ? `Migration ${report.migrationVersion}` : "Migration —"} · {report.tables?.length ?? 0} tables compared
        {mismatched.length > 0 ? `, ${mismatched.length} differ` : ", all equal"} · {report.casChecked ?? 0} content-store blobs re-hashed
      </p>
      {report.error ? (
        <p role="alert" className="text-xs text-destructive">
          {report.error}
        </p>
      ) : null}
      {report.tables && report.tables.length > 0 ? (
        <Table label="Restored row counts" head={["Table", "At backup", "Restored"]}>
          {report.tables.map((t) => (
            <tr key={t.name}>
              <Td className="font-mono">{t.name}</Td>
              <Td className="tabular-nums">{t.backedUp}</Td>
              <Td className={cn("tabular-nums", t.restored !== t.backedUp && "text-status-failed-foreground")}>{t.restored}</Td>
            </tr>
          ))}
        </Table>
      ) : null}
    </div>
  );
}
