import { useState, type ReactNode } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { mountsListOptions, storageGetOptions } from "@/api/gen/@tanstack/react-query.gen";
import type { ApprovalAccepted, JobAccepted, Mount, MountNew, StorageDataset } from "@/api/gen/types.gen";
import { Button } from "@/components/ui/button";
import { cn } from "@/lib/utils";
import { EmptyState, PanelToolbar, StatusChip } from "@/shell/entity/primitives";
import { errorMessage, focusJob, openPanelById, runCommand, useTopic, type PanelProps } from "@/shell/panel";
import { EMPTY_FORM, cacheLevel, datasetAction, formatBytes, healthLine, inventoryLine, mountBody, rootHint, type MountForm } from "./model";

// Storage (phase 4 · stream M; docs/help/panels/storage.md): the mounts with their health and last scan, the local
// cache against its water marks, each project's quota, and the dataset versions in the cache — pinned, evictable or
// evicted. Every button is one command; live on mount.{id}, entity.artifact.{hash} and storage.

export function StorageEmpty() {
  return <EmptyState step="prepare" title="No mounts yet" hint="A mount names where audio already lives — a local or network path, a bucket or a Hub repository. The admin approves each one." />;
}

const isJob = (r: unknown): r is JobAccepted => typeof r === "object" && r !== null && "jobId" in r;
const isApproval = (r: unknown): r is ApprovalAccepted => typeof r === "object" && r !== null && "approvalId" in r;

function Section({ title, children, action }: { title: string; children: ReactNode; action?: ReactNode }) {
  return (
    <section className="flex flex-col gap-2" aria-label={title}>
      <div className="flex items-center gap-2">
        <h3 className="text-xs font-semibold tracking-wide text-muted-foreground uppercase">{title}</h3>
        <div className="ml-auto flex gap-2">{action}</div>
      </div>
      {children}
    </section>
  );
}

function Grid({ label, head, children }: { label: string; head: string[]; children: ReactNode }) {
  return (
    <table className="w-full text-xs" aria-label={label}>
      <thead>
        <tr className="text-left text-muted-foreground">
          {head.map((h) => (
            <th key={h} className="px-1 py-1 font-medium">
              {h}
            </th>
          ))}
        </tr>
      </thead>
      <tbody>{children}</tbody>
    </table>
  );
}

const td = "border-t px-1 py-1.5 align-top";

export function StoragePanel(_props: PanelProps) {
  const qc = useQueryClient();
  const mounts = useQuery(mountsListOptions());
  const use = useQuery(storageGetOptions());
  const refresh = () => {
    void qc.invalidateQueries({ queryKey: mountsListOptions().queryKey });
    void qc.invalidateQueries({ queryKey: storageGetOptions().queryKey });
  };
  useTopic(["mount.*", "entity.artifact.*", "storage"], refresh);
  const [note, setNote] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [form, setForm] = useState<MountForm | null>(null);

  const act = async (label: string, run: () => Promise<unknown>) => {
    setError(null);
    setNote(null);
    try {
      const res = await run();
      if (isJob(res)) {
        focusJob({ id: res.jobId, label });
        setNote(`${label}: job ${res.jobId} queued.`);
      } else if (isApproval(res)) setNote(`${label}: waiting for approval ${res.approvalId}.`);
      refresh();
    } catch (err) {
      setError(errorMessage(err));
    }
  };

  const items = mounts.data?.items ?? [];
  const u = use.data;
  return (
    <div className="flex h-full min-h-0 flex-col" data-slot="storage">
      <PanelToolbar>
        <span className="text-xs font-medium">Storage</span>
        <Button size="xs" variant="outline" className="ml-auto" onClick={refresh}>
          Refresh
        </Button>
        <Button size="xs" data-command="mounts.new" onClick={() => setForm(form ? null : EMPTY_FORM)}>
          Add mount…
        </Button>
      </PanelToolbar>
      <div className="flex min-h-0 flex-1 flex-col gap-5 overflow-auto p-3">
        {note ? (
          <p role="status" className="text-xs text-muted-foreground">
            {note}{" "}
            {note.includes("approval") ? (
              <button type="button" className="text-primary underline-offset-2 hover:underline" onClick={() => openPanelById("approvals")}>
                Open Approvals
              </button>
            ) : null}
          </p>
        ) : null}
        {error ? (
          <p role="alert" className="text-xs text-destructive">
            {error}
          </p>
        ) : null}
        {form ? <AddMount form={form} onChange={setForm} onSubmit={(body) => void act("Add mount", () => runCommand("mounts.new", { body })).then(() => setForm(null))} /> : null}

        <Section title="Mounts">
          {items.length === 0 && !mounts.isLoading ? <StorageEmpty /> : null}
          {items.length > 0 ? (
            <Grid label="Mounts" head={["Mount", "Health", "Last scan", "Uses", ""]}>
              {items.map((m) => (
                <MountRow key={m.id} m={m} onScan={() => void act(`Rescan ${m.name}`, () => runCommand("mounts.scan", { mount: m }))}
                  onVerify={() => void act(`Check ${m.name}`, () => runCommand("mounts.verify", { mount: m }))} />
              ))}
            </Grid>
          ) : null}
        </Section>

        {u ? (
          <Section title="Cache">
            <div className="flex flex-col gap-1 rounded-md border p-3 text-xs" data-testid="cache-use">
              <div className="flex flex-wrap items-center gap-2">
                <span className={cn("font-medium tabular-nums", cacheLevel(u) === "over" && "text-status-failed-foreground")}>
                  {u.usedPct} % used · {formatBytes(u.freeBytes)} free of {formatBytes(u.totalBytes)}
                </span>
                <span className="text-muted-foreground">
                  evicts above {u.highWaterPct} % down to {u.lowWaterPct} % · datasets {formatBytes(u.datasetBytes)} · evictable {formatBytes(u.evictableBytes)}
                  {u.lastSweepAt ? ` · swept ${new Date(u.lastSweepAt).toLocaleTimeString()}` : ""}
                </span>
              </div>
              <div className="relative h-1.5 overflow-hidden rounded-full bg-muted" aria-hidden>
                <div className={cn("h-full", cacheLevel(u) === "over" ? "bg-status-failed" : cacheLevel(u) === "between" ? "bg-status-warning" : "bg-status-done")} style={{ width: `${Math.min(100, u.usedPct)}%` }} />
                <div className="absolute inset-y-0 w-px bg-foreground" style={{ left: `${u.highWaterPct}%` }} />
              </div>
            </div>
            {u.projects.length > 0 ? (
              <Grid label="Quotas" head={["Project", "Cached datasets", "Quota"]}>
                {u.projects.map((p) => (
                  <tr key={p.projectId} data-project={p.slug}>
                    <td className={td}>{p.slug}</td>
                    <td className={cn(td, "tabular-nums", p.over && "text-status-failed-foreground")}>{formatBytes(p.datasetBytes)}</td>
                    <td className={cn(td, "tabular-nums")}>{formatBytes(p.quotaBytes)}{p.over ? " · over" : ""}</td>
                  </tr>
                ))}
              </Grid>
            ) : null}
            {u.datasets.length > 0 ? (
              <Grid label="Dataset versions" head={["Dataset version", "State", "Size", "On a mount", "Pinned by", ""]}>
                {u.datasets.map((d) => (
                  <DatasetRow key={d.versionId} d={d} onRun={(command) => void act(`${command === "datasets.evict" ? "Evict" : "Materialize"} ${d.name}`, () => runCommand(command, { versionId: d.versionId }))} />
                ))}
              </Grid>
            ) : (
              <p className="text-xs text-muted-foreground">No dataset version has shards in the cache.</p>
            )}
          </Section>
        ) : null}
        {use.error ? (
          <p role="alert" className="text-xs text-destructive">
            {errorMessage(use.error)}
          </p>
        ) : null}
      </div>
    </div>
  );
}

function MountRow({ m, onScan, onVerify }: { m: Mount; onScan: () => void; onVerify: () => void }) {
  return (
    <tr data-mount={m.name}>
      <td className={td}>
        <div className="font-medium">{m.name}</div>
        <div className="font-mono text-[11px] text-muted-foreground" title={m.root}>
          {m.kind} · {m.root}
          {m.revision ? `@${m.revision.slice(0, 12)}` : ""} · {m.readOnly ? "read-only" : "writable"}
        </div>
      </td>
      <td className={td}>
        <StatusChip state={m.health.state} />
        <div className="text-muted-foreground">{healthLine(m)}</div>
      </td>
      <td className={cn(td, "text-muted-foreground")}>{inventoryLine(m)}</td>
      <td className={cn(td, "tabular-nums text-muted-foreground")}>
        {m.utterances.toLocaleString("en")} utterances · {m.copies.toLocaleString("en")} copies
      </td>
      <td className={cn(td, "whitespace-nowrap text-right")}>
        <Button size="xs" variant="outline" data-command="mounts.scan" onClick={onScan}>
          Rescan
        </Button>{" "}
        <Button size="xs" variant="outline" data-command="mounts.verify" onClick={onVerify}>
          Check health
        </Button>
      </td>
    </tr>
  );
}

function DatasetRow({ d, onRun }: { d: StorageDataset; onRun: (command: "datasets.evict" | "datasets.materialize") => void }) {
  const a = datasetAction(d);
  return (
    <tr data-dataset={d.versionId}>
      <td className={td}>
        <div className="font-medium">{d.name}</div>
        <div className="font-mono text-[11px] text-muted-foreground">{d.version}</div>
      </td>
      <td className={td}>
        <StatusChip state={d.state} />
      </td>
      <td className={cn(td, "tabular-nums")}>{formatBytes(d.bytes)}</td>
      <td className={cn(td, "tabular-nums")}>
        {d.copies} / {d.shards ?? 0} shards
      </td>
      <td className={cn(td, "text-muted-foreground")}>{d.pinned.length > 0 ? d.pinned.join("; ") : "—"}</td>
      <td className={cn(td, "text-right")}>
        <Button size="xs" variant="outline" data-command={a.command} disabled={a.enabled !== true} title={a.enabled === true ? undefined : a.enabled} onClick={() => onRun(a.command)}>
          {a.label}
        </Button>
      </td>
    </tr>
  );
}

const KINDS: MountNew["kind"][] = ["local", "nfs", "smb", "s3", "hf"];

function AddMount({ form, onChange, onSubmit }: { form: MountForm; onChange: (f: MountForm) => void; onSubmit: (body: MountNew) => void }) {
  const field = (key: keyof MountForm, label: string, hint?: string) => (
    <label className="flex flex-col gap-1 text-xs">
      <span className="font-medium">{label}</span>
      <input className="h-7 rounded-md border bg-background px-2 font-mono" value={String(form[key])} placeholder={hint} onChange={(e) => onChange({ ...form, [key]: e.target.value })} />
    </label>
  );
  return (
    <form
      className="grid grid-cols-2 gap-2 rounded-md border p-3"
      aria-label="Add mount"
      onSubmit={(e) => {
        e.preventDefault();
        onSubmit(mountBody(form));
      }}
    >
      {field("name", "Name", "corpora")}
      <label className="flex flex-col gap-1 text-xs">
        <span className="font-medium">Kind</span>
        <select className="h-7 rounded-md border bg-background px-2" value={form.kind} onChange={(e) => onChange({ ...form, kind: e.target.value as MountNew["kind"] })}>
          {KINDS.map((k) => (
            <option key={k} value={k}>
              {k}
            </option>
          ))}
        </select>
      </label>
      <div className="col-span-2">{field("root", "Root", rootHint(form.kind))}</div>
      {form.kind === "s3" ? field("endpoint", "Endpoint", "https://minio.example:9000") : null}
      {form.kind === "s3" || form.kind === "hf" ? field("credentials", "Credentials (secret name)", form.kind === "s3" ? "s3-corpora" : "hf-token (optional)") : null}
      {form.kind === "hf" ? field("revision", "Revision (commit SHA)", "40 hex digits") : null}
      {form.kind !== "hf" ? (
        <label className="col-span-2 flex items-center gap-2 text-xs">
          <input type="checkbox" checked={form.readOnly} onChange={(e) => onChange({ ...form, readOnly: e.target.checked })} />
          Read-only (exports need a writable mount)
        </label>
      ) : null}
      <div className="col-span-2 flex items-center gap-2">
        <span className="text-xs text-muted-foreground">Mounts are shared by every project: the admin approves each one.</span>
        <Button size="xs" type="submit" className="ml-auto">
          Ask for approval
        </Button>
      </div>
    </form>
  );
}
