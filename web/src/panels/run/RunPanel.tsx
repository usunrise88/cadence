import { useEffect, useRef, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { OpenNewWindow, Pause, Play, Xmark } from "iconoir-react";
import { checkpointsListOptions, eventsListOptions, runsGetQueryKey } from "@/api/gen/@tanstack/react-query.gen";
import type { Checkpoint, Run, RunEstimate, RunStageEntry } from "@/api/gen/types.gen";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { NativeSelect } from "@/components/ui/native-select";
import { cn } from "@/lib/utils";
import { ActorBadge, EmptyState, StatusChip } from "@/shell/entity/primitives";
import {
  errorMessage,
  focusJob,
  focusPipelineRun,
  openDocument,
  openPanelById,
  runCommand,
  runLabel,
  useEditRequest,
  useProject,
  useSelection,
  useTopic,
  type PanelProps,
} from "@/shell/panel";

// The Run document (docs/spec/11-ui-panels.md "Panel catalogue", Run): status and the stage timeline of the run's
// pipeline run, the config diff against the parent run, departures from defaults, final metrics, the best checkpoint
// and GPU-hours against the estimate. Pause, resume and stop act on the run's current step job (jobs.pause | resume |
// cancel). The header's actions (entity manifest) are "New stage from checkpoint", which opens the stage form here
// (an explicit peak LR, the estimate first, runs.stage), and "Resume from checkpoint" (runs.resume) for a failed or
// cancelled run.
// Live on run.{id}.status and run.{id}.checkpoints (the entity manifest re-reads runs.get).

export function RunEmpty() {
  return <EmptyState step="run" title="No run open" hint="Launch a run from a Mix, or open one from Metrics, Checkpoints or Queue & GPU." />;
}

export function RunPanel({ tab, entity, doc }: PanelProps) {
  const run = entity?.run as Run | undefined;
  if (!entity || !run) return <RunEmpty />;
  switch (tab) {
    case "details":
      return <Details run={run} />;
    case "activity":
      return <Activity runId={run.id} />;
    case "lineage":
      return <Lineage run={run} />;
    case "notes":
      return <EmptyState step="record" title="Notes live in NOTES.md" hint="Record what this run taught you as a project note (Project document, Notes)." />;
    default:
      return <Overview run={run} doc={doc} />;
  }
}

const n2 = (v: number | undefined, d = 2) => (v === undefined || !Number.isFinite(v) ? "—" : String(Math.round(v * 10 ** d) / 10 ** d));
const duration = (s: number | undefined) => (s === undefined ? "" : s < 60 ? `${Math.round(s)} s` : s < 3600 ? `${Math.round(s / 60)} min` : `${n2(s / 3600, 1)} h`);
const elapsed = (a?: string, b?: string) => (a ? ((b ? Date.parse(b) : Date.now()) - Date.parse(a)) / 1000 : undefined);
const show = (v: unknown) => (v === undefined ? "—" : typeof v === "object" ? JSON.stringify(v) : String(v));
const ENDED = ["done", "failed", "cancelled"];

function Section({ id, title, children, className }: { id: string; title: string; children: React.ReactNode; className?: string }) {
  return (
    <section aria-labelledby={id} className={cn("flex flex-col gap-1.5", className)}>
      <h3 id={id} className="text-[11px] font-medium tracking-wide text-muted-foreground uppercase">
        {title}
      </h3>
      {children}
    </section>
  );
}

function Overview({ run, doc }: { run: Run; doc?: string }) {
  const qc = useQueryClient();
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [confirmStop, setConfirmStop] = useState(false);
  const [stageOpen, setStageOpen] = useState(false);
  const [stageFrom, setStageFrom] = useState<string | undefined>(undefined);
  useEditRequest(doc, () => setStageOpen(true));
  // "New stage from here" in Checkpoints selects stage:<ckp_…> in this document.
  const wanted = useSelection((s) => (doc ? s.selections[doc] : undefined));
  useEffect(() => {
    if (!wanted?.startsWith("stage:")) return;
    setStageFrom(wanted.slice("stage:".length));
    setStageOpen(true);
  }, [wanted]);
  const refresh = () => void qc.invalidateQueries({ queryKey: runsGetQueryKey({ path: { id: run.id } }) });
  const act = async (fn: () => Promise<unknown>) => {
    setBusy(true);
    setError(null);
    try {
      const res = await fn();
      if (res && typeof res === "object" && "approvalId" in res) setError(`Waiting for an approval (${String((res as { approvalId: string }).approvalId)})`);
      refresh();
    } catch (err) {
      setError(errorMessage(err));
    } finally {
      setBusy(false);
      setConfirmStop(false);
    }
  };
  const job = run.currentJobId;
  const ended = ENDED.includes(run.status);
  const best = run.bestCheckpointId;
  return (
    <div className="flex flex-col gap-5 p-4 text-xs" data-run={run.id} data-status={run.status}>
      <div className="flex flex-wrap items-center gap-2">
        <StatusChip state={run.status} />
        {run.error ? <span className="text-status-failed-foreground">{run.error}</span> : null}
        <span className="text-muted-foreground tabular-nums">{duration(elapsed(run.createdAt, run.finishedAt))}</span>
        <span className="ml-auto flex flex-wrap gap-1">
          {!ended && run.status !== "paused" ? (
            <Button size="xs" variant="outline" disabled={busy || !job} title={job ? "Pause the current step (it saves its training state)" : "No step job holds the run yet"} onClick={() => job && void act(() => runCommand("jobs.pause", { jobId: job }))} data-command="jobs.pause">
              <Pause aria-hidden />
              Pause
            </Button>
          ) : null}
          {run.status === "paused" && job ? (
            <Button size="xs" variant="outline" disabled={busy} onClick={() => void act(() => runCommand("jobs.resume", { jobId: job }))} data-command="jobs.resume">
              <Play aria-hidden />
              Resume
            </Button>
          ) : null}
          {!ended ? (
            <Button
              size="xs"
              variant={confirmStop ? "destructive" : "ghost"}
              disabled={busy || !job}
              onClick={() => (confirmStop ? job && void act(() => runCommand("jobs.cancel", { jobId: job })) : setConfirmStop(true))}
              data-command="jobs.cancel"
            >
              <Xmark aria-hidden />
              {confirmStop ? "Confirm stop" : "Stop"}
            </Button>
          ) : null}
          <Button
            size="xs"
            variant="ghost"
            onClick={() => {
              focusPipelineRun(run.pipelineRunId);
              openPanelById("pipeline-run");
            }}
          >
            <OpenNewWindow aria-hidden />
            Pipeline run
          </Button>
        </span>
      </div>
      {error ? (
        <p role="alert" className="text-destructive">
          {error}
        </p>
      ) : null}
      {stageOpen ? <StageForm run={run} from={stageFrom ?? best} onClose={() => setStageOpen(false)} /> : null}

      <Section id={`run-timeline-${run.id}`} title="Stage timeline">
        <ol className="flex flex-col" aria-label="Stage timeline">
          {run.timeline.map((s) => (
            <TimelineRow key={s.step} s={s} label={`${runLabel(run)} · ${s.step}`} train={s.step === run.trainStep} />
          ))}
        </ol>
      </Section>

      <div className="grid gap-5 sm:grid-cols-2">
        <Section id={`run-metrics-${run.id}`} title="Final metrics">
          {Object.keys(run.finalMetrics).length ? (
            <dl className="grid grid-cols-[9rem_1fr] gap-x-2 gap-y-0.5" data-slot="final-metrics">
              {Object.entries(run.finalMetrics).map(([k, v]) => (
                <div key={k} className="contents">
                  <dt className="text-muted-foreground">{k}</dt>
                  <dd className="tabular-nums">{n2(v, 4)}</dd>
                </div>
              ))}
            </dl>
          ) : (
            <p className="text-muted-foreground">Final metrics arrive when the train step ends; Metrics shows them live.</p>
          )}
          <Button size="xs" variant="link" className="h-5 w-fit px-0" onClick={() => openPanelById("metrics")}>
            Open Metrics
          </Button>
        </Section>
        <Section id={`run-spend-${run.id}`} title="Spend and checkpoints">
          <dl className="grid grid-cols-[9rem_1fr] gap-x-2 gap-y-0.5">
            <dt className="text-muted-foreground">GPU-hours</dt>
            <dd className="tabular-nums" data-slot="gpu-hours">
              {n2(run.gpuHours)} used{run.estimate ? ` of ~${n2(run.estimate.gpuHours.value)} estimated (${n2(run.estimate.gpuHours.low)}–${n2(run.estimate.gpuHours.high)}, ${run.estimate.basis})` : ""}
            </dd>
            <dt className="text-muted-foreground">Checkpoints</dt>
            <dd className="tabular-nums">{run.checkpointCount}</dd>
            <dt className="text-muted-foreground">Best checkpoint</dt>
            <dd>
              {best ? (
                <button type="button" className="underline-offset-2 hover:underline" onClick={() => openPanelById("checkpoints")}>
                  {best}
                </button>
              ) : (
                "—"
              )}
            </dd>
          </dl>
        </Section>
      </div>

      {run.parentRunId ? (
        <Section id={`run-parent-${run.id}`} title="Against the parent run">
          <button type="button" className="w-fit underline-offset-2 hover:underline" onClick={() => openDocument(`run:${run.parentRunId}`)}>
            Parent {run.parentRunId}
          </button>
          {run.parentDiff?.length ? (
            <table className="w-full" aria-label="Config diff against the parent run" data-slot="parent-diff">
              <thead className="text-left text-muted-foreground">
                <tr>
                  <th className="font-normal">Parameter</th>
                  <th className="font-normal">This run</th>
                  <th className="font-normal">Parent</th>
                </tr>
              </thead>
              <tbody>
                {run.parentDiff.map((d) => (
                  <tr key={d.param} className="border-t">
                    <td className="py-0.5 font-mono">{d.param}</td>
                    <td className="rounded bg-diff-added px-1 text-diff-added-foreground tabular-nums">{show(d.value)}</td>
                    <td className="text-muted-foreground tabular-nums">{show(d.parentValue)}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          ) : (
            <p className="text-muted-foreground">Same train-step parameters as the parent.</p>
          )}
        </Section>
      ) : null}

      <Section id={`run-departures-${run.id}`} title="Departures from defaults">
        {run.departures.length ? (
          <table className="w-full" aria-label="Departures from defaults" data-slot="departures">
            <thead className="text-left text-muted-foreground">
              <tr>
                <th className="font-normal">Step</th>
                <th className="font-normal">Parameter</th>
                <th className="font-normal">Value</th>
                <th className="font-normal">Default</th>
              </tr>
            </thead>
            <tbody>
              {run.departures.map((d) => (
                <tr key={`${d.step}.${d.param}`} className="border-t">
                  <td className="py-0.5">{d.step}</td>
                  <td className="font-mono">{d.param}</td>
                  <td className="tabular-nums">{show(d.value)}</td>
                  <td className="text-muted-foreground tabular-nums">{show(d.default)}</td>
                </tr>
              ))}
            </tbody>
          </table>
        ) : (
          <p className="text-muted-foreground">Every parameter at its default.</p>
        )}
      </Section>
    </div>
  );
}

function TimelineRow({ s, label, train }: { s: RunStageEntry; label: string; train: boolean }) {
  return (
    <li className="flex min-h-8 flex-wrap items-center gap-x-2 gap-y-0.5 border-b py-1 last:border-0" data-stage={s.step} data-state={s.paused ? "paused" : s.state}>
      <StatusChip state={s.paused ? "paused" : s.state === "reused" ? "done" : s.state} />
      <span className="font-medium">{s.step}</span>
      <span className="text-muted-foreground">
        {s.kind}@{s.kindVersion}
      </span>
      {s.role ? <span className={cn("rounded-full border px-1.5 text-[11px]", train ? "border-accent-line text-accent-text" : "text-muted-foreground")}>{s.role}</span> : null}
      {s.attempts > 1 ? <span className="text-muted-foreground">attempt {s.attempts}</span> : null}
      {s.oomRetries ? <span className="text-status-warning-foreground">{s.oomRetries} OOM retr{s.oomRetries === 1 ? "y" : "ies"}</span> : null}
      {s.batchScale !== undefined && s.batchScale !== 1 ? <span className="text-status-warning-foreground">batch {s.batchScale}×</span> : null}
      {s.error ? <span className="text-status-failed-foreground">{s.error.type}: {s.error.message}</span> : null}
      <span className="ml-auto text-muted-foreground tabular-nums">{s.startedAt ? duration(elapsed(s.startedAt, s.finishedAt)) : s.estimateSeconds ? `est. ${duration(s.estimateSeconds)}` : ""}</span>
      {s.jobId ? (
        <Button
          size="xs"
          variant="ghost"
          onClick={() => {
            focusJob({ id: s.jobId!, label });
            openPanelById("logs");
          }}
        >
          Logs
        </Button>
      ) : null}
    </li>
  );
}

/** New stage from a checkpoint: an explicit peak LR (there is no default for it), the estimate first, then start. */
function StageForm({ run, from, onClose }: { run: Run; from?: string; onClose: () => void }) {
  const project = useProject();
  const list = useQuery({ ...checkpointsListOptions({ path: { p: project ?? "" }, query: { run: run.id } }), enabled: !!project });
  const items = list.data?.items ?? [];
  const [checkpoint, setCheckpoint] = useState<string | undefined>(from);
  useEffect(() => setCheckpoint(from), [from]);
  const [peakLr, setPeakLr] = useState("");
  const [steps, setSteps] = useState("");
  const [estimate, setEstimate] = useState<RunEstimate | null>(null);
  const [busy, setBusy] = useState(false);
  const [message, setMessage] = useState<{ error: boolean; text: string } | null>(null);
  const first = useRef<HTMLInputElement>(null);
  useEffect(() => first.current?.focus(), []);
  const lr = Number(peakLr);
  const valid = peakLr.trim() !== "" && lr > 0 && lr <= 1;
  const act = async (dryRun: boolean) => {
    setBusy(true);
    setMessage(null);
    try {
      const res = await runCommand("runs.stage", {
        run,
        dryRun,
        body: { peakLr: lr, ...(checkpoint ? { checkpoint } : {}), ...(Number(steps) > 0 ? { steps: Math.round(Number(steps)) } : {}) },
      });
      if (!res) return;
      if ("approvalId" in res) setMessage({ error: false, text: `The stage waits for an approval (${res.approvalId}).` });
      else if ("basis" in res) setEstimate(res);
      else {
        openDocument(`run:${res.id}`);
        onClose();
      }
    } catch (err) {
      setMessage({ error: true, text: errorMessage(err) });
    } finally {
      setBusy(false);
    }
  };
  const ck = (c: Checkpoint) => `${c.kind === "averaged" ? "averaged" : `step ${c.step ?? "?"}`} · val WER ${n2(c.valWer, 4)}${c.rank === 1 ? " · best" : c.kept ? " · kept" : ""}`;
  return (
    <form onSubmit={(e) => (e.preventDefault(), valid && void act(true))} className="flex flex-col gap-2 rounded-md border bg-tool p-3" aria-label="New stage from checkpoint" data-slot="stage-form">
      <div className="grid grid-cols-[7rem_1fr] items-center gap-2">
        <label htmlFor={`stage-ck-${run.id}`} className="text-muted-foreground">
          Checkpoint
        </label>
        <NativeSelect id={`stage-ck-${run.id}`} className="h-6 w-auto text-xs" value={checkpoint ?? ""} onChange={(e) => setCheckpoint(e.target.value || undefined)}>
          <option value="">The best kept checkpoint</option>
          {items.map((c) => (
            <option key={c.id} value={c.id}>
              {ck(c)}
            </option>
          ))}
        </NativeSelect>
        <label htmlFor={`stage-lr-${run.id}`} className="text-muted-foreground">
          Peak LR
        </label>
        <div className="flex flex-col gap-0.5">
          <Input ref={first} id={`stage-lr-${run.id}`} className="h-6 w-32 text-xs tabular-nums" placeholder="e.g. 0.0001" value={peakLr} onChange={(e) => setPeakLr(e.target.value)} aria-invalid={peakLr !== "" && !valid ? true : undefined} />
          <span className="text-muted-foreground">Required: a continuation needs its own peak learning rate (0–1, usually lower than the first stage's).</span>
        </div>
        <label htmlFor={`stage-steps-${run.id}`} className="text-muted-foreground">
          Steps
        </label>
        <Input id={`stage-steps-${run.id}`} type="number" min={1} className="h-6 w-32 text-xs tabular-nums" placeholder="default" value={steps} onChange={(e) => setSteps(e.target.value)} />
      </div>
      {estimate ? (
        <p className="tabular-nums" data-slot="stage-estimate">
          ~{n2(estimate.gpuHours.value)} GPU-h ({n2(estimate.gpuHours.low)}–{n2(estimate.gpuHours.high)}), {duration(estimate.durationSeconds.value)}, {estimate.steps} steps, {estimate.basis}
          {estimate.budget.withinDailyBudget ? "" : " — over today's budget"}
        </p>
      ) : null}
      <div className="flex gap-1">
        <Button type="submit" size="xs" variant="outline" disabled={busy || !valid}>
          Estimate
        </Button>
        <Button type="button" size="xs" disabled={busy || !valid || !estimate} onClick={() => void act(false)} data-command="runs.stage">
          Start stage
        </Button>
        <Button type="button" size="xs" variant="ghost" onClick={onClose}>
          Cancel
        </Button>
      </div>
      {message ? (
        <p role={message.error ? "alert" : "status"} className={message.error ? "text-destructive" : "text-muted-foreground"}>
          {message.text}
        </p>
      ) : null}
    </form>
  );
}

function Details({ run }: { run: Run }) {
  const rows: [string, React.ReactNode][] = [
    ["ID", <code className="font-mono text-[11px]">{run.id}</code>],
    ["Revision", `rev ${run.rev}`],
    ["Started by", <ActorBadge actor={run.actor} />],
    ["Created", new Date(run.createdAt).toLocaleString()],
    ["Init", run.init === "checkpoint" ? `checkpoint ${run.checkpointId ?? ""}` : "base model"],
    ["Base model", `${run.baseModel.name ?? ""} ${run.baseModel.version ?? run.baseModel.id}`],
    ["Family", run.family.name],
    ["Mix", `${run.mix.name} rev ${run.mix.revision}`],
    ["Recipe", `${run.recipe.pipeline} (${run.recipe.source}${run.recipe.commit ? ` @ ${run.recipe.commit.slice(0, 7)}` : ""})`],
    ["Train step", run.trainStep],
    ["Steps · seed · precision", `${run.steps} · ${run.seed ?? "—"} · ${run.precision}`],
    ["Runtime", run.runtime ? `${run.runtime.name ?? ""} ${run.runtime.digest ?? ""}` : "—"],
    ["Card", run.card ? `${run.card.host} #${run.card.index} · ${run.card.cardClass}, cap ${run.card.memoryCapGb} GB` : "—"],
    ["Resumed from", run.resumedFrom ?? "—"],
  ];
  return (
    <dl className="grid grid-cols-[10rem_1fr] gap-x-4 gap-y-2 p-4 text-xs">
      {rows.map(([k, v]) => (
        <div key={k} className="contents">
          <dt className="text-muted-foreground">{k}</dt>
          <dd className="min-w-0 break-words">{v}</dd>
        </div>
      ))}
    </dl>
  );
}

function Lineage({ run }: { run: Run }) {
  return (
    <ul className="flex flex-col gap-1.5 p-4 text-xs" aria-label="Lineage">
      <li>
        Base model <span className="font-medium">{run.baseModel.name ?? run.baseModel.id}</span>
      </li>
      <li>
        Mix{" "}
        <button type="button" className="font-medium underline-offset-2 hover:underline" onClick={() => openDocument(`mix:${run.mix.id}`)}>
          {run.mix.name} rev {run.mix.revision}
        </button>{" "}
        <code className="text-[11px] text-muted-foreground">{run.mix.hash.slice(0, 16)}…</code>
      </li>
      {run.parentRunId ? (
        <li>
          Parent run{" "}
          <button type="button" className="font-medium underline-offset-2 hover:underline" onClick={() => openDocument(`run:${run.parentRunId}`)}>
            {run.parentRunId}
          </button>{" "}
          (checkpoint {run.checkpointId})
        </li>
      ) : null}
      <li>
        Pipeline run{" "}
        <button
          type="button"
          className="font-medium underline-offset-2 hover:underline"
          onClick={() => {
            focusPipelineRun(run.pipelineRunId);
            openPanelById("pipeline-run");
          }}
        >
          {run.pipelineRunId}
        </button>
      </li>
    </ul>
  );
}

function Activity({ runId }: { runId: string }) {
  const topics = `run.${runId}.status,run.${runId}.checkpoints`;
  const q = useQuery(eventsListOptions({ query: { topics, limit: 100 } }));
  useTopic(topics.split(","), () => void q.refetch());
  const items = [...(q.data?.items ?? [])].reverse();
  if (items.length === 0) return <EmptyState step="record" title="No activity yet" />;
  return (
    <ol className="flex flex-col px-4 py-2 text-xs">
      {items.map((e) => (
        <li key={e.seq} className="flex h-8 items-center gap-3 border-b last:border-0">
          <time className="text-muted-foreground tabular-nums">{new Date(e.at).toLocaleString()}</time>
          <span className="font-medium">{e.type}</span>
          <ActorBadge actor={e.actor} toolCallId={e.causedBy?.toolCallId} />
          {e.entity ? <span className="ml-auto text-muted-foreground">rev {e.entity.rev}</span> : null}
        </li>
      ))}
    </ol>
  );
}
