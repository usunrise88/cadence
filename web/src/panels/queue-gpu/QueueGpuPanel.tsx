import { useEffect, useMemo, useRef, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { NavArrowDown, NavArrowUp, Pause, Play, Xmark } from "iconoir-react";
import { computeListOptions, computeListQueryKey, projectsListOptions, queueEntriesListOptions } from "@/api/gen/@tanstack/react-query.gen";
import type { CardTelemetry, ComputeCard, ComputeHealth, ComputeHost, ComputeList, QueueEntry } from "@/api/gen/types.gen";
import { Button } from "@/components/ui/button";
import { cn } from "@/lib/utils";
import { EmptyState, PanelToolbar, StatusChip } from "@/shell/entity/primitives";
import { TimeSeriesChart, type TimeSeries } from "@/shell/charts";
import { errorMessage, focusJob, focusPipelineRun, formatWindows, openPanelById, runCommand, useProject, useTopic, type PanelProps } from "@/shell/panel";
import { appendTelemetry, cardKey, formatDuration, groupEntries, reorderPriority, seedTelemetry, splitMemory, trainingSlot, type Sample, type Telemetry } from "./model";

// Queue & GPU (docs/spec/11-ui-panels.md "Panel catalogue"): the step queue per card — waiting, paused, running and
// stopping jobs with priority, project and who holds the training slot — and each card's memory (resident services
// against Cadence) and utilisation, live on `queue`, `gpu` and `compute.{id}`. Reorder (jobs.edit priority), pause,
// resume and cancel are one command each. The list follows the current project; "All projects" is for Ops
// (docs/spec/02 "Projects": Library, Queue & GPU and Approvals filter to the current project).

export function QueueGpuEmpty() {
  return <EmptyState step="run" title="The queue is empty" hint="Step jobs wait here for a card; a training run or a pipeline run puts them in the queue." />;
}

type Scope = "project" | "all";

const mb = (v: number) => Math.round((v / 1024) * 10) / 10;

export function QueueGpuPanel(_props: PanelProps) {
  const project = useProject();
  const qc = useQueryClient();
  const [scope, setScope] = useState<Scope>(project ? "project" : "all");
  const filter = scope === "project" && project ? project : undefined;
  const queueOpts = queueEntriesListOptions(filter ? { query: { project: filter } } : undefined);
  const queue = useQuery(queueOpts);
  const compute = useQuery(computeListOptions());
  const projects = useQuery(projectsListOptions());
  const slugOf = useMemo(() => new Map((projects.data?.items ?? []).map((p) => [p.id, p.slug])), [projects.data]);
  // The training slot and the memory split are per card whatever the filter says: read the whole queue for them.
  const all = useQuery({ ...queueEntriesListOptions(), enabled: !!filter });
  const everything = filter ? all.data?.items : queue.data?.items;

  const groups = useMemo(() => groupEntries(queue.data?.items ?? []), [queue.data]);
  const leasedCards = useMemo(() => new Set((everything ?? []).flatMap((e) => (e.lease && e.lease.card >= 0 && e.state === "running" ? [cardKey(e.lease.host, e.lease.card)] : []))), [everything]);
  const leasedRef = useRef(leasedCards);
  useEffect(() => {
    leasedRef.current = leasedCards;
  }, [leasedCards]);

  const [telemetry, setTelemetry] = useState<Telemetry>({});
  const seeded = useRef(false);
  useEffect(() => {
    if (seeded.current || !compute.data) return;
    seeded.current = true;
    setTelemetry((cur) => ({ ...seedTelemetry(compute.data.items, (k) => leasedRef.current.has(k)), ...cur }));
  }, [compute.data]);

  useTopic(["queue", "gpu", "compute.*"], (batch) => {
    let stale = false;
    for (const e of batch) {
      if (e.type === "queue.changed") stale = true;
      else if (e.type === "gpu.telemetry") {
        const p = e.payload as { host?: string; cards?: CardTelemetry[]; at?: string } | undefined;
        if (!p?.host || !p.cards) continue;
        const t = p.at ? Date.parse(p.at) / 1000 : Date.now() / 1000;
        const host = p.host;
        const cards = p.cards;
        setTelemetry((cur) => appendTelemetry(cur, host, cards, t, (k) => leasedRef.current.has(k)));
      } else if (e.type === "compute.health") {
        const p = e.payload as { hostId?: string; health?: ComputeHealth } | undefined;
        if (!p?.hostId || !p.health) continue;
        const { hostId, health } = p;
        qc.setQueryData<ComputeList>(computeListQueryKey(), (old) => (old ? { ...old, items: old.items.map((h) => (h.id === hostId ? { ...h, health } : h)) } : old));
      }
    }
    if (stale) void qc.invalidateQueries({ predicate: (q) => (q.queryKey[0] as { _id?: string } | undefined)?._id === "queueEntriesList" });
  });

  const hosts = compute.data?.items ?? [];
  const nothing = !queue.isLoading && (queue.data?.items.length ?? 0) === 0;
  return (
    <div className="flex h-full min-h-0 flex-col" data-slot="queue-gpu">
      <PanelToolbar>
        <div role="radiogroup" aria-label="Jobs shown" className="inline-flex rounded-md border bg-background p-0.5 text-xs">
          {(
            [
              ["project", project ? `This project` : "This project (none open)"],
              ["all", "All projects"],
            ] as const
          ).map(([s, label]) => (
            <button
              key={s}
              type="button"
              role="radio"
              aria-checked={scope === s}
              disabled={s === "project" && !project}
              onClick={() => setScope(s)}
              className={cn("h-6 rounded-[4px] px-2.5 disabled:opacity-50", scope === s ? "bg-selected font-medium text-foreground" : "text-muted-foreground hover:text-foreground")}
            >
              {label}
            </button>
          ))}
        </div>
        <span className="ml-auto text-xs text-muted-foreground tabular-nums" data-slot="queue-count">
          {(queue.data?.items.length ?? 0).toString()} job{queue.data?.items.length === 1 ? "" : "s"}
        </span>
      </PanelToolbar>
      <div className="min-h-0 flex-1 overflow-auto">
        {queue.error ? (
          <p role="alert" className="p-3 text-xs text-destructive">
            {errorMessage(queue.error)}
          </p>
        ) : null}
        {hosts.flatMap((h) =>
          h.cards.map((c) => (
            <CardSection
              key={cardKey(h.name, c.index)}
              host={h}
              card={c}
              entries={groups.byCard.get(cardKey(h.name, c.index)) ?? []}
              holder={trainingSlot((everything ?? []).filter((e) => e.lease?.host === h.name && e.lease.card === c.index))}
              samples={telemetry[cardKey(h.name, c.index)] ?? []}
              slugOf={slugOf}
            />
          )),
        )}
        <section aria-labelledby="queue-waiting" className="flex flex-col gap-1 p-2">
          <h3 id="queue-waiting" className="text-[11px] font-medium tracking-wide text-muted-foreground uppercase">
            Waiting for a card · {groups.waiting.length}
          </h3>
          {groups.waiting.length === 0 ? <p className="text-xs text-muted-foreground">Nothing waiting.</p> : null}
          <ul className="flex flex-col gap-1" aria-label="Waiting jobs">
            {groups.waiting.map((e) => (
              <EntryRow key={e.jobId} e={e} slugOf={slugOf} queue={groups.waiting} />
            ))}
          </ul>
        </section>
        {groups.noCard.length > 0 ? (
          <section aria-labelledby="queue-nocard" className="flex flex-col gap-1 p-2">
            <h3 id="queue-nocard" className="text-[11px] font-medium tracking-wide text-muted-foreground uppercase">
              Running without a card · {groups.noCard.length}
            </h3>
            <ul className="flex flex-col gap-1">
              {groups.noCard.map((e) => (
                <EntryRow key={e.jobId} e={e} slugOf={slugOf} />
              ))}
            </ul>
          </section>
        ) : null}
        {nothing && hosts.length === 0 ? (
          <div className="h-56">
            <QueueGpuEmpty />
          </div>
        ) : null}
      </div>
    </div>
  );
}

function CardSection({ host, card, entries, holder, samples, slugOf }: { host: ComputeHost; card: ComputeCard; entries: QueueEntry[]; holder?: QueueEntry; samples: Sample[]; slugOf: Map<string, string> }) {
  const capMb = card.memoryCapGb * 1024;
  const split = splitMemory(samples, capMb);
  const totalMb = samples[samples.length - 1]?.totalMb || card.memoryGb * 1024;
  const windows = formatWindows(card.windows);
  const series = useMemo<TimeSeries[]>(() => {
    const x = { wallTime: samples.map((s) => s.t) };
    return [
      { id: "used", label: "Used, all processes", slot: 0, x, y: samples.map((s) => mb(s.usedMb)) },
      { id: "cadence", label: "Cadence (estimated)", slot: 1, x, y: cadenceSeries(samples, capMb) },
    ];
  }, [samples, capMb]);
  const util = useMemo<TimeSeries[]>(() => [{ id: "util", label: "Utilisation", slot: 2, x: { wallTime: samples.map((s) => s.t) }, y: samples.map((s) => (s.util === null ? null : Math.round(s.util * 1000) / 10)) }], [samples]);
  const label = `${host.name} · card ${card.index}`;
  return (
    <section aria-label={label} className="flex flex-col gap-2 border-b p-2" data-card={cardKey(host.name, card.index)}>
      <div className="flex flex-wrap items-center gap-x-2 gap-y-1 text-xs">
        <span className="text-[13px] font-medium">{card.name}</span>
        <span className="text-muted-foreground">
          {host.name} #{card.index} · {card.cardClass}
        </span>
        <StatusChip state={host.health.state === "healthy" ? "running" : host.health.state === "unknown" ? "planned" : "failed"} />
        <span className="text-muted-foreground" title={host.health.detail}>
          {host.health.state}
        </span>
        <span className="ml-auto" data-slot="training-slot">
          Training slot:{" "}
          {holder ? (
            <button type="button" className="font-medium underline-offset-2 hover:underline" onClick={() => focusEntry(holder, slugOf)}>
              {slugOf.get(holder.projectId ?? "") ?? holder.projectId ?? "a project"} · {holder.kind}
            </button>
          ) : (
            <span className="text-muted-foreground">free</span>
          )}
        </span>
      </div>
      <MemoryBar totalMb={totalMb} capMb={capMb} split={split} />
      {samples.length > 1 ? (
        <div className="grid gap-2 @container sm:grid-cols-2">
          <TimeSeriesChart title={`Memory of ${label}`} series={series} xKey="wallTime" yLabel="Memory" unit="GB" height={120} syncKey={`gpu-${cardKey(host.name, card.index)}`} envelope="off" />
          <TimeSeriesChart title={`Utilisation of ${label}`} series={util} xKey="wallTime" yLabel="Utilisation" unit="%" height={120} syncKey={`gpu-${cardKey(host.name, card.index)}`} envelope="off" />
        </div>
      ) : (
        <p className="text-xs text-muted-foreground">{samples.length ? "Telemetry charts start with the next report." : "No telemetry yet: a worker reports every few seconds while it runs."}</p>
      )}
      <div className="text-xs">
        <span className="text-muted-foreground">Availability: </span>
        {windows.length ? windows.join(" · ") : <span>any time</span>}
        <Button size="xs" variant="link" className="h-5 px-1" onClick={() => openPanelById("settings")}>
          Edit in Settings
        </Button>
      </div>
      {entries.length ? (
        <ul className="flex flex-col gap-1" aria-label={`Jobs on ${label}`}>
          {entries.map((e) => (
            <EntryRow key={e.jobId} e={e} slugOf={slugOf} />
          ))}
        </ul>
      ) : (
        <p className="text-xs text-muted-foreground">No job on this card.</p>
      )}
    </section>
  );
}

/** Cadence's share at every sample, by the same rule as splitMemory (the last idle reading is resident). */
function cadenceSeries(samples: Sample[], capMb: number): (number | null)[] {
  let idle: number | undefined;
  return samples.map((s) => {
    if (!s.cadence) {
      idle = s.usedMb;
      return 0;
    }
    const resident = idle !== undefined ? Math.min(idle, s.usedMb) : Math.max(0, s.usedMb - capMb);
    return mb(s.usedMb - resident);
  });
}

function MemoryBar({ totalMb, capMb, split }: { totalMb: number; capMb: number; split?: { residentMb: number; cadenceMb: number; estimated: boolean } }) {
  const pct = (v: number) => `${Math.min(100, Math.max(0, (v / (totalMb || 1)) * 100))}%`;
  return (
    <div className="flex flex-col gap-1 text-xs" data-slot="memory">
      <div className="relative h-3 overflow-hidden rounded-full bg-muted" role="img" aria-label={split ? `Resident services ${mb(split.residentMb)} GB, Cadence ${mb(split.cadenceMb)} GB of ${mb(totalMb)} GB; Cadence cap ${mb(capMb)} GB` : "No memory reading yet"}>
        {split ? (
          <>
            <span className="absolute inset-y-0 left-0 bg-muted-foreground/60" style={{ width: pct(split.residentMb) }} />
            <span className="absolute inset-y-0 bg-accent-line" style={{ left: pct(split.residentMb), width: pct(split.cadenceMb) }} />
          </>
        ) : null}
        <span aria-hidden className="absolute inset-y-0 w-px bg-foreground" style={{ left: pct(totalMb - capMb) }} title="Cadence's cap starts here" />
      </div>
      <div className="flex flex-wrap gap-x-3 text-muted-foreground tabular-nums">
        {split ? (
          <>
            <span>
              <span aria-hidden className="mr-1 inline-block size-2 rounded-full bg-muted-foreground/60" />
              Resident services {mb(split.residentMb)} GB
            </span>
            <span>
              <span aria-hidden className="mr-1 inline-block size-2 rounded-full bg-accent-line" />
              Cadence {mb(split.cadenceMb)} GB{split.estimated ? " (estimated)" : ""}
            </span>
          </>
        ) : null}
        <span>
          Cap {mb(capMb)} of {mb(totalMb)} GB
        </span>
      </div>
    </div>
  );
}

function focusEntry(e: QueueEntry, slugOf: Map<string, string>) {
  focusJob({ id: e.jobId, label: `${e.kind}@${e.kindVersion}${e.projectId ? ` · ${slugOf.get(e.projectId) ?? e.projectId}` : ""}` });
  if (e.pipelineRunId) focusPipelineRun(e.pipelineRunId);
}

const STATE_CHIP: Record<QueueEntry["state"], string> = { running: "running", stopping: "paused", waiting: "queued", paused: "paused" };

function EntryRow({ e, slugOf, queue }: { e: QueueEntry; slugOf: Map<string, string>; queue?: QueueEntry[] }) {
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [confirm, setConfirm] = useState(false);
  useEffect(() => {
    if (!confirm) return;
    const t = setTimeout(() => setConfirm(false), 4000);
    return () => clearTimeout(t);
  }, [confirm]);
  const act = async (fn: () => Promise<unknown>) => {
    setBusy(true);
    setError(null);
    try {
      await fn();
    } catch (err) {
      setError(errorMessage(err));
    } finally {
      setBusy(false);
    }
  };
  const up = queue ? reorderPriority(queue, e.jobId, -1) : undefined;
  const down = queue ? reorderPriority(queue, e.jobId, 1) : undefined;
  const slug = e.projectId ? (slugOf.get(e.projectId) ?? e.projectId) : undefined;
  const progress = e.lease?.progress;
  return (
    <li className="flex flex-col gap-1 rounded-md border bg-background p-2 text-xs" data-job={e.jobId} data-state={e.state}>
      <div className="flex min-w-0 flex-wrap items-center gap-x-2 gap-y-1">
        <StatusChip state={STATE_CHIP[e.state]} />
        {e.state === "stopping" ? <span className="text-status-warning-foreground">stopping{e.lease?.stopReason ? ` (${e.lease.stopReason})` : ""}</span> : null}
        <button type="button" className="min-w-0 truncate font-medium underline-offset-2 hover:underline" onClick={() => focusEntry(e, slugOf)} title="Show its log and pipeline run">
          {e.kind}@{e.kindVersion}
        </button>
        <span className="rounded-full border px-1.5 text-[11px] text-muted-foreground">{e.jobKind}</span>
        {slug ? <span className="text-muted-foreground">{slug}</span> : null}
        <span className="text-muted-foreground tabular-nums" title="Higher starts first" data-slot="priority">
          priority {e.priority}
        </span>
        {e.attempt > 1 ? <span className="text-muted-foreground">attempt {e.attempt}</span> : null}
        <span className="ml-auto text-muted-foreground tabular-nums">
          {e.state === "running" && progress !== undefined ? `${Math.round(progress * 100)} %` : `est. ${formatDuration(e.estimateSeconds)}`}
        </span>
      </div>
      {e.lease?.message ? <p className="truncate text-muted-foreground">{e.lease.message}</p> : null}
      <div className="flex flex-wrap items-center gap-1">
        {queue ? (
          <>
            <Button size="icon-xs" variant="ghost" aria-label={`Move ${e.kind} up`} disabled={busy || up === undefined} onClick={() => up !== undefined && void act(() => runCommand("jobs.edit", { jobId: e.jobId, priority: up }))} data-command="jobs.edit">
              <NavArrowUp aria-hidden />
            </Button>
            <Button size="icon-xs" variant="ghost" aria-label={`Move ${e.kind} down`} disabled={busy || down === undefined} onClick={() => down !== undefined && void act(() => runCommand("jobs.edit", { jobId: e.jobId, priority: down }))} data-command="jobs.edit">
              <NavArrowDown aria-hidden />
            </Button>
          </>
        ) : null}
        {e.state === "paused" ? (
          <Button size="xs" variant="ghost" disabled={busy} onClick={() => void act(() => runCommand("jobs.resume", { jobId: e.jobId }))} data-command="jobs.resume">
            <Play aria-hidden />
            Resume
          </Button>
        ) : e.state === "running" || e.state === "waiting" ? (
          <Button size="xs" variant="ghost" disabled={busy} onClick={() => void act(() => runCommand("jobs.pause", { jobId: e.jobId }))} data-command="jobs.pause">
            <Pause aria-hidden />
            Pause
          </Button>
        ) : null}
        {e.state !== "stopping" ? (
          <Button
            size="xs"
            variant={confirm ? "destructive" : "ghost"}
            disabled={busy}
            onClick={() => (confirm ? void act(() => runCommand("jobs.cancel", { jobId: e.jobId })).then(() => setConfirm(false)) : setConfirm(true))}
            data-command="jobs.cancel"
          >
            <Xmark aria-hidden />
            {confirm ? "Confirm cancel" : "Cancel"}
          </Button>
        ) : null}
        <Button
          size="xs"
          variant="ghost"
          className="ml-auto"
          onClick={() => {
            focusEntry(e, slugOf);
            openPanelById("logs");
          }}
        >
          Logs
        </Button>
        {e.pipelineRunId ? (
          <Button
            size="xs"
            variant="ghost"
            onClick={() => {
              focusEntry(e, slugOf);
              openPanelById("pipeline-run");
            }}
          >
            Pipeline run
          </Button>
        ) : null}
      </div>
      {error ? (
        <p role="alert" className="text-destructive">
          {error}
        </p>
      ) : null}
    </li>
  );
}
