import { useMemo, useRef, useState } from "react";
import { useQueries, useQuery, useQueryClient } from "@tanstack/react-query";
import { Pin, Xmark } from "iconoir-react";
import { metricsGetOptions, metricsGetQueryKey, runsGetOptions } from "@/api/gen/@tanstack/react-query.gen";
import type { MetricAxis, MetricSeriesSet } from "@/api/gen/types.gen";
import { Button } from "@/components/ui/button";
import { NativeSelect } from "@/components/ui/native-select";
import { cn } from "@/lib/utils";
import { TimeSeriesChart } from "@/shell/charts";
import { EmptyState, PanelToolbar } from "@/shell/entity/primitives";
import { errorMessage, openDocument, runLabel, useActiveRun, useProject, useTopic, type PanelProps } from "@/shell/panel";
import { afterStepFor, appendDelta, AXES, chartGroups, checkpointMarks, minEventStep, seriesFor, xKeyOf } from "./model";

// Metrics (docs/spec/11-ui-panels.md "Panel catalogue"; R53): loss, validation WER, LR, gradient norm, throughput and
// GPU memory of the active run by step, epoch, wall time or GPU-hours, with checkpoint marks; pinned runs overlay.
// The series come binned from metrics.get (the same numbers an agent reads); live points on run.{id}.metrics are
// appended with afterStep. Each chart has its table view, CSV copy and keyboard cursor (the chart primitive).

export function MetricsEmpty() {
  return <EmptyState step="run" title="No run yet" hint="Launch a run from a Mix; its loss and validation WER draw here live." />;
}

const MAX_POINTS = 1000;

export function MetricsPanel({ instanceId }: PanelProps) {
  const project = useProject();
  const { runId } = useActiveRun(instanceId, project);
  const [axis, setAxis] = useState<MetricAxis>("step");
  const [smoothing, setSmoothing] = useState(0.6);
  const [log, setLog] = useState(false);
  const [pinned, setPinned] = useState<string[]>([]);
  if (!runId && pinned.length === 0) return <MetricsEmpty />;
  const overlay = pinned.filter((id) => id !== runId);
  return (
    <div className="flex h-full min-h-0 flex-col" data-slot="metrics">
      <PanelToolbar className="h-auto min-h-9 flex-wrap gap-y-1 py-1">
        {runId ? <RunTitle runId={runId} /> : null}
        {runId ? (
          <Button size="xs" variant="ghost" onClick={() => setPinned((p) => (p.includes(runId) ? p : [...p, runId]))} disabled={pinned.includes(runId)} title="Keep this run on the charts when another run is active">
            <Pin aria-hidden />
            Pin
          </Button>
        ) : null}
        <label className="ml-auto flex items-center gap-1 text-xs">
          <span className="text-muted-foreground">x</span>
          <NativeSelect className="h-6 w-auto text-xs" value={axis} onChange={(e) => setAxis(e.target.value as MetricAxis)} aria-label="x axis">
            {AXES.map((a) => (
              <option key={a.id} value={a.id}>
                {a.label}
              </option>
            ))}
          </NativeSelect>
        </label>
        <label className="flex items-center gap-1 text-xs">
          <span className="text-muted-foreground">Smoothing</span>
          <input type="range" min={0} max={0.95} step={0.05} value={smoothing} onChange={(e) => setSmoothing(Number(e.target.value))} className="h-6 w-20 accent-primary" aria-label="Smoothing" aria-valuetext={String(smoothing)} />
          <span className="w-8 tabular-nums">{smoothing.toFixed(2)}</span>
        </label>
        <Button size="xs" variant={log ? "secondary" : "ghost"} aria-pressed={log} onClick={() => setLog((l) => !l)}>
          Log scale
        </Button>
      </PanelToolbar>
      {overlay.length ? (
        <div className="flex flex-wrap items-center gap-1 border-b px-2 py-1 text-xs" aria-label="Pinned runs">
          <span className="text-muted-foreground">Pinned</span>
          {overlay.map((id) => (
            <span key={id} className="inline-flex items-center gap-1 rounded-full border px-2">
              <PinnedLabel runId={id} />
              <button type="button" aria-label={`Unpin ${id}`} className="inline-flex size-6 items-center justify-center" onClick={() => setPinned((p) => p.filter((x) => x !== id))}>
                <Xmark aria-hidden className="size-3" />
              </button>
            </span>
          ))}
        </div>
      ) : null}
      <Charts runId={runId} overlay={overlay} axis={axis} smoothing={smoothing} log={log} />
    </div>
  );
}

function RunTitle({ runId }: { runId: string }) {
  const q = useQuery(runsGetOptions({ path: { id: runId } }));
  return (
    <button type="button" className="min-w-0 truncate text-[13px] font-medium underline-offset-2 hover:underline" onClick={() => openDocument(`run:${runId}`)} title="Open the run">
      {q.data ? runLabel(q.data) : runId}
    </button>
  );
}

function PinnedLabel({ runId }: { runId: string }) {
  const q = useQuery(runsGetOptions({ path: { id: runId } }));
  return <span>{q.data ? runLabel(q.data) : runId}</span>;
}

function Charts({ runId, overlay, axis, smoothing, log }: { runId: string | undefined; overlay: string[]; axis: MetricAxis; smoothing: number; log: boolean }) {
  const qc = useQueryClient();
  const ids = useMemo(() => [...(runId ? [runId] : []), ...overlay], [runId, overlay]);
  const opts = (id: string) => metricsGetOptions({ path: { id }, query: { x: axis, maxPoints: MAX_POINTS } });
  const sets = useQueries({ queries: ids.map((id) => ({ ...opts(id), staleTime: Infinity })) });
  const labels = useQueries({ queries: ids.map((id) => ({ ...runsGetOptions({ path: { id } }), staleTime: 30_000 })) });

  // Live append for the active run: at most one delta read per second, batched events in between.
  const pending = useRef<ReturnType<typeof setTimeout> | null>(null);
  const earliest = useRef<number | undefined>(undefined);
  useTopic(runId ? [`run.${runId}.metrics`, `run.${runId}.checkpoints`] : null, (batch) => {
    earliest.current = minEventStep(batch.map((e) => e.payload), earliest.current);
    if (!runId || pending.current) return;
    pending.current = setTimeout(() => {
      pending.current = null;
      const from = earliest.current;
      earliest.current = undefined;
      const key = metricsGetQueryKey({ path: { id: runId }, query: { x: axis, maxPoints: MAX_POINTS } });
      const cur = qc.getQueryData<MetricSeriesSet>(key);
      if (!cur || cur.lastStep === undefined) {
        void qc.invalidateQueries({ queryKey: key });
        return;
      }
      void qc
        .fetchQuery({ ...metricsGetOptions({ path: { id: runId }, query: { x: axis, maxPoints: MAX_POINTS, afterStep: afterStepFor(cur, from) } }), staleTime: 0 })
        .then((delta) => {
          const next = appendDelta(qc.getQueryData<MetricSeriesSet>(key) ?? cur, delta);
          if (next) qc.setQueryData(key, next);
          else void qc.invalidateQueries({ queryKey: key });
        })
        .catch(() => void qc.invalidateQueries({ queryKey: key }));
    }, 1000);
  });

  const runs = ids.map((id, i) => ({ runId: id, label: labels[i]?.data ? runLabel(labels[i]!.data!) : id.slice(4, 12), slot: i, set: sets[i]?.data }));
  const main = runId ? sets[0] : undefined;
  const names = new Set(runs.flatMap((r) => r.set?.series.filter((s) => s.points.length).map((s) => s.name) ?? []));
  const groups = chartGroups(names);
  const marks = checkpointMarks(main?.data);
  if (main?.error) return <p className="p-3 text-xs text-destructive">{errorMessage(main.error)}</p>;
  if (sets.some((s) => s.isLoading)) return <p className="p-3 text-xs text-muted-foreground">Loading…</p>;
  if (groups.length === 0) return <EmptyState step="run" title="No metrics yet" hint="Points arrive once the train step starts reporting; they draw here live." />;
  return (
    <div className="min-h-0 flex-1 overflow-auto p-2" data-slot="metric-charts">
      <div className={cn("grid gap-3", groups.length > 1 && "lg:grid-cols-2")}>
        {groups.map((g) => (
          <div key={g.key} data-chart={g.key}>
            <TimeSeriesChart
              title={g.title}
              series={seriesFor(g, runs)}
              xKey={xKeyOf(axis)}
              yLabel={g.title}
              unit={g.unit}
              yScale={log || g.log ? "log" : "linear"}
              smoothing={smoothing}
              markers={marks}
              syncKey={`metrics-${runId ?? "pinned"}`}
              height={180}
            />
          </div>
        ))}
      </div>
    </div>
  );
}
