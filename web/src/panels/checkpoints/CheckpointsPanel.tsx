import { useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { checkpointsListOptions, runsGetOptions } from "@/api/gen/@tanstack/react-query.gen";
import type { Checkpoint } from "@/api/gen/types.gen";
import { Button } from "@/components/ui/button";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import { cn } from "@/lib/utils";
import { EmptyState, PanelToolbar } from "@/shell/entity/primitives";
import { errorMessage, EvalForm, focusPipelineRun, openDocument, openPanelById, runCommand, runLabel, useActiveRun, useProject, useSelection, useTopic, type PanelProps } from "@/shell/panel";

// Checkpoints (docs/spec/11-ui-panels.md "Panel catalogue"): the checkpoints of the active run with validation WER,
// ranked, the run's top k marked kept, averaged ones marked with what they were made from. Average the selected ones
// (checkpoints.average), start a new stage from one (the Run document's stage form, runs.stage), evaluate one
// (the shared Run eval form: evals.new's axes, the plan first, then the Eval report opens); export waits for phase 5.
// Live on run.{id}.checkpoints.

export function CheckpointsEmpty() {
  return <EmptyState step="review" title="No run yet" hint="Checkpoints of the active run appear here as the train step saves them." />;
}

const wer = (v: number | undefined) => (v === undefined ? "—" : `${Math.round(v * 10000) / 100} %`);

/** Checkpoints best first: rank, then validation WER, then step. */
export function orderCheckpoints(items: Checkpoint[]): Checkpoint[] {
  return [...items].sort(
    (a, b) => (a.rank ?? Infinity) - (b.rank ?? Infinity) || (a.valWer ?? Infinity) - (b.valWer ?? Infinity) || (b.step ?? 0) - (a.step ?? 0),
  );
}

function Later({ label, reason }: { label: string; reason: string }) {
  return (
    <Tooltip>
      <TooltipTrigger render={<span tabIndex={0} className="inline-flex rounded-md" aria-label={`${label}: ${reason}`} />}>
        <Button size="xs" variant="ghost" disabled>
          {label}
        </Button>
      </TooltipTrigger>
      <TooltipContent>{reason}</TooltipContent>
    </Tooltip>
  );
}

export function CheckpointsPanel({ instanceId }: PanelProps) {
  const project = useProject();
  const { runId } = useActiveRun(instanceId, project);
  if (!runId || !project) return <CheckpointsEmpty />;
  return <RunCheckpoints key={runId} project={project} runId={runId} />;
}

function RunCheckpoints({ project, runId }: { project: string; runId: string }) {
  const qc = useQueryClient();
  const opts = checkpointsListOptions({ path: { p: project }, query: { run: runId } });
  const list = useQuery(opts);
  const run = useQuery(runsGetOptions({ path: { id: runId } }));
  useTopic([`run.${runId}.checkpoints`], () => void qc.invalidateQueries({ queryKey: opts.queryKey }));
  const select = useSelection((s) => s.select);
  const [chosen, setChosen] = useState<string[]>([]);
  const [evaluating, setEvaluating] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [message, setMessage] = useState<{ error: boolean; text: string } | null>(null);
  const items = orderCheckpoints(list.data?.items ?? []);
  const toggle = (id: string) => setChosen((c) => (c.includes(id) ? c.filter((x) => x !== id) : [...c, id]));
  const average = async () => {
    setBusy(true);
    setMessage(null);
    try {
      const res = await runCommand("checkpoints.average", { runId, body: { checkpoints: chosen } });
      if ("approvalId" in res) setMessage({ error: false, text: `Averaging waits for an approval (${res.approvalId}).` });
      else {
        setChosen([]);
        setMessage({ error: false, text: `Averaging ${res.checkpoints.length} checkpoints (${res.step}); the result joins this list when it is saved.` });
        if (res.pipelineRun) focusPipelineRun(res.pipelineRun.id);
      }
    } catch (err) {
      setMessage({ error: true, text: errorMessage(err) });
    } finally {
      setBusy(false);
    }
  };
  const stageFrom = (c: Checkpoint) => {
    const doc = `run:${runId}`;
    select(doc, `stage:${c.id}`);
    openDocument(doc);
  };
  return (
    <div className="flex h-full min-h-0 flex-col" data-slot="checkpoints">
      <PanelToolbar>
        <button type="button" className="min-w-0 truncate text-[13px] font-medium underline-offset-2 hover:underline" onClick={() => openDocument(`run:${runId}`)}>
          {run.data ? runLabel(run.data) : runId}
        </button>
        {list.data ? <span className="text-xs text-muted-foreground">top {list.data.keepTopK} kept</span> : null}
        <Button size="xs" className="ml-auto" disabled={busy || chosen.length < 2} onClick={() => void average()} title={chosen.length < 2 ? "Select two or more checkpoints" : undefined} data-command="checkpoints.average">
          Average selected{chosen.length ? ` (${chosen.length})` : ""}
        </Button>
      </PanelToolbar>
      {message ? (
        <p role={message.error ? "alert" : "status"} className={cn("px-2 pt-1 text-xs", message.error ? "text-destructive" : "text-muted-foreground")}>
          {message.text}
        </p>
      ) : null}
      <div className="min-h-0 flex-1 overflow-auto">
        {list.isLoading ? <p className="p-3 text-xs text-muted-foreground">Loading…</p> : null}
        {list.error ? <p className="p-3 text-xs text-destructive">{errorMessage(list.error)}</p> : null}
        {!list.isLoading && items.length === 0 ? (
          <div className="h-48">
            <EmptyState step="run" title="No checkpoint yet" hint="The train step saves checkpoints as it validates; they appear here with their validation WER." />
          </div>
        ) : null}
        <ul className="flex flex-col gap-1 p-2" aria-label="Checkpoints">
          {items.map((c) => (
            <li key={c.id} className={cn("flex flex-col gap-1 rounded-md border bg-background p-2 text-xs", c.rank === 1 && "border-accent-line")} data-checkpoint={c.id} data-kept={c.kept || undefined}>
              <div className="flex flex-wrap items-center gap-x-2 gap-y-1">
                <input type="checkbox" className="size-4 accent-primary" checked={chosen.includes(c.id)} onChange={() => toggle(c.id)} aria-label={`Select ${c.kind === "averaged" ? "averaged checkpoint" : `checkpoint at step ${c.step}`}`} />
                <span className="font-medium">{c.kind === "averaged" ? "Averaged" : `Step ${c.step ?? "?"}`}</span>
                <span className="tabular-nums" data-slot="val-wer">
                  val WER {wer(c.valWer)}
                </span>
                {c.rank === 1 ? <span className="rounded-full bg-accent-soft px-1.5 text-[11px] text-accent-text">best</span> : null}
                {c.kept ? <span className="rounded-full border px-1.5 text-[11px]">top {c.rank ?? ""} · kept</span> : <span className="text-[11px] text-muted-foreground">not kept</span>}
                {c.averagedFrom?.length ? <span className="text-muted-foreground">of {c.averagedFrom.length} checkpoints</span> : null}
                <span className="ml-auto text-muted-foreground tabular-nums">{new Date(c.createdAt).toLocaleString()}</span>
              </div>
              <div className="flex flex-wrap items-center gap-1">
                <Button size="xs" variant="outline" onClick={() => stageFrom(c)} data-command="runs.stage">
                  New stage from here
                </Button>
                <Button size="xs" variant="outline" aria-expanded={evaluating === c.id} onClick={() => setEvaluating(evaluating === c.id ? null : c.id)} data-command="evals.new">
                  Evaluate
                </Button>
                <Later label="Export" reason="Export arrives in phase 5" />
                <Button
                  size="xs"
                  variant="ghost"
                  className="ml-auto"
                  onClick={() => {
                    if (c.pipelineRunId) focusPipelineRun(c.pipelineRunId);
                    openPanelById("pipeline-run");
                  }}
                  disabled={!c.pipelineRunId}
                >
                  From step
                </Button>
              </div>
              {evaluating === c.id ? (
                <EvalForm
                  project={project}
                  subject={{ checkpointId: c.id }}
                  subjectLabel={c.kind === "averaged" ? "the averaged checkpoint" : `the checkpoint at step ${c.step ?? "?"}`}
                  family={c.family}
                  runId={runId}
                  planOnOpen
                  onClose={() => setEvaluating(null)}
                />
              ) : null}
            </li>
          ))}
        </ul>
      </div>
    </div>
  );
}
