import { useEffect, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { NavArrowDown, NavArrowRight, OpenNewWindow, Restart, Xmark } from "iconoir-react";
import { artifactsGetOptions, pipelineRunsGetOptions, pipelineRunsGetQueryKey, pipelineRunsListOptions, stepKindsGetOptions } from "@/api/gen/@tanstack/react-query.gen";
import type { ArtifactRef, PipelineRun, PipelineStep } from "@/api/gen/types.gen";
import { Button } from "@/components/ui/button";
import { NativeSelect } from "@/components/ui/native-select";
import { cn } from "@/lib/utils";
import { ActorBadge, EmptyState, PanelToolbar, StatusChip } from "@/shell/entity/primitives";
import {
  errorMessage,
  focusJob,
  LogView,
  openDocument,
  openPanelById,
  runCommand,
  SchemaForm,
  useFocusedPipelineRun,
  useProject,
  useTopic,
  type PanelProps,
  type ParamSchema,
} from "@/shell/panel";
import { applyRunEvent, elapsedSeconds, formatBytes, pipelineFile, retryable, shortHash, sortSteps } from "./model";

// Pipeline run (docs/spec/11-ui-panels.md "Panel catalogue"): any pipeline run — its steps with status, attempts,
// inputs and outputs (artifacts.get with a small preview), departures from defaults, parameters rendered from the step
// kind's schema as a read-only form, and each step's log. Live on pipeline_run.{id}. Retry a failed step, cancel the
// run, open the pipeline file in Recipe. It follows the pipeline run focused elsewhere (Queue & GPU, later the Run
// document) until the person picks one here.

export function PipelineRunEmpty() {
  return <EmptyState step="run" title="No pipeline run yet" hint="Run a pipeline (pipelines.run) or start a training run; its steps show here as they run." />;
}

const DURATION = (s: number | undefined) => {
  if (s === undefined) return "";
  if (s < 60) return `${Math.round(s)} s`;
  if (s < 3600) return `${Math.floor(s / 60)} min ${Math.round(s % 60)} s`;
  return `${Math.floor(s / 3600)} h ${Math.round((s % 3600) / 60)} min`;
};

export function PipelineRunPanel(_props: PanelProps) {
  const project = useProject();
  const focused = useFocusedPipelineRun();
  const [picked, setPicked] = useState<string | null>(null);
  // A newly focused run (from Queue & GPU or a Run) replaces the person's pick.
  useEffect(() => setPicked(null), [focused]);
  const list = useQuery({ ...pipelineRunsListOptions({ path: { p: project ?? "" }, query: { limit: 50 } }), enabled: !!project });
  const qc = useQueryClient();
  useTopic(project ? ["pipeline_run.*"] : null, (batch) => {
    if (batch.some((e) => e.type !== "pipeline_run.step_changed")) void qc.invalidateQueries({ predicate: (q) => (q.queryKey[0] as { _id?: string } | undefined)?._id === "pipelineRunsList" });
  });
  const id = picked ?? focused ?? list.data?.items[0]?.id ?? null;
  const items = list.data?.items ?? [];
  if (!id) return list.isLoading ? <p className="p-3 text-xs text-muted-foreground">Loading…</p> : <PipelineRunEmpty />;
  return (
    <div className="flex h-full min-h-0 flex-col" data-slot="pipeline-run">
      <PanelToolbar>
        <label htmlFor="pipeline-run-pick" className="sr-only">
          Pipeline run
        </label>
        <NativeSelect id="pipeline-run-pick" className="h-6 w-auto max-w-full min-w-0 flex-1 text-xs" value={id} onChange={(e) => setPicked(e.target.value)}>
          {items.some((r) => r.id === id) ? null : <option value={id}>{id}</option>}
          {items.map((r) => (
            <option key={r.id} value={r.id}>
              {r.pipeline} · {r.state} · {new Date(r.createdAt).toLocaleString()}
            </option>
          ))}
        </NativeSelect>
      </PanelToolbar>
      <RunView key={id} id={id} />
    </div>
  );
}

function RunView({ id }: { id: string }) {
  const qc = useQueryClient();
  const key = pipelineRunsGetQueryKey({ path: { id } });
  const q = useQuery(pipelineRunsGetOptions({ path: { id } }));
  useTopic([`pipeline_run.${id}`], (batch) => {
    let refetch = false;
    for (const e of batch) {
      const cur = qc.getQueryData<PipelineRun>(key);
      if (!cur) {
        refetch = true;
        continue;
      }
      const next = applyRunEvent(cur, e.type, e.payload);
      if (next !== cur) qc.setQueryData(key, next);
    }
    if (refetch) void q.refetch();
  });
  const [open, setOpen] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [confirm, setConfirm] = useState(false);
  const run = q.data;
  if (q.error) return <p className="p-3 text-xs text-destructive">{errorMessage(q.error)}</p>;
  if (!run) return <p className="p-3 text-xs text-muted-foreground">Loading…</p>;
  const file = pipelineFile(run);
  const steps = sortSteps(run.steps);
  const cancel = async () => {
    setBusy(true);
    setError(null);
    try {
      const next = await runCommand("pipelineRuns.cancel", { run });
      qc.setQueryData(key, next);
    } catch (err) {
      setError(errorMessage(err));
    } finally {
      setBusy(false);
      setConfirm(false);
    }
  };
  return (
    <div className="min-h-0 flex-1 overflow-auto">
      <section aria-label="Run" className="flex flex-col gap-1.5 border-b p-2 text-xs" data-run={run.id} data-state={run.state}>
        <div className="flex flex-wrap items-center gap-x-2 gap-y-1">
          <StatusChip state={run.state} />
          <span className="text-[13px] font-medium">{run.pipeline}</span>
          <span className="rounded-full border px-1.5 text-[11px] text-muted-foreground">{run.source}</span>
          {run.commit ? <code className="text-[11px] text-muted-foreground">{run.commit.slice(0, 7)}</code> : null}
          <ActorBadge actor={run.actor} />
          <span className="ml-auto text-muted-foreground tabular-nums">{DURATION(elapsedSeconds(run.createdAt, run.finishedAt))}</span>
        </div>
        {run.error ? <p className="text-status-failed-foreground">{run.error}</p> : null}
        {run.inputs && Object.keys(run.inputs).length ? (
          <div className="flex flex-wrap items-center gap-1">
            <span className="text-muted-foreground">Inputs</span>
            {Object.entries(run.inputs).map(([name, ref]) => (
              <ArtifactChip key={name} name={name} artifact={ref} />
            ))}
          </div>
        ) : null}
        <div className="flex flex-wrap items-center gap-1">
          <Button
            size="xs"
            variant="outline"
            disabled={!file}
            title={file ? `Open ${file} in Recipe` : "A bundled template: the project repository has no file for it"}
            onClick={() => file && openDocument(`recipe:${file}`)}
          >
            <OpenNewWindow aria-hidden />
            Open pipeline file
          </Button>
          {run.runId ? (
            <Button size="xs" variant="outline" onClick={() => openDocument(`run:${run.runId}`)}>
              Open run
            </Button>
          ) : null}
          {run.state === "running" ? (
            <Button size="xs" variant={confirm ? "destructive" : "ghost"} disabled={busy} onClick={() => (confirm ? void cancel() : setConfirm(true))} data-command="pipelineRuns.cancel">
              <Xmark aria-hidden />
              {confirm ? "Confirm cancel" : "Cancel run"}
            </Button>
          ) : null}
        </div>
        {error ? (
          <p role="alert" className="text-destructive">
            {error}
          </p>
        ) : null}
      </section>
      <ol aria-label="Steps" className="flex flex-col">
        {steps.map((s) => (
          <StepRow
            key={s.id}
            run={run}
            step={s}
            open={open === s.id}
            onToggle={() => {
              const next = open === s.id ? null : s.id;
              setOpen(next);
              if (next && s.jobId) focusJob({ id: s.jobId, label: `${run.pipeline} · ${s.step}` });
            }}
          />
        ))}
      </ol>
    </div>
  );
}

function StepRow({ run, step, open, onToggle }: { run: PipelineRun; step: PipelineStep; open: boolean; onToggle: () => void }) {
  const qc = useQueryClient();
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const retry = async () => {
    setBusy(true);
    setError(null);
    try {
      const next = await runCommand("pipelineRuns.retry", { run, body: { step: step.step } });
      qc.setQueryData(pipelineRunsGetQueryKey({ path: { id: run.id } }), next);
    } catch (err) {
      setError(errorMessage(err));
    } finally {
      setBusy(false);
    }
  };
  const panelId = `step-${step.id}`;
  return (
    <li className="border-b" data-step={step.step} data-state={step.state}>
      <div className="flex min-h-9 flex-wrap items-center gap-x-2 gap-y-1 px-2 py-1 text-xs">
        <button type="button" className="flex min-h-6 min-w-0 items-center gap-1.5 text-left" aria-expanded={open} aria-controls={panelId} onClick={onToggle}>
          {open ? <NavArrowDown aria-hidden className="size-3.5 shrink-0" /> : <NavArrowRight aria-hidden className="size-3.5 shrink-0" />}
          <span className="font-medium">{step.step}</span>
          <span className="truncate text-muted-foreground">
            {step.kind}@{step.kindVersion}
          </span>
        </button>
        <StatusChip state={step.state === "reused" ? "done" : step.state} />
        {step.state === "reused" ? <span className="text-muted-foreground">reused</span> : null}
        {step.departures.length ? (
          <span className="rounded-full bg-accent-soft px-1.5 text-[11px] text-accent-text" data-slot="departures">
            {step.departures.length} departure{step.departures.length === 1 ? "" : "s"}
          </span>
        ) : null}
        {step.stepKindVersionId ? <Deprecated id={step.stepKindVersionId} /> : null}
        {step.attempts > 1 ? <span className="text-muted-foreground">attempt {step.attempts}</span> : null}
        <span className="ml-auto text-muted-foreground tabular-nums">{DURATION(elapsedSeconds(step.startedAt, step.finishedAt))}</span>
        {retryable(step) ? (
          <Button size="xs" variant="outline" disabled={busy || run.state === "running"} title={run.state === "running" ? "Wait until the run stops" : `Retry ${step.step} as a new attempt`} onClick={() => void retry()} data-command="pipelineRuns.retry">
            <Restart aria-hidden />
            Retry
          </Button>
        ) : null}
      </div>
      {step.error ? (
        <p className="px-2 pb-1 text-xs text-status-failed-foreground">
          {step.error.type}: {step.error.message}
        </p>
      ) : null}
      {error ? (
        <p role="alert" className="px-2 pb-1 text-xs text-destructive">
          {error}
        </p>
      ) : null}
      {open ? <StepDetails id={panelId} run={run} step={step} /> : null}
    </li>
  );
}

/** A step pinned to a kind version its pack deprecates (stepKinds.get deprecation): the warning, with what to pin instead. */
function Deprecated({ id }: { id: string }) {
  const kind = useQuery({ ...stepKindsGetOptions({ path: { id } }), staleTime: Infinity });
  const d = kind.data?.stepKind.deprecation;
  if (!d) return null;
  const text = `Deprecated: new pins are refused from ${d.after}${d.replacedBy ? `; pin ${d.replacedBy} instead` : ""}${d.note ? ` (${d.note})` : ""}`;
  return (
    <span className="rounded-full border px-1.5 text-[11px] text-status-warning-foreground" title={text} data-slot="deprecated">
      <span aria-hidden>⚠ </span>deprecated<span className="sr-only">. {text}</span>
    </span>
  );
}

function StepDetails({ id, run, step }: { id: string; run: PipelineRun; step: PipelineStep }) {
  const kind = useQuery({ ...stepKindsGetOptions({ path: { id: step.stepKindVersionId ?? "" } }), enabled: !!step.stepKindVersionId, staleTime: Infinity });
  const schema = kind.data?.stepKind.params as ParamSchema | undefined;
  const inputs = Object.entries(step.inputs ?? {});
  const outputs = Object.entries(step.outputs ?? {});
  return (
    <div id={id} className="flex flex-col gap-3 bg-tool px-3 py-2 text-xs">
      <div className="grid gap-2 sm:grid-cols-2">
        <div className="flex flex-col gap-1">
          <h4 className="text-[11px] font-medium tracking-wide text-muted-foreground uppercase">Inputs</h4>
          {inputs.length ? inputs.map(([n, ref]) => <ArtifactChip key={n} name={n} artifact={ref} />) : <span className="text-muted-foreground">{Object.keys(step.in).length ? `Wired: ${Object.entries(step.in).map(([k, v]) => `${k} ← ${v}`).join(", ")}` : "None"}</span>}
        </div>
        <div className="flex flex-col gap-1">
          <h4 className="text-[11px] font-medium tracking-wide text-muted-foreground uppercase">Outputs</h4>
          {outputs.length ? outputs.map(([n, ref]) => <ArtifactChip key={n} name={n} artifact={ref} />) : <span className="text-muted-foreground">{Object.entries(step.produces).map(([k, v]) => `${k} (${v})`).join(", ") || "None"} — not yet produced</span>}
        </div>
      </div>
      {step.departures.length ? (
        <div className="flex flex-col gap-1">
          <h4 className="text-[11px] font-medium tracking-wide text-muted-foreground uppercase">Departures from defaults</h4>
          <ul className="flex flex-col gap-0.5">
            {step.departures.map((d) => (
              <li key={d.param}>
                <code>{d.param}</code> = {JSON.stringify(d.value)}
                {d.default !== undefined ? <span className="text-muted-foreground"> (default {JSON.stringify(d.default)})</span> : null}
              </li>
            ))}
          </ul>
        </div>
      ) : null}
      <div className="flex flex-col gap-1">
        <h4 className="text-[11px] font-medium tracking-wide text-muted-foreground uppercase">Parameters</h4>
        {schema ? (
          <SchemaForm schema={schema} value={step.params} departures={step.departures.map((d) => d.param)} label={`Parameters of ${step.step}`} />
        ) : (
          <pre className="overflow-auto rounded border bg-background p-2 font-mono text-[11px]">{JSON.stringify(step.params, null, 2)}</pre>
        )}
      </div>
      {step.metrics && Object.keys(step.metrics).length ? (
        <div className="flex flex-col gap-1">
          <h4 className="text-[11px] font-medium tracking-wide text-muted-foreground uppercase">Metrics</h4>
          <dl className="grid grid-cols-[10rem_1fr] gap-x-2">
            {Object.entries(step.metrics).map(([k, v]) => (
              <div key={k} className="contents">
                <dt className="text-muted-foreground">{k}</dt>
                <dd className="tabular-nums">{v}</dd>
              </div>
            ))}
          </dl>
        </div>
      ) : null}
      {step.attemptLog.length ? (
        <div className="flex flex-col gap-1">
          <h4 className="text-[11px] font-medium tracking-wide text-muted-foreground uppercase">Attempts</h4>
          <table className="w-full text-xs" aria-label={`Attempts of ${step.step}`}>
            <thead className="text-left text-muted-foreground">
              <tr>
                <th className="font-normal">#</th>
                <th className="font-normal">Why</th>
                <th className="font-normal">State</th>
                <th className="font-normal">Batch</th>
                <th className="font-normal">Error</th>
              </tr>
            </thead>
            <tbody>
              {step.attemptLog.map((a) => (
                <tr key={a.attempt} className="border-t">
                  <td className="tabular-nums">{a.attempt}</td>
                  <td>{a.reason}</td>
                  <td>{a.state}</td>
                  <td className="tabular-nums">{a.batchScale !== undefined ? `${a.batchScale}×` : "1×"}</td>
                  <td className="text-status-failed-foreground">{a.error ? `${a.error.type}: ${a.error.message}` : ""}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      ) : null}
      {step.jobId ? (
        <div className="flex flex-col gap-1">
          <div className="flex items-center gap-2">
            <h4 className="text-[11px] font-medium tracking-wide text-muted-foreground uppercase">Log</h4>
            <Button
              size="xs"
              variant="link"
              className="h-5 px-1"
              onClick={() => {
                focusJob({ id: step.jobId!, label: `${run.pipeline} · ${step.step}` });
                openPanelById("logs");
              }}
            >
              Open in Logs
            </Button>
          </div>
          <div className="h-56 overflow-hidden rounded-md border bg-background">
            <LogView jobId={step.jobId} compact label={`Log of ${step.step}`} />
          </div>
        </div>
      ) : null}
    </div>
  );
}

function ArtifactChip({ name, artifact }: { name: string; artifact: ArtifactRef }) {
  const [open, setOpen] = useState(false);
  return (
    <span className="flex flex-col gap-1" data-artifact={artifact.hash}>
      <button
        type="button"
        className={cn("inline-flex min-h-6 w-fit items-center gap-1.5 rounded-md border bg-background px-1.5 text-left hover:bg-hover", open && "bg-selected")}
        aria-expanded={open}
        onClick={() => setOpen((o) => !o)}
        title={artifact.hash}
      >
        <span className="font-medium">{name}</span>
        <span className="text-muted-foreground">{artifact.type}</span>
        <code className="text-[11px] text-muted-foreground">{shortHash(artifact.hash)}</code>
        {artifact.size !== undefined ? <span className="text-muted-foreground tabular-nums">{formatBytes(artifact.size)}</span> : null}
      </button>
      {open ? <ArtifactPreview hash={artifact.hash} /> : null}
    </span>
  );
}

function ArtifactPreview({ hash }: { hash: string }) {
  const q = useQuery({ ...artifactsGetOptions({ path: { hash }, query: { content: true } }), staleTime: Infinity });
  const a = q.data;
  if (q.error) return <p className="text-destructive">{errorMessage(q.error)}</p>;
  if (!a) return <p className="text-muted-foreground">Loading…</p>;
  return (
    <div className="flex flex-col gap-1 rounded-md border bg-background p-2" data-slot="artifact-preview">
      <div className="flex flex-wrap gap-x-3 text-muted-foreground">
        <span>{a.type}</span>
        <span className="tabular-nums">{formatBytes(a.size)}</span>
        {a.producer ? (
          <span>
            from step {a.producer.step} ({a.producer.output})
          </span>
        ) : null}
        <span>{new Date(a.createdAt).toLocaleString()}</span>
      </div>
      {a.meta && Object.keys(a.meta).length ? <pre className="max-h-32 overflow-auto font-mono text-[11px]">{JSON.stringify(a.meta, null, 2)}</pre> : null}
      {a.directory ? (
        <ul className="max-h-32 overflow-auto font-mono text-[11px]">
          {(a.files ?? []).map((f) => (
            <li key={f.path}>
              {f.path} <span className="text-muted-foreground">{formatBytes(f.size)}</span>
            </li>
          ))}
        </ul>
      ) : a.content !== undefined && a.encoding === "utf8" ? (
        <pre className="max-h-40 overflow-auto font-mono text-[11px] whitespace-pre-wrap">{a.content.length > 4000 ? `${a.content.slice(0, 4000)}\n…` : a.content}</pre>
      ) : (
        <p className="text-muted-foreground">{a.contentOmitted ?? (a.encoding === "base64" ? "Binary content; not shown." : "No content preview.")}</p>
      )}
    </div>
  );
}
