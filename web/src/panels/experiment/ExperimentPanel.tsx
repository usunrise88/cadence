import { useMemo, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { BookmarkBook, Plus, StatsReport } from "iconoir-react";
import { eventsListOptions, experimentsGetQueryKey, mixesListOptions } from "@/api/gen/@tanstack/react-query.gen";
import type { EvalPlan, Experiment, ExperimentRun, ModelRegistration, Sweep } from "@/api/gen/types.gen";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { NativeSelect } from "@/components/ui/native-select";
import { Textarea } from "@/components/ui/textarea";
import { cn } from "@/lib/utils";
import { AnalyticsChart } from "@/shell/charts";
import { ActorBadge, EmptyState, StatusChip } from "@/shell/entity/primitives";
import { errorMessage, openDocument, runCommand, useEditRequest, useProject, useTopic, type PanelProps } from "@/shell/panel";
import { numericParams, parallelSpec, scatterSpec, shortRun, showValue } from "./model";
import { SweepForm } from "./SweepForm";

// The Experiment document (docs/spec/11-ui-panels.md "Panel catalogue", Experiment; R53): the question, the fixed mix
// revision and base model, the sweeps with their GPU-hours against the cap, and the comparison — parameters × metrics
// of the experiment's runs with departures from defaults highlighted and the best run by validation WER. Compare N
// narrows the table and both charts (parameter against WER scatter, parallel coordinates over a sweep's parameters)
// to the chosen runs. Actions: New sweep (sweeps.run, dry run first), Evaluate best (evals.new on the best run's best
// checkpoint), Register best (models.register; needs a passed gate). Live on entity.experiment.{id} (the entity
// manifest re-reads experiments.get).

/** The header's Register best asks the document through this request prefix (the command registry's REGISTER_REQUEST). */
const REGISTER_REQUEST = "register:";

export function ExperimentEmpty() {
  return (
    <EmptyState
      step="prepare"
      title="No experiment open"
      hint="An experiment asks one question on a fixed mix revision and base model; its runs and sweeps answer it."
      action={<NewExperimentForm />}
    />
  );
}

export function ExperimentPanel({ tab, entity, doc }: PanelProps) {
  const e = entity?.experiment as Experiment | undefined;
  if (!entity || !e) return <ExperimentEmpty />;
  switch (tab) {
    case "details":
      return <Details e={e} />;
    case "activity":
      return <Activity id={e.id} />;
    case "lineage":
      return <EmptyState step="record" title="Lineage of the runs" hint="Open a run of the experiment for its mix, recipe and checkpoints." />;
    case "notes":
      return <EmptyState step="record" title="Notes live in NOTES.md" hint="Record the answer to the experiment's question as a project note (Project document, Notes)." />;
    default:
      return <Overview e={e} doc={doc} />;
  }
}

const n = (v: number | undefined, d = 2) => (v === undefined || !Number.isFinite(v) ? "—" : String(Math.round(v * 10 ** d) / 10 ** d));

function Section({ id, title, children, className, actions }: { id: string; title: string; children: React.ReactNode; className?: string; actions?: React.ReactNode }) {
  return (
    <section aria-labelledby={id} className={cn("flex flex-col gap-1.5", className)}>
      <div className="flex min-h-6 items-center gap-2">
        <h3 id={id} className="text-[11px] font-medium tracking-wide text-muted-foreground uppercase">
          {title}
        </h3>
        {actions ? <span className="ml-auto flex flex-wrap gap-1">{actions}</span> : null}
      </div>
      {children}
    </section>
  );
}

function Overview({ e, doc }: { e: Experiment; doc?: string }) {
  const qc = useQueryClient();
  const project = useProject();
  const [sweepOpen, setSweepOpen] = useState(false);
  const [registerOpen, setRegisterOpen] = useState(false);
  const [evalOpen, setEvalOpen] = useState(false);
  useEditRequest(doc, () => setSweepOpen(true));
  useEditRequest(doc ? `${REGISTER_REQUEST}${doc}` : undefined, () => setRegisterOpen(true));
  const refresh = () => void qc.invalidateQueries({ queryKey: experimentsGetQueryKey({ path: { id: e.id } }) });
  const best = e.best;
  const running = e.sweeps.some((s) => s.state === "running");
  return (
    <div className="flex flex-col gap-5 p-4 text-xs" data-experiment={e.id}>
      <p className="text-[13px] leading-relaxed" data-slot="question">
        {e.question}
      </p>
      <dl className="grid grid-cols-[8rem_1fr] gap-x-2 gap-y-0.5">
        <dt className="text-muted-foreground">Mix</dt>
        <dd>
          <button type="button" className="underline-offset-2 hover:underline" onClick={() => openDocument(`mix:${e.mix.id}`)}>
            {e.mix.name} rev {e.mix.revision}
          </button>
          {e.mix.replayShare !== undefined ? <span className="text-muted-foreground"> · replay share {showValue(e.mix.replayShare)}</span> : null}
        </dd>
        <dt className="text-muted-foreground">Base model</dt>
        <dd>
          {e.baseModel.name} <span className="text-muted-foreground">{e.baseModel.version}</span>
        </dd>
        <dt className="text-muted-foreground">Best run</dt>
        <dd data-slot="best">
          {best ? (
            <>
              <button type="button" className="font-mono underline-offset-2 hover:underline" onClick={() => openDocument(`run:${best.runId}`)}>
                {shortRun(best.runId)}
              </button>{" "}
              ◆ val WER {n(best.valWer, 4)} · checkpoint {best.checkpointId}
              {best.eval ? ` · eval ${best.eval.evalId} ${best.eval.verdict ?? best.eval.status}` : ""}
            </>
          ) : (
            <span className="text-muted-foreground">No run has a validated checkpoint yet</span>
          )}
        </dd>
      </dl>
      <div className="flex flex-wrap gap-1">
        <Button size="xs" variant="outline" disabled={running} title={running ? "A sweep of this experiment is running" : undefined} onClick={() => setSweepOpen(true)} data-command="sweeps.run">
          <Plus aria-hidden />
          New sweep
        </Button>
        <Button size="xs" variant="outline" disabled={!best || !project} title={best ? undefined : "No best checkpoint yet"} onClick={() => setEvalOpen(true)} data-command="evals.new">
          <StatsReport aria-hidden />
          Evaluate best
        </Button>
        <Button size="xs" variant="outline" disabled={!best?.registrable || !project} title={best?.registrable ? undefined : (best?.reason ?? "No best checkpoint yet")} onClick={() => setRegisterOpen(true)} data-command="models.register">
          <BookmarkBook aria-hidden />
          Register best
        </Button>
      </div>
      {sweepOpen ? <SweepForm experiment={e} onClose={() => setSweepOpen(false)} onStarted={refresh} /> : null}
      {evalOpen && best && project ? <EvaluateBest project={project} checkpointId={best.checkpointId} onClose={() => setEvalOpen(false)} onDone={refresh} /> : null}
      {registerOpen && best && project ? <RegisterBest project={project} checkpointId={best.checkpointId} reason={best.registrable ? undefined : best.reason} onClose={() => setRegisterOpen(false)} onDone={refresh} /> : null}

      <Section id={`exp-sweeps-${e.id}`} title="Sweeps">
        {e.sweeps.length ? (
          <ul className="flex flex-col" aria-label="Sweeps">
            {e.sweeps.map((s) => (
              <SweepRow key={s.id} s={s} />
            ))}
          </ul>
        ) : (
          <p className="text-muted-foreground">No sweep yet. New sweep generates runs from a grid or a random draw over recipe parameters under a GPU-hour cap.</p>
        )}
      </Section>

      <Comparison e={e} />
    </div>
  );
}

function SweepRow({ s }: { s: Sweep }) {
  const params = s.parameters.map((p) => p.name).join(", ");
  return (
    <li className="flex min-h-8 flex-wrap items-center gap-x-2 gap-y-0.5 border-b py-1 last:border-0" data-sweep={s.id} data-state={s.state}>
      <StatusChip state={s.state} />
      <span className="font-medium">{s.mode}</span>
      <span className="text-muted-foreground">over {params}</span>
      <span className="tabular-nums" data-slot="sweep-progress">
        {s.runsDone} of {s.points.length} runs
      </span>
      <span className="tabular-nums" data-slot="sweep-spend">
        {n(s.gpuHoursSpent)} of {n(s.gpuHourCap)} GPU-h cap
      </span>
      {s.stopReason ? <span className="text-status-warning-foreground">{s.stopReason}</span> : null}
      {s.currentRunId ? (
        <button type="button" className="ml-auto font-mono text-primary underline-offset-2 hover:underline" onClick={() => openDocument(`run:${s.currentRunId}`)}>
          current {shortRun(s.currentRunId)}
        </button>
      ) : null}
    </li>
  );
}

function Comparison({ e }: { e: Experiment }) {
  const all = useMemo(() => e.runs ?? [], [e.runs]);
  const params = e.parameters ?? [];
  const [chosen, setChosen] = useState<string[]>([]);
  const [narrowed, setNarrowed] = useState(false);
  const rows = narrowed && chosen.length ? all.filter((r) => chosen.includes(r.runId)) : all;
  const numeric = useMemo(() => numericParams(e, all), [e, all]);
  const [xParam, setXParam] = useState<string | undefined>(undefined);
  const x = xParam && numeric.includes(xParam) ? xParam : numeric[0];
  const scatter = useMemo(() => (x ? scatterSpec(e, rows, x) : undefined), [e, rows, x]);
  const parallel = useMemo(() => (e.sweeps.length ? parallelSpec(e, rows) : undefined), [e, rows]);
  const toggle = (id: string) => setChosen((c) => (c.includes(id) ? c.filter((v) => v !== id) : [...c, id]));
  if (all.length === 0) {
    return (
      <Section id={`exp-compare-${e.id}`} title="Comparison">
        <p className="text-muted-foreground">No run yet. Runs of a sweep, or runs.new with this experiment, appear here.</p>
      </Section>
    );
  }
  return (
    <>
      <Section
        id={`exp-compare-${e.id}`}
        title="Comparison"
        actions={
          narrowed ? (
            <Button size="xs" variant="ghost" onClick={() => setNarrowed(false)}>
              Show all {all.length}
            </Button>
          ) : (
            <Button size="xs" variant="outline" disabled={chosen.length < 2} title={chosen.length < 2 ? "Select two or more runs" : undefined} onClick={() => setNarrowed(true)} data-slot="compare-n">
              Compare {chosen.length || "N"}
            </Button>
          )
        }
      >
        <div className="overflow-x-auto">
          <table className="w-full border-collapse" aria-label="Runs of the experiment" data-slot="comparison">
            <thead className="text-left text-muted-foreground">
              <tr>
                <th className="w-6 font-normal">
                  <span className="sr-only">Select</span>
                </th>
                <th className="font-normal">Run</th>
                <th className="font-normal">Status</th>
                {params.map((p) => (
                  <th key={p.name} className="font-normal" title={p.default !== undefined ? `default ${showValue(p.default)}` : undefined} data-param={p.name}>
                    <span className="font-mono">{p.name}</span>
                    {p.swept ? <span className="ml-1 rounded-full border px-1 text-[10px]">swept</span> : null}
                  </th>
                ))}
                <th className="font-normal">Best val WER</th>
                <th className="font-normal">GPU-h</th>
                <th className="font-normal">Eval</th>
              </tr>
            </thead>
            <tbody>
              {rows.map((r) => (
                <ComparisonRow key={r.runId} r={r} params={params.map((p) => p.name)} checked={chosen.includes(r.runId)} onToggle={() => toggle(r.runId)} />
              ))}
            </tbody>
          </table>
        </div>
        <p className="text-muted-foreground">Highlighted cells depart from the default; ◆ marks the best run by validation WER.</p>
      </Section>
      <div className="grid gap-5 lg:grid-cols-2">
        {scatter ? (
          <Section
            id={`exp-scatter-${e.id}`}
            title="Parameter against validation WER"
            actions={
              <NativeSelect aria-label="Parameter on the x axis" className="h-6 w-auto text-xs" value={x} onChange={(ev) => setXParam(ev.target.value)}>
                {numeric.map((p) => (
                  <option key={p} value={p}>
                    {p}
                  </option>
                ))}
              </NativeSelect>
            }
          >
            <AnalyticsChart spec={scatter} height={220} hideTitle />
          </Section>
        ) : null}
        {parallel ? (
          <Section id={`exp-parallel-${e.id}`} title="Parallel coordinates">
            <AnalyticsChart spec={parallel} height={240} hideTitle />
          </Section>
        ) : null}
      </div>
    </>
  );
}

function ComparisonRow({ r, params, checked, onToggle }: { r: ExperimentRun; params: string[]; checked: boolean; onToggle: () => void }) {
  return (
    <tr className="border-t" data-run={r.runId} data-best={r.best || undefined}>
      <td className="py-0.5">
        <input type="checkbox" className="size-4 accent-primary" checked={checked} onChange={onToggle} aria-label={`Select run ${shortRun(r.runId)}`} />
      </td>
      <td>
        <button type="button" className="font-mono underline-offset-2 hover:underline" onClick={() => openDocument(`run:${r.runId}`)}>
          {shortRun(r.runId)}
        </button>
        {r.best ? <span className="ml-1 text-accent-text">◆ best</span> : null}
      </td>
      <td>
        <StatusChip state={r.status} />
      </td>
      {params.map((p) => {
        const departs = r.departures.includes(p);
        return (
          <td key={p} className={cn("px-1 tabular-nums", departs && "rounded bg-diff-added text-diff-added-foreground")} data-departs={departs || undefined} title={departs ? "Departs from the default" : undefined}>
            {showValue(r.values[p])}
          </td>
        );
      })}
      <td className="tabular-nums">{n(r.bestValWer, 4)}</td>
      <td className="tabular-nums">
        {n(r.gpuHours)}
        {r.estimateGpuHours !== undefined ? <span className="text-muted-foreground"> / ~{n(r.estimateGpuHours)}</span> : null}
      </td>
      <td>{r.eval ? (r.eval.verdict ?? r.eval.status) : "—"}</td>
    </tr>
  );
}

/** Evaluate the best checkpoint: evals.new's plan first (cells cached and to compute, GPU-hours), then start. */
function EvaluateBest({ project, checkpointId, onClose, onDone }: { project: string; checkpointId: string; onClose: () => void; onDone: () => void }) {
  const [plan, setPlan] = useState<EvalPlan>();
  const [busy, setBusy] = useState(false);
  const [message, setMessage] = useState<{ error: boolean; text: string } | null>(null);
  const body = { subject: { checkpointId } };
  const act = async (dryRun: boolean) => {
    setBusy(true);
    setMessage(null);
    try {
      const res = await runCommand("evals.new", { project, body, dryRun });
      if ("approvalId" in res) setMessage({ error: false, text: `The eval waits for an approval (${res.approvalId}).` });
      else if ("id" in res) {
        setMessage({ error: false, text: `Eval ${res.id} is ${res.status}; gate it with evals.gate when it is done.` });
        onDone();
      } else setPlan(res);
    } catch (err) {
      setMessage({ error: true, text: errorMessage(err) });
    } finally {
      setBusy(false);
    }
  };
  return (
    <div className="flex flex-col gap-2 rounded-md border bg-tool p-3" role="group" aria-label="Evaluate the best checkpoint" data-slot="evaluate-best">
      <p>
        Evaluate checkpoint <code className="font-mono">{checkpointId}</code> against the baseline on the project's golden sets.
      </p>
      {plan ? (
        <p className="tabular-nums" data-slot="eval-plan">
          {plan.cellsToCompute} cells to compute, {plan.cellsCached} cached · ~{n(plan.estimate.gpuHours)} GPU-h ({n(plan.estimate.audioHours)} h of audio)
        </p>
      ) : null}
      <div className="flex gap-1">
        <Button size="xs" variant="outline" disabled={busy} onClick={() => void act(true)}>
          Plan
        </Button>
        <Button size="xs" disabled={busy || !plan} onClick={() => void act(false)}>
          Start eval
        </Button>
        <Button size="xs" variant="ghost" onClick={onClose}>
          Close
        </Button>
      </div>
      {message ? (
        <p role={message.error ? "alert" : "status"} className={message.error ? "text-destructive" : "text-muted-foreground"}>
          {message.text}
        </p>
      ) : null}
    </div>
  );
}

/** Register the best checkpoint: the dry run shows the collection and the gate first, then the confirmation. */
function RegisterBest({ project, checkpointId, reason, onClose, onDone }: { project: string; checkpointId: string; reason?: string; onClose: () => void; onDone: () => void }) {
  const [plan, setPlan] = useState<ModelRegistration>();
  const [busy, setBusy] = useState(false);
  const [message, setMessage] = useState<{ error: boolean; text: string } | null>(reason ? { error: true, text: reason } : null);
  const act = async (dryRun: boolean) => {
    setBusy(true);
    setMessage(null);
    try {
      const res = await runCommand("models.register", { project, body: { checkpointId }, dryRun });
      if (!res) return;
      if ("approvalId" in res) setMessage({ error: false, text: `Registration waits for an approval (${res.approvalId}).` });
      else if ("id" in res) {
        setMessage({ error: false, text: `Registered ${res.name} ${res.version}.` });
        onDone();
      } else setPlan(res);
    } catch (err) {
      setMessage({ error: true, text: errorMessage(err) });
    } finally {
      setBusy(false);
    }
  };
  return (
    <div className="flex flex-col gap-2 rounded-md border bg-tool p-3" role="group" aria-label="Register the best checkpoint" data-slot="register-best">
      <p>
        Publish checkpoint <code className="font-mono">{checkpointId}</code> as a model version (its latest gated eval must have passed).
      </p>
      {plan ? (
        <p data-slot="register-plan">
          Registers as <span className="font-medium">{plan.name}</span> with eval {plan.model.evalId} (gate {plan.model.gate.verdict}).
        </p>
      ) : null}
      <div className="flex gap-1">
        <Button size="xs" variant="outline" disabled={busy || !!reason} onClick={() => void act(true)}>
          Show registration
        </Button>
        <Button size="xs" disabled={busy || !plan} onClick={() => void act(false)}>
          Confirm register
        </Button>
        <Button size="xs" variant="ghost" onClick={onClose}>
          Close
        </Button>
      </div>
      {message ? (
        <p role={message.error ? "alert" : "status"} className={message.error ? "text-destructive" : "text-muted-foreground"}>
          {message.text}
        </p>
      ) : null}
    </div>
  );
}

/** The empty state's form: experiments.new with its dry run's check first. */
function NewExperimentForm() {
  const project = useProject();
  const mixes = useQuery({ ...mixesListOptions({ path: { p: project ?? "" } }), enabled: !!project });
  const [name, setName] = useState("");
  const [question, setQuestion] = useState("");
  const [mix, setMix] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  if (!project) return null;
  const items = mixes.data?.items ?? [];
  const chosenMix = mix || items[0]?.id || "";
  const valid = name.trim() !== "" && question.trim() !== "" && chosenMix !== "";
  const create = async () => {
    setBusy(true);
    setError(null);
    try {
      const res = await runCommand("experiments.new", { project, body: { name: name.trim(), question: question.trim(), mix: chosenMix } });
      if (res) openDocument(`experiment:${res.id}`);
    } catch (err) {
      setError(errorMessage(err));
    } finally {
      setBusy(false);
    }
  };
  return (
    <form className="flex w-72 flex-col gap-1.5 text-left text-xs" aria-label="New experiment" onSubmit={(ev) => (ev.preventDefault(), valid && void create())}>
      <Input aria-label="Name" className="h-6 text-xs" placeholder="Name (lr-warmup)" value={name} onChange={(ev) => setName(ev.target.value)} />
      <Textarea aria-label="Question" className="min-h-12 text-xs" placeholder="The question the runs answer" value={question} onChange={(ev) => setQuestion(ev.target.value)} />
      <NativeSelect aria-label="Mix" className="h-6 text-xs" value={chosenMix} onChange={(ev) => setMix(ev.target.value)}>
        {items.map((m) => (
          <option key={m.id} value={m.id}>
            {m.name} rev {m.rev}
          </option>
        ))}
      </NativeSelect>
      <Button type="submit" size="xs" disabled={busy || !valid} data-command="experiments.new">
        New experiment
      </Button>
      {error ? (
        <p role="alert" className="text-destructive">
          {error}
        </p>
      ) : null}
    </form>
  );
}

function Details({ e }: { e: Experiment }) {
  const rows: [string, React.ReactNode][] = [
    ["ID", <code className="font-mono text-[11px]">{e.id}</code>],
    ["Revision", `rev ${e.rev}`],
    ["Tag", e.tag ?? "—"],
    ["Opened by", <ActorBadge actor={e.actor} />],
    ["Created", new Date(e.createdAt).toLocaleString()],
    ["Mix", `${e.mix.name} rev ${e.mix.revision} (${e.mix.id})`],
    ["Base model", `${e.baseModel.name} ${e.baseModel.version} (${e.baseModel.id})`],
    ["Runs", String(e.runCount)],
    ["Sweeps", String(e.sweeps.length)],
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

function Activity({ id }: { id: string }) {
  const topics = `entity.experiment.${id}`;
  const q = useQuery(eventsListOptions({ query: { topics, limit: 100 } }));
  useTopic([topics], () => void q.refetch());
  const items = [...(q.data?.items ?? [])].reverse();
  if (items.length === 0) return <EmptyState step="record" title="No activity yet" />;
  return (
    <ol className="flex flex-col px-4 py-2 text-xs">
      {items.map((ev) => (
        <li key={ev.seq} className="flex h-8 items-center gap-3 border-b last:border-0">
          <time className="text-muted-foreground tabular-nums">{new Date(ev.at).toLocaleString()}</time>
          <span className="font-medium">{ev.type}</span>
          <ActorBadge actor={ev.actor} toolCallId={ev.causedBy?.toolCallId} />
          {ev.entity ? <span className="ml-auto text-muted-foreground">rev {ev.entity.rev}</span> : null}
        </li>
      ))}
    </ol>
  );
}
