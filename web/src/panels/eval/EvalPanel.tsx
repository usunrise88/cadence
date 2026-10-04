import { useMemo, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { NavArrowDown, NavArrowRight, OpenNewWindow, SoundHigh } from "iconoir-react";
import { evalsGetOptions, eventsListOptions } from "@/api/gen/@tanstack/react-query.gen";
import type { Eval, EvalCell, EvalGateCheck, ModelRegistration } from "@/api/gen/types.gen";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { NativeSelect } from "@/components/ui/native-select";
import { cn } from "@/lib/utils";
import { openAudio } from "@/shell/audio";
import { AnalyticsChart } from "@/shell/charts";
import { ActorBadge, EmptyState, StatusChip } from "@/shell/entity/primitives";
import {
  errorMessage,
  EVAL_FORM_REQUEST,
  EvalForm,
  evalItem,
  formFromEval,
  subjectRefOf,
  focusPipelineRun,
  formatInterval,
  formatRate,
  GATE_CLASS,
  GATE_GLYPH,
  invalidateEval,
  openDocument,
  openPanelById,
  parseEvalItem,
  problemOf,
  registrable,
  runCommand,
  textDirection,
  TONE_CLASS,
  TONE_GLYPH,
  TONE_LABEL,
  useEditRequest,
  useProject,
  useSelection,
  useTopic,
  WORST_N,
  type PanelProps,
} from "@/shell/panel";
import {
  augmentationLabel,
  baselineOf,
  bucketBars,
  buildMatrix,
  cellTitle,
  decodingLabel,
  defaultCell,
  deltaForest,
  deltaHeatmap,
  ECDF_ROWS,
  emissionBars,
  emissionReasons,
  entityBars,
  latencyBars,
  profileCells,
  profileLabel,
  progressOf,
  robustnessHeatmap,
  sdiBars,
  shortName,
  stabilityBars,
  unavailableReasons,
  werEcdf,
  werLatency,
  type Matrix,
  type MatrixCell,
} from "./model";

// The Eval report (docs/spec/11-ui-panels.md "Panel catalogue", Eval report; R20–R24, R43, R53, R54): one eval from
// evals.get. The matrix of golden sets × latency profiles fills live (eval.{id}.progress); each cell shows the
// subject's WER, the baseline's, and the delta with its 95 % interval, toned by glyph and colour; the primary cell
// (the one the gate reads) is starred. Charts: delta heatmap, forest plot, S/D/I bars, WER by duration, the
// per-utterance WER ECDF and entity accuracy of the selected cell; under "Streaming" WER against latency, latency to
// final, emission delay (aligned golden sets) and partial stability of the selected cell's golden set; under
// "Robustness" the augmentation matrix. The gate
// (evals.gate) and model registration (models.register, inline confirm) act here; Run eval… and Re-run missing cells
// open the shared Run eval form (evals.new, filled from this eval's axes for the re-run); Edit gates.yaml opens the
// Project home's gate editor. A cell selects into the selection
// bus (cell:<id>), its worst utterances list below, and a row opens in Diff (cell:<id>/utt:<n>).

export function EvalEmpty() {
  return <EmptyState step="review" title="No eval open" hint="Evaluate a checkpoint from Checkpoints, or open an eval from the Library." />;
}

export function EvalPanel({ tab, entity, doc }: PanelProps) {
  const ev = entity?.eval as Eval | undefined;
  if (!entity || !ev) return <EvalEmpty />;
  switch (tab) {
    case "details":
      return <Definition ev={ev} />;
    case "activity":
      return <Activity id={ev.id} />;
    case "lineage":
      return <LineageTab ev={ev} />;
    case "notes":
      return <EmptyState step="record" title="Notes live in NOTES.md" hint="Record what this eval taught you as a project note (Project document, Notes)." />;
    default:
      return <Report ev={ev} doc={doc ?? `eval:${ev.id}`} />;
  }
}

function Section({ id, title, children, className, actions }: { id: string; title: string; children: React.ReactNode; className?: string; actions?: React.ReactNode }) {
  return (
    <section aria-labelledby={id} className={cn("flex min-w-0 flex-col gap-1.5", className)}>
      <div className="flex items-center gap-2">
        <h3 id={id} className="text-[11px] font-medium tracking-wide text-muted-foreground uppercase">
          {title}
        </h3>
        {actions ? <div className="ml-auto flex items-center gap-1">{actions}</div> : null}
      </div>
      {children}
    </section>
  );
}

function Report({ ev, doc }: { ev: Eval; doc: string }) {
  const [decoding, setDecoding] = useState(0);
  const matrix = useMemo(() => buildMatrix(ev, decoding), [ev, decoding]);
  const select = useSelection((s) => s.select);
  const item = useSelection((s) => s.selections[doc]);
  const { cellId, utterance } = parseEvalItem(item);
  const cell = (ev.cells ?? []).find((c) => c.id === cellId && c.role === "subject") ?? defaultCell(ev);
  const base = cell ? baselineOf(ev, cell) : undefined;
  const [registerOpen, setRegisterOpen] = useState(false);
  const [rerun, setRerun] = useState<"fresh" | "missing" | null>(null);
  const project = useProject();
  useEditRequest(doc, () => setRegisterOpen(true));
  useEditRequest(`${EVAL_FORM_REQUEST}${doc}`, () => setRerun("missing"));
  return (
    <div className="flex flex-col gap-5 p-4 text-xs" data-eval={ev.id} data-status={ev.status}>
      <Progress ev={ev} onRun={setRerun} />
      {rerun && project ? (
        <EvalForm
          key={rerun}
          project={project}
          subject={subjectRefOf(ev)}
          subjectLabel={ev.subject.label}
          family={ev.subject.family}
          runId={ev.subject.runId}
          initial={rerun === "missing" ? formFromEval(ev) : undefined}
          planOnOpen={rerun === "missing"}
          onClose={() => setRerun(null)}
        />
      ) : null}
      <Gate ev={ev} onRegister={() => setRegisterOpen(true)} />
      {registerOpen ? <RegisterForm ev={ev} onClose={() => setRegisterOpen(false)} /> : null}
      <Section
        id={`eval-matrix-${ev.id}`}
        title="Matrix"
        actions={
          ev.decoding.length > 1 ? (
            <NativeSelect aria-label="Decoding variant" className="h-6 w-auto text-xs" value={String(decoding)} onChange={(e) => setDecoding(Number(e.target.value))}>
              {ev.decoding.map((d) => (
                <option key={d.index} value={d.index}>
                  {decodingLabel(d)}
                </option>
              ))}
            </NativeSelect>
          ) : (
            <span className="text-muted-foreground">{decodingLabel(ev.decoding[0])}</span>
          )
        }
      >
        <MatrixGrid matrix={matrix} selected={cell?.id} onSelect={(c) => select(doc, evalItem(c.id))} />
        <Legend />
      </Section>
      <div className="grid gap-5 @container lg:grid-cols-2">
        <Section id={`eval-heatmap-${ev.id}`} title="Delta heatmap">
          <div className="h-56">
            <AnalyticsChart spec={deltaHeatmap(matrix)} hideTitle />
          </div>
        </Section>
        <Section id={`eval-forest-${ev.id}`} title="Deltas with intervals">
          <div className="h-56">
            <AnalyticsChart spec={deltaForest(ev, decoding)} hideTitle />
          </div>
        </Section>
        <Section id={`eval-sdi-${ev.id}`} title="Error types">
          <div className="h-56">
            <AnalyticsChart spec={sdiBars(ev, decoding)} hideTitle />
          </div>
        </Section>
        <Section id={`eval-buckets-${ev.id}`} title="WER by duration">
          {cell?.summary?.buckets?.length || base?.summary?.buckets?.length ? (
            <div className="h-56">
              <AnalyticsChart spec={bucketBars(cellTitle(ev, cell!), cell?.summary, base?.summary)} hideTitle />
            </div>
          ) : (
            <p className="text-muted-foreground">Duration buckets appear when the selected cell is scored.</p>
          )}
        </Section>
        {cell ? <EcdfSection ev={ev} cell={cell} base={base} /> : null}
        {cell && (cell.metrics || base?.metrics) ? <EntitySection ev={ev} cell={cell} base={base} /> : null}
      </div>
      {cell ? <Streaming ev={ev} goldenSet={cell.goldenSetVersionId} decoding={decoding} /> : null}
      <Robustness ev={ev} decoding={decoding} />
      {cell ? <Utterances ev={ev} cell={cell} doc={doc} selected={utterance} /> : null}
    </div>
  );
}

/** A report section that folds; it starts collapsed while it has nothing to show and opens when data arrives, unless
 * the reader toggled it. */
function Foldable({ id, slot, title, empty, emptyHint, context, children }: { id: string; slot: string; title: string; empty: boolean; emptyHint: string; context?: React.ReactNode; children: React.ReactNode }) {
  const [choice, setChoice] = useState<boolean | null>(null);
  const open = choice ?? !empty;
  return (
    <section aria-labelledby={id} className="flex min-w-0 flex-col gap-1.5" data-slot={slot} data-empty={empty || undefined}>
      <div className="flex flex-wrap items-center gap-2">
        <h3 id={id} className="text-[11px] font-medium tracking-wide text-muted-foreground uppercase">
          <button type="button" className="flex min-h-6 items-center gap-1 uppercase" aria-expanded={open} aria-controls={`${id}-body`} onClick={() => setChoice(!open)}>
            {open ? <NavArrowDown aria-hidden className="size-3.5" /> : <NavArrowRight aria-hidden className="size-3.5" />}
            {title}
          </button>
        </h3>
        {context ? <span className="text-muted-foreground">{context}</span> : null}
        {empty && !open ? <span className="text-muted-foreground">nothing yet</span> : null}
      </div>
      {open ? (
        <div id={`${id}-body`} className="flex flex-col gap-3">
          {empty ? <p className="text-muted-foreground">{emptyHint}</p> : children}
        </div>
      ) : null}
    </section>
  );
}

function Reasons({ items, label }: { items: { reason: string; cells: string[] }[]; label: string }) {
  if (!items.length) return null;
  return (
    <ul className="flex flex-col gap-0.5 text-muted-foreground" aria-label={label} data-slot="metric-unavailable">
      {items.map((u) => (
        <li key={u.reason}>
          <span className="text-status-warning-foreground">Unavailable:</span> {u.reason} <span className="text-[11px]">({u.cells.join("; ")})</span>
        </li>
      ))}
    </ul>
  );
}

/** Per-utterance WER ECDF of the selected cell against its baseline (the API's worst rows, up to ECDF_ROWS each). */
function EcdfSection({ ev, cell, base }: { ev: Eval; cell: EvalCell; base?: EvalCell }) {
  const opts = (c?: EvalCell) => ({ ...evalsGetOptions({ path: { id: ev.id }, query: { worst: ECDF_ROWS, cell: c?.id ?? "" } }), enabled: !!c?.summary && !c.evicted });
  const qs = useQuery(opts(cell));
  const qb = useQuery(opts(base));
  const side = (c: EvalCell | undefined, data: Eval | undefined) => {
    const rows = c && !c.evicted ? data?.cells?.find((x) => x.id === c.id)?.worst : undefined;
    return rows && c?.summary ? { rows, total: c.summary.utterances } : undefined;
  };
  const s = side(cell, qs.data);
  const b = side(base, qb.data);
  return (
    <Section id={`eval-ecdf-${ev.id}`} title="WER per utterance">
      {s || b ? (
        <div className="h-56">
          <AnalyticsChart spec={werEcdf(cellTitle(ev, cell), s, b)} hideTitle />
        </div>
      ) : qs.isLoading || qb.isLoading ? (
        <p className="text-muted-foreground">Loading…</p>
      ) : qs.error ? (
        <p className="text-destructive">{errorMessage(qs.error)}</p>
      ) : cell.evicted ? (
        <p className="text-muted-foreground">{cell.evicted.note}</p>
      ) : (
        <p className="text-muted-foreground">The distribution appears when the selected cell is scored.</p>
      )}
    </Section>
  );
}

function EntitySection({ ev, cell, base }: { ev: Eval; cell: EvalCell; base?: EvalCell }) {
  const spec = entityBars(cellTitle(ev, cell), cell.metrics?.entities, base?.metrics?.entities);
  const reasons = unavailableReasons(ev, "entities", base ? [cell, base] : [cell]);
  return (
    <Section id={`eval-entities-${ev.id}`} title="Entity accuracy">
      {spec ? (
        <div className="h-56">
          <AnalyticsChart spec={spec} hideTitle />
        </div>
      ) : !reasons.length ? (
        <p className="text-muted-foreground">Entity accuracy appears when its scorer finishes for the selected cell.</p>
      ) : null}
      <Reasons items={reasons} label="Why entity accuracy is unavailable" />
    </Section>
  );
}

/** WER against latency, latency to final and partial stability of one golden set across the profiles. */
function Streaming({ ev, goldenSet, decoding }: { ev: Eval; goldenSet: string; decoding: number }) {
  const rows = profileCells(ev, goldenSet, decoding);
  const wl = werLatency(ev, goldenSet, decoding);
  const lat = latencyBars(ev, goldenSet, decoding);
  const em = emissionBars(ev, goldenSet, decoding);
  const stab = stabilityBars(ev, goldenSet, decoding);
  const cells = rows.flatMap((r) => [r.subject, r.baseline].filter((c): c is EvalCell => !!c));
  const reasons = unavailableReasons(ev, "latency", cells);
  const emReasons = emissionReasons(ev, cells);
  const hasWl = wl.series.some((s) => s.points.length > 0);
  const empty = !hasWl && !lat && !stab && !reasons.length;
  const gs = ev.goldenSets.find((g) => g.versionId === goldenSet);
  return (
    <Foldable
      id={`eval-streaming-${ev.id}`}
      slot="streaming"
      title="Streaming"
      empty={empty}
      emptyHint="WER against latency, latency to final and partial stability appear as the cells are scored."
      context={`${gs ? shortName(gs.name) : goldenSet}${ev.decoding.length > 1 ? ` · ${decodingLabel(ev.decoding.find((d) => d.index === decoding))}` : ""} · follows the selected cell`}
    >
      <div className="grid gap-5 lg:grid-cols-2">
        {hasWl ? (
          <Section id={`eval-wer-latency-${ev.id}`} title="WER against latency" className="lg:col-span-2">
            <div className="h-64">
              <AnalyticsChart spec={wl} hideTitle />
            </div>
          </Section>
        ) : null}
        <Section id={`eval-latency-${ev.id}`} title="Latency to final">
          {lat ? (
            <div className="h-56">
              <AnalyticsChart spec={lat} hideTitle />
            </div>
          ) : !reasons.length ? (
            <p className="text-muted-foreground">Latency to final appears when its scorer finishes.</p>
          ) : null}
          <Reasons items={reasons} label="Why latency to final is unavailable" />
        </Section>
        {em || emReasons.length ? (
          <Section id={`eval-emission-${ev.id}`} title="Emission delay">
            {em ? (
              <div className="h-56">
                <AnalyticsChart spec={em} hideTitle />
              </div>
            ) : null}
            <Reasons items={emReasons} label="Why emission delay is n/a" />
          </Section>
        ) : null}
        <Section id={`eval-stability-${ev.id}`} title="Partial stability">
          {stab ? (
            <div className="flex flex-col gap-3">
              <div className="h-44">
                <AnalyticsChart spec={stab.ratio} hideTitle />
              </div>
              <div className="h-44">
                <AnalyticsChart spec={stab.edits} hideTitle />
              </div>
            </div>
          ) : (
            <p className="text-muted-foreground">No partial stability in these scores (the decode wrote no partials).</p>
          )}
        </Section>
      </div>
    </Foldable>
  );
}

function Robustness({ ev, decoding }: { ev: Eval; decoding: number }) {
  const spec = robustnessHeatmap(ev, decoding);
  const augs = (ev.augmentations ?? []).filter((a) => a.index > 0);
  return (
    <Foldable
      id={`eval-robustness-${ev.id}`}
      slot="robustness"
      title="Robustness"
      empty={!spec}
      emptyHint={augs.length ? "The robustness matrix fills as the augmented cells are scored." : "This eval has no augmentation axis (evals.new with augmentations adds one)."}
      context={augs.length ? augs.map((a) => augmentationLabel(ev, a.index)).join(", ") : undefined}
    >
      {spec ? (
        <div style={{ height: Math.max(160, 48 + spec.y.length * 28) }}>
          <AnalyticsChart spec={spec} hideTitle />
        </div>
      ) : null}
    </Foldable>
  );
}

function Progress({ ev, onRun }: { ev: Eval; onRun: (mode: "fresh" | "missing") => void }) {
  const p = ev.progress;
  const done = ev.status === "done" || ev.status === "failed";
  const missing = done && p.cellsDone < p.cellsTotal;
  return (
    <div className="flex flex-col gap-1.5" data-slot="eval-progress">
      <div className="flex flex-wrap items-center gap-2">
        <StatusChip state={ev.status} />
        <span className="tabular-nums" aria-live="polite">
          {p.cellsDone} of {p.cellsTotal} cells scored{p.cellsCached ? ` · ${p.cellsCached} from cache` : ""}
        </span>
        {!done ? <span className="text-muted-foreground tabular-nums">~{ev.estimate.gpuHours.toFixed(2)} GPU-h for {ev.estimate.cellsToCompute} cells</span> : null}
        {ev.error ? <span className="text-status-failed-foreground">{ev.error}</span> : null}
        <span className="ml-auto flex flex-wrap gap-1">
          {/* The same axes again: scored cells come back cached, only the missing ones compute. */}
          <Button
            size="xs"
            variant={missing ? "default" : "outline"}
            disabled={!missing}
            title={missing ? undefined : done ? "Every cell is scored" : "The eval is still running"}
            onClick={() => onRun("missing")}
            data-command="evals.new"
          >
            Re-run missing cells
          </Button>
          <Button size="xs" variant="outline" onClick={() => onRun("fresh")} data-command="evals.new">
            Run eval…
          </Button>
        </span>
        {ev.pipelineRunId ? (
          <Button
            size="xs"
            variant="ghost"
            onClick={() => {
              focusPipelineRun(ev.pipelineRunId ?? null);
              openPanelById("pipeline-run");
            }}
          >
            <OpenNewWindow aria-hidden />
            Pipeline run
          </Button>
        ) : null}
      </div>
      {!done ? (
        <div role="progressbar" aria-label="Cells scored" aria-valuemin={0} aria-valuemax={p.cellsTotal} aria-valuenow={p.cellsDone} className="h-1.5 w-full overflow-hidden rounded-full bg-hover">
          <div className="h-full bg-status-running" style={{ width: `${Math.round(progressOf(ev) * 100)}%` }} />
        </div>
      ) : null}
    </div>
  );
}

function checkTitle(ev: Eval, c: EvalGateCheck): string {
  const what = c.kind === "target" ? "Target" : c.kind === "replay" ? "Replay" : c.kind === "deletionsInsertions" ? "Deletions vs insertions" : c.kind === "baseline" ? "Baseline" : "Primary profile";
  const gs = c.goldenSet ? ` · ${shortName(c.goldenSet)}` : "";
  return `${what}${gs}${c.profile && c.profile !== ev.primaryProfile ? ` · ${c.profile}` : ""}`;
}

function Gate({ ev, onRegister }: { ev: Eval; onRegister: () => void }) {
  const qc = useQueryClient();
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const gate = ev.gate;
  const run = async () => {
    setBusy(true);
    setError(null);
    try {
      await runCommand("evals.gate", { eval: { id: ev.id, rev: ev.rev } });
      invalidateEval(qc, ev.id);
    } catch (err) {
      setError(errorMessage(err));
      if (problemOf(err)?.status === 412) invalidateEval(qc, ev.id);
    } finally {
      setBusy(false);
    }
  };
  const canRegister = registrable(ev);
  return (
    <Section id={`eval-gate-${ev.id}`} title="Gate">
      <div className="flex flex-wrap items-center gap-2" data-slot="gate-verdict" data-verdict={gate?.verdict ?? "none"}>
        {gate ? (
          <span className={cn("text-[13px] font-medium", GATE_CLASS[gate.verdict])}>
            <span aria-hidden>{GATE_GLYPH[gate.verdict]} </span>
            Gate {gate.verdict}
          </span>
        ) : (
          <span className="text-muted-foreground">Not run yet: the gate reads the primary profile ({ev.primaryProfile}) once the eval is done.</span>
        )}
        {gate ? (
          <span className="text-muted-foreground">
            gates.yaml {gate.gatesSha ? <code className="text-[11px]">{gate.gatesSha.slice(0, 7)}</code> : "defaults"} · {new Date(gate.at).toLocaleString()}
          </span>
        ) : null}
        <span className="ml-auto flex gap-1">
          <Button size="xs" variant={gate ? "outline" : "default"} disabled={busy || ev.status !== "done"} title={ev.status !== "done" ? "The gate reads a finished eval" : undefined} onClick={() => void run()} data-command="evals.gate">
            {gate ? "Run the gate again" : "Run the gate"}
          </Button>
          <Button size="xs" variant={gate?.verdict === "passed" ? "default" : "outline"} disabled={canRegister !== true} title={canRegister === true ? undefined : canRegister} onClick={onRegister} data-command="models.register">
            Register model…
          </Button>
          <Button size="xs" variant="ghost" onClick={() => void runCommand("gates.edit", {})} title="The project's gate: the effective values and gates.yaml (Project home)" data-command="gates.edit">
            Edit gates.yaml
          </Button>
        </span>
      </div>
      {error ? (
        <p role="alert" className="text-destructive">
          {error}
        </p>
      ) : null}
      {gate?.checks.length ? (
        <ul className="flex flex-col" aria-label="Gate checks" data-slot="gate-checks">
          {gate.checks.map((c, i) => (
            <li key={i} className="flex min-h-7 flex-wrap items-center gap-x-2 gap-y-0.5 border-b py-1 last:border-0" data-check={c.kind} data-state={c.state}>
              <span className={cn("w-24 shrink-0 font-medium", GATE_CLASS[c.state])}>
                <span aria-hidden>{GATE_GLYPH[c.state]} </span>
                {c.state}
              </span>
              <span className="font-medium">{checkTitle(ev, c)}</span>
              {c.delta ? <span className="tabular-nums">Δ {formatInterval(c.delta)}</span> : null}
              {c.candidateWer !== undefined || c.baselineWer !== undefined ? (
                <span className="text-muted-foreground tabular-nums">
                  {formatRate(c.candidateWer, 2)} vs {formatRate(c.baselineWer, 2)}
                </span>
              ) : null}
              {c.threshold !== undefined ? <span className="text-muted-foreground tabular-nums">allowed +{(c.threshold * 100).toFixed(1)} pp</span> : null}
              <span className="basis-full text-muted-foreground">{c.message}</span>
            </li>
          ))}
        </ul>
      ) : null}
    </Section>
  );
}

/** models.register: the dry run shows what would be registered; the real call needs a second, inline confirm. */
function RegisterForm({ ev, onClose }: { ev: Eval; onClose: () => void }) {
  const project = useProject();
  const [name, setName] = useState("");
  const [description, setDescription] = useState("");
  const [busy, setBusy] = useState(false);
  const [preview, setPreview] = useState<ModelRegistration | null>(null);
  const [confirm, setConfirm] = useState(false);
  const [message, setMessage] = useState<{ error: boolean; text: string; help?: string } | null>(null);
  const reason = registrable(ev);
  const body = { checkpointId: ev.subject.id, evalId: ev.id, ...(name.trim() ? { name: name.trim() } : {}), ...(description.trim() ? { description: description.trim() } : {}) };
  const act = async (dryRun: boolean) => {
    if (!project) return;
    setBusy(true);
    setMessage(null);
    try {
      const res = await runCommand("models.register", { project, body, dryRun });
      if (!res) return;
      if ("approvalId" in res) setMessage({ error: false, text: `Registration waits for an approval (${res.approvalId}).` });
      else if ("model" in res && !("id" in res)) setPreview(res);
      else if ("id" in res) {
        openDocument(`model:${res.id}`);
        onClose();
      }
    } catch (err) {
      const p = problemOf(err);
      setMessage({ error: true, text: errorMessage(err), help: p?.type });
      setConfirm(false);
    } finally {
      setBusy(false);
    }
  };
  return (
    <form
      onSubmit={(e) => {
        e.preventDefault();
        void act(true);
      }}
      className="flex flex-col gap-2 rounded-md border bg-tool p-3"
      aria-label="Register model version"
      data-slot="register-form"
    >
      <p>
        Register checkpoint <code className="text-[11px]">{ev.subject.id}</code> ({ev.subject.label}) as a model version, published with this eval and its
        model card.
      </p>
      <div className="grid grid-cols-[7rem_1fr] items-center gap-2">
        <label htmlFor={`register-name-${ev.id}`} className="text-muted-foreground">
          Collection
        </label>
        <Input id={`register-name-${ev.id}`} className="h-6 text-xs" placeholder={`model/${project ?? "<project>"}`} value={name} onChange={(e) => (setName(e.target.value), setPreview(null), setConfirm(false))} />
        <label htmlFor={`register-desc-${ev.id}`} className="text-muted-foreground">
          Description
        </label>
        <Input id={`register-desc-${ev.id}`} className="h-6 text-xs" placeholder="Used when the collection is new" value={description} onChange={(e) => (setDescription(e.target.value), setPreview(null), setConfirm(false))} />
      </div>
      {preview ? (
        <p className="text-muted-foreground" data-slot="register-preview">
          Will register <span className="font-medium text-foreground">{preview.name}</span> · gate {preview.model.gate.verdict} · weights{" "}
          <code className="text-[11px]">{preview.model.weightsHash.slice(0, 16)}…</code>
        </p>
      ) : null}
      <div className="flex gap-1">
        <Button type="submit" size="xs" variant="outline" disabled={busy || reason !== true}>
          Check
        </Button>
        <Button
          type="button"
          size="xs"
          variant={confirm ? "destructive" : "default"}
          disabled={busy || reason !== true || !preview}
          title={!preview ? "Check first" : undefined}
          onClick={() => (confirm ? void act(false) : setConfirm(true))}
          data-command="models.register"
        >
          {confirm ? "Confirm register (cannot be undone)" : "Register"}
        </Button>
        <Button type="button" size="xs" variant="ghost" onClick={onClose}>
          Cancel
        </Button>
      </div>
      {reason !== true ? <p className="text-status-warning-foreground">{reason}</p> : null}
      {message ? (
        <p role={message.error ? "alert" : "status"} className={message.error ? "text-destructive" : "text-muted-foreground"} data-problem={message.help}>
          {message.text}
        </p>
      ) : null}
    </form>
  );
}

function Legend() {
  return (
    <p className="flex flex-wrap gap-x-3 gap-y-0.5 text-[11px] text-muted-foreground" data-slot="matrix-legend">
      {(["better", "worse", "same"] as const).map((t) => (
        <span key={t}>
          <span aria-hidden className={TONE_CLASS[t]}>
            {TONE_GLYPH[t]}
          </span>{" "}
          {TONE_LABEL[t]}
        </span>
      ))}
      <span>★ primary profile (the gate reads it)</span>
      <span>⟲ from the eval-record cache</span>
      <span>Δ subject − baseline, percentage points, 95 % interval</span>
    </p>
  );
}

function MatrixGrid({ matrix, selected, onSelect }: { matrix: Matrix; selected?: string; onSelect: (c: EvalCell) => void }) {
  if (matrix.rows.length === 0) return <p className="text-muted-foreground">No golden sets in this eval.</p>;
  return (
    <div className="overflow-x-auto">
      <table className="w-full border-separate border-spacing-1" aria-label="Golden sets by latency profile" data-slot="eval-matrix">
        <thead>
          <tr>
            <th className="text-left font-normal text-muted-foreground">Golden set</th>
            {matrix.cols.map((p, i) => (
              <th key={p.name} scope="col" className="text-left font-normal text-muted-foreground">
                {profileLabel(p)}
                {matrix.cells.some((r) => r[i]?.primary) ? <span title="Primary profile: the gate reads it"> ★</span> : null}
              </th>
            ))}
          </tr>
        </thead>
        <tbody>
          {matrix.rows.map((gs, r) => (
            <tr key={gs.versionId}>
              <th scope="row" className="pr-2 text-left align-top font-medium">
                <button type="button" className="text-left underline-offset-2 hover:underline" onClick={() => openDocument(`golden_set:${gs.versionId}`)}>
                  {shortName(gs.name)}
                </button>
                <div className="font-normal text-muted-foreground">
                  {gs.locale} · {gs.utterances.toLocaleString()} utt.{gs.replay ? " · replay" : ""}
                </div>
              </th>
              {matrix.cells[r]!.map((c, i) => (
                <td key={matrix.cols[i]!.name} className="align-top">
                  {c ? <MatrixButton c={c} selected={!!c.subject && c.subject.id === selected} onSelect={onSelect} /> : <span className="text-muted-foreground" aria-label="Not evaluated at this profile">—</span>}
                </td>
              ))}
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

function MatrixButton({ c, selected, onSelect }: { c: MatrixCell; selected: boolean; onSelect: (c: EvalCell) => void }) {
  const s = c.subject;
  const scored = !!s?.summary;
  const delta = s?.delta;
  const state = s?.state ?? c.baseline?.state ?? "queued";
  const label = `${shortName(c.goldenSet.name)} at ${c.profile.name}: ${scored ? `WER ${formatRate(s!.summary!.wer, 2)}` : state}${delta ? `, delta ${formatInterval(delta.wer)}, ${TONE_LABEL[c.tone]}` : ""}${c.primary ? ", primary" : ""}${c.gate ? `, gate check ${c.gate}` : ""}`;
  return (
    <button
      type="button"
      aria-pressed={selected}
      aria-label={label}
      disabled={!s}
      onClick={() => s && onSelect(s)}
      data-cell={s?.id}
      data-tone={c.tone}
      data-state={state}
      data-primary={c.primary || undefined}
      className={cn(
        "flex min-h-14 w-full min-w-36 flex-col items-start gap-0.5 rounded-md border bg-background px-2 py-1 text-left hover:bg-hover",
        selected && "border-accent-line bg-selected",
        c.primary && "border-2",
        c.gate === "failed" && "border-status-failed",
        c.gate === "passed" && "border-status-done",
        c.gate === "inconclusive" && "border-status-warning",
      )}
    >
      <span className="flex w-full items-center gap-1">
        <span className="text-[13px] font-medium tabular-nums">{scored ? formatRate(s!.summary!.wer, 2) : <StatusChip state={state === "cached" ? "done" : state} />}</span>
        {c.primary ? <span aria-hidden>★</span> : null}
        {s?.state === "cached" ? (
          <span aria-hidden title="From the eval-record cache">
            ⟲
          </span>
        ) : null}
        {c.gate ? (
          <span aria-hidden className={cn("ml-auto", GATE_CLASS[c.gate])}>
            {GATE_GLYPH[c.gate]}
          </span>
        ) : null}
      </span>
      {c.baseline?.summary ? <span className="text-muted-foreground tabular-nums">base {formatRate(c.baseline.summary.wer, 2)}</span> : null}
      {delta && !delta.error ? (
        <span className={cn("tabular-nums", TONE_CLASS[c.tone])}>
          <span aria-hidden>{TONE_GLYPH[c.tone]} </span>Δ {formatInterval(delta.wer)}
        </span>
      ) : delta?.error ? (
        <span className="text-status-warning-foreground">{delta.error}</span>
      ) : null}
      {scored ? (
        <span className="text-muted-foreground tabular-nums">
          CER {formatRate(s!.summary!.cer, 1)} · no punct. {formatRate(s!.summary!.werNoPunct, 1)}
        </span>
      ) : null}
    </button>
  );
}

function Utterances({ ev, cell, doc, selected }: { ev: Eval; cell: EvalCell; doc: string; selected?: number }) {
  const q = useQuery({ ...evalsGetOptions({ path: { id: ev.id }, query: { worst: WORST_N, cell: cell.id } }), enabled: !!cell.summary && !cell.evicted });
  // An evicted cell has no rows (its scores left the store): its note stands in for the table.
  const rows = (!cell.evicted && q.data?.cells?.find((c) => c.id === cell.id)?.worst) || [];
  const select = useSelection((s) => s.select);
  const [filter, setFilter] = useState("");
  const [errorsOnly, setErrorsOnly] = useState(true);
  const gs = ev.goldenSets.find((g) => g.versionId === cell.goldenSetVersionId);
  const dir = textDirection(gs?.locale);
  const f = filter.trim().toLowerCase();
  const shown = rows.filter((r) => (!errorsOnly || r.errors > 0) && (!f || r.ref.toLowerCase().includes(f) || r.hyp.toLowerCase().includes(f) || (r.speaker ?? "").toLowerCase().includes(f)));
  const open = (index: number) => {
    select(doc, evalItem(cell.id, index));
    openPanelById("diff");
  };
  return (
    <Section
      id={`eval-utts-${ev.id}`}
      title={`Worst utterances · ${cellTitle(ev, cell)}`}
      actions={
        <>
          <Input className="h-6 w-40 text-xs" placeholder="Filter text or speaker" aria-label="Filter utterances" value={filter} onChange={(e) => setFilter(e.target.value)} />
          <label className="flex items-center gap-1 text-muted-foreground">
            <input type="checkbox" className="size-4 accent-primary" checked={errorsOnly} onChange={(e) => setErrorsOnly(e.target.checked)} />
            With errors
          </label>
        </>
      }
    >
      {!cell.summary ? <p className="text-muted-foreground">The cell is not scored yet.</p> : null}
      {cell.evicted ? (
        <p className="text-muted-foreground" data-slot="utterances-evicted">
          {cell.evicted.note}
        </p>
      ) : null}
      {q.isLoading ? <p className="text-muted-foreground">Loading…</p> : null}
      {q.error ? <p className="text-destructive">{errorMessage(q.error)}</p> : null}
      {cell.summary && !cell.evicted && q.data && rows.length === 0 ? <p className="text-muted-foreground">No utterance rows in the scores.</p> : null}
      {shown.length ? (
        <table className="w-full table-fixed" aria-label="Worst utterances; Enter opens one in Diff" data-slot="utterance-table">
          <thead className="text-left text-muted-foreground">
            <tr>
              <th className="w-10 font-normal">#</th>
              <th className="w-16 font-normal">WER</th>
              <th className="w-24 font-normal">S · D · I</th>
              <th className="font-normal">Reference</th>
              <th className="font-normal">Hypothesis</th>
              <th className="w-8 font-normal">
                <span className="sr-only">Audio</span>
              </th>
            </tr>
          </thead>
          <tbody>
            {shown.map((r) => (
              <tr
                key={r.index}
                tabIndex={0}
                aria-selected={selected === r.index}
                onClick={() => open(r.index)}
                onKeyDown={(e) => {
                  if (e.key === "Enter" || e.key === " ") {
                    e.preventDefault();
                    open(r.index);
                  }
                }}
                className={cn("h-7 cursor-pointer border-t hover:bg-hover focus-visible:outline-2 focus-visible:outline-ring", selected === r.index && "bg-selected")}
                data-utterance={r.index}
              >
                <td className="text-muted-foreground tabular-nums">{r.index}</td>
                <td className="tabular-nums">{formatRate(r.wer, 0)}</td>
                <td className="tabular-nums">
                  {r.sub} · {r.del} · {r.ins}
                </td>
                <td className="truncate" dir={dir} title={r.ref}>
                  {r.ref}
                </td>
                <td className="truncate" dir={dir} title={r.hyp}>
                  {r.hyp}
                </td>
                <td>
                  <Button
                    size="icon-xs"
                    variant="ghost"
                    aria-label={`Open utterance ${r.index} in Audio`}
                    onClick={(e) => {
                      e.stopPropagation();
                      select(doc, evalItem(cell.id, r.index));
                      openAudio({ utterance: r.audio, cell: cell.id, hypotheses: cell.hypotheses, scores: cell.scores });
                    }}
                    onKeyDown={(e) => e.stopPropagation()}
                  >
                    <SoundHigh aria-hidden />
                  </Button>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      ) : null}
      {rows.length >= WORST_N ? <p className="text-muted-foreground">The {WORST_N} worst utterances of the cell, most errors first.</p> : null}
    </Section>
  );
}

function Definition({ ev }: { ev: Eval }) {
  const model = (label: string, m: Eval["subject"]) => (
    <div className="contents">
      <dt className="text-muted-foreground">{label}</dt>
      <dd className="min-w-0 break-words">
        {m.kind === "checkpoint" ? m.label : (
          <button type="button" className="underline-offset-2 hover:underline" onClick={() => openDocument(`${m.kind}:${m.id}`)}>
            {m.label}
          </button>
        )}{" "}
        <span className="text-muted-foreground">
          ({m.kind.replace("_", " ")}, {m.family}
          {m.source ? `, ${m.source}` : ""})
        </span>
        {m.runId ? (
          <>
            {" "}
            <button type="button" className="text-primary underline-offset-2 hover:underline" onClick={() => openDocument(`run:${m.runId}`)}>
              run
            </button>
          </>
        ) : null}
        <div>
          <code className="text-[11px] text-muted-foreground">{m.modelKey}</code>
        </div>
      </dd>
    </div>
  );
  return (
    <div className="flex flex-col gap-5 p-4 text-xs">
      <dl className="grid grid-cols-[10rem_1fr] gap-x-4 gap-y-2">
        <dt className="text-muted-foreground">ID</dt>
        <dd>
          <code className="font-mono text-[11px]">{ev.id}</code> · rev {ev.rev}
        </dd>
        <dt className="text-muted-foreground">Started by</dt>
        <dd>
          <ActorBadge actor={ev.actor} /> {new Date(ev.createdAt).toLocaleString()}
        </dd>
        {model("Subject", ev.subject)}
        {model("Baseline", ev.baseline)}
        <dt className="text-muted-foreground">Primary profile</dt>
        <dd>{ev.primaryProfile}</dd>
        <dt className="text-muted-foreground">Profiles</dt>
        <dd>{ev.profiles.map((p) => `${profileLabel(p)} (${p.latencyMs} ms)`).join(", ")}</dd>
        <dt className="text-muted-foreground">Decoding</dt>
        <dd>{ev.decoding.map((d) => decodingLabel(d)).join("; ")}</dd>
        <dt className="text-muted-foreground">Significance</dt>
        <dd className="tabular-nums">
          paired blockwise bootstrap, {ev.significance.samples} samples, {Math.round(ev.significance.level * 100)} % level, seed {ev.significance.seed}
        </dd>
        <dt className="text-muted-foreground">Estimate</dt>
        <dd className="tabular-nums">
          ~{ev.estimate.gpuHours.toFixed(2)} GPU-h for {ev.estimate.cellsToCompute} cells ({ev.estimate.audioHours.toFixed(2)} h of audio × {ev.estimate.gpuHoursPerAudioHour}, {ev.estimate.basis})
        </dd>
        {ev.finishedAt ? (
          <>
            <dt className="text-muted-foreground">Finished</dt>
            <dd>{new Date(ev.finishedAt).toLocaleString()}</dd>
          </>
        ) : null}
      </dl>
      <table className="w-full" aria-label="Golden sets">
        <thead className="text-left text-muted-foreground">
          <tr>
            <th className="font-normal">Golden set</th>
            <th className="font-normal">Locale</th>
            <th className="font-normal">Utterances</th>
            <th className="font-normal">Hours</th>
            <th className="font-normal">Resampled by</th>
          </tr>
        </thead>
        <tbody>
          {ev.goldenSets.map((g) => (
            <tr key={g.versionId} className="border-t">
              <td className="py-0.5">
                <button type="button" className="underline-offset-2 hover:underline" onClick={() => openDocument(`golden_set:${g.versionId}`)}>
                  {g.name}
                </button>{" "}
                <span className="text-muted-foreground">{g.version}</span>
                {g.replay ? <span className="ml-1 rounded-full border px-1.5 text-[11px]">replay</span> : null}
              </td>
              <td>{g.locale}</td>
              <td className="tabular-nums">{g.utterances.toLocaleString()}</td>
              <td className="tabular-nums">{g.hours.toFixed(2)}</td>
              <td>{g.groups}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

function LineageTab({ ev }: { ev: Eval }) {
  return (
    <div className="flex flex-col gap-2 p-4 text-xs">
      <p>
        The subject {ev.subject.kind === "checkpoint" ? "checkpoint" : ev.subject.kind.replace("_", " ")} <span className="font-medium">{ev.subject.label}</span> and what it was built
        from: the Lineage panel follows this report and draws the graph around its subject.
      </p>
      <Button size="xs" variant="outline" className="w-fit" onClick={() => openPanelById("lineage")}>
        Open Lineage
      </Button>
    </div>
  );
}

function Activity({ id }: { id: string }) {
  const topics = `entity.eval.${id}`;
  const q = useQuery(eventsListOptions({ query: { topics, limit: 100 } }));
  useTopic([topics], () => void q.refetch());
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
