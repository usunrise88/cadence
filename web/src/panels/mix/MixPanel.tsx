import { useEffect, useId, useMemo, useRef, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Play, Plus, Trash } from "iconoir-react";
import { datasetsListOptions, eventsListOptions, mixesGetQueryKey, projectsGetOptions } from "@/api/gen/@tanstack/react-query.gen";
import type { DraftChange, Mix, MixGroup, MixPreview, Problem, RunEstimate, RunNew } from "@/api/gen/types.gen";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { NativeSelect } from "@/components/ui/native-select";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import { AnalyticsChart, type AnalyticsSpec } from "@/shell/charts";
import { cn } from "@/lib/utils";
import { DraftOutline, PresenceNotice, changed, presenceLabel, useActivePresence, useDrafts } from "@/shell/entity/drafts";
import { ActorBadge, EmptyState } from "@/shell/entity/primitives";
import { errorMessage, lookupDefault, materializeOf, NeedsMaterialize, openDocument, rangeWarning, runCommand, useCommand, useDefaults, useEditRequest, useKeyedEstimate, useProject, useTopic, WhyDefault, type PanelProps } from "@/shell/panel";

// The Mix document (docs/spec/11-ui-panels.md): groups, weights, temperature and replay share over dataset
// versions, and the preview of hours per language. A person edits the table directly (a new revision); an agent's
// edits arrive live as drafts above it, which the person accepts or reverts; while an agent is editing, the table
// waits. A save on a revision that moved on shows the conflict notice instead of overwriting.

export function MixEmpty() {
  const cmd = useCommand("mixes.new");
  return (
    <EmptyState
      step="prepare"
      title="No mix open"
      hint="Open a mix from the Library, or create a new one."
      action={
        cmd ? (
          <Button type="button" size="sm" disabled={cmd.enabled !== true} onClick={() => void cmd.run()}>
            New mix
          </Button>
        ) : undefined
      }
    />
  );
}

export function MixPanel({ tab, entity, doc }: PanelProps) {
  const mix = entity?.mix as Mix | undefined;
  if (!entity || !mix) return <MixEmpty />;
  switch (tab) {
    case "details":
      return <Details mix={mix} />;
    case "activity":
      return <Activity mixId={mix.id} />;
    case "lineage":
      return <EmptyState step="record" title="Lineage arrives with training" hint="Runs record the mix revision they trained on (phase 2); dataset versions link back here." />;
    case "notes":
      return <EmptyState step="record" title="No notes yet" hint="Notes are dated learnings committed to the project repository." />;
    default:
      return <Overview mix={mix} doc={doc} />;
  }
}

type DatasetInfo = { name: string; version: string };
type Local = { groups?: MixGroup[]; temperature?: number; replayShare?: number };

function useDatasetNames(mix: Mix) {
  const { data } = useQuery(datasetsListOptions({ query: { state: "frozen" } }));
  return useMemo(() => {
    const m = new Map<string, DatasetInfo>();
    for (const d of data?.items ?? []) m.set(d.id, { name: d.name, version: d.version });
    for (const d of mix.preview.datasets) m.set(d.id, { name: d.name, version: d.version });
    return { names: m, frozen: data?.items ?? [] };
  }, [data, mix.preview.datasets]);
}

function isProblem(err: unknown): err is { problem: Problem; status: number } {
  return !!err && typeof err === "object" && "problem" in err && "status" in err;
}

function Overview({ mix, doc }: { mix: Mix; doc?: string }) {
  const qc = useQueryClient();
  const { drafts } = useDrafts("mix", mix.id);
  const editing = useActivePresence(mix.presence);
  const blocked = editing.length > 0;
  const { names, frozen } = useDatasetNames(mix);
  const [local, setLocal] = useState<Local | null>(null);
  const [base, setBase] = useState(mix.rev);
  const [conflict, setConflict] = useState<{ currentRev: number } | null>(null);
  const [problem, setProblem] = useState<Problem | null>(null);
  const [saving, setSaving] = useState(false);
  const [launch, setLaunch] = useState(false);
  const first = useRef<HTMLInputElement>(null);
  useEditRequest(doc, () => first.current?.focus());

  const working = {
    groups: local?.groups ?? mix.groups,
    temperature: local?.temperature ?? mix.temperature,
    replayShare: local?.replayShare ?? mix.replayShare,
  };
  const dirty = !!local && Object.keys(local).length > 0;
  const edit = (patch: Local) => {
    if (!local) setBase(mix.rev);
    setLocal({ ...(local ?? {}), ...patch });
  };
  const key = mixesGetQueryKey({ path: { id: mix.id } });

  const save = async () => {
    if (!local) return;
    setSaving(true);
    setProblem(null);
    try {
      const res = await runCommand("mixes.edit", { mix: { id: mix.id, rev: base }, patch: local });
      if (res?.mix) qc.setQueryData(key, res.mix);
      setLocal(null);
      setConflict(null);
    } catch (err) {
      if (isProblem(err) && err.status === 412) {
        setConflict({ currentRev: err.problem.currentRev ?? mix.rev });
        void qc.invalidateQueries({ queryKey: key });
      } else if (isProblem(err)) {
        setProblem(err.problem);
      } else {
        setProblem({ type: "about:blank", title: "Save failed", status: 0, detail: err instanceof Error ? err.message : String(err) });
      }
    } finally {
      setSaving(false);
    }
  };
  const discard = () => {
    setLocal(null);
    setConflict(null);
    setProblem(null);
  };
  const reapply = () => {
    setBase(Math.max(conflict?.currentRev ?? mix.rev, mix.rev));
    setConflict(null);
  };
  const reason = blocked ? `${presenceLabel(editing)} is editing this mix — accept or revert its draft first` : undefined;

  return (
    <div className="flex flex-col gap-5 p-4">
      <PresenceNotice presence={editing} noun="mix" />
      {drafts.map((d) => (
        <DraftOutline key={d.id} draft={d} noun="mix">
          <MixContent content={d.content as Partial<Mix>} names={names} changes={d.changes} />
        </DraftOutline>
      ))}

      <section aria-labelledby="mix-current" className="flex flex-col gap-2">
        <div className="flex items-center gap-2">
          <h3 id="mix-current" className="text-[11px] font-medium tracking-wide text-muted-foreground uppercase">
            Groups · rev {mix.rev}
          </h3>
          <ActorBadge actor={mix.cause?.draftAuthor ?? mix.updatedBy} toolCallId={mix.cause?.toolCallId} />
          {mix.cause?.draftAuthor ? <span className="text-xs text-muted-foreground">accepted by {mix.updatedBy.name ?? mix.updatedBy.id}</span> : null}
        </div>
        <GroupsTable
          groups={working.groups}
          names={names}
          shares={new Map(mix.preview.groups.map((g) => [g.name, g.share]))}
          disabled={blocked}
          firstInput={first}
          onChange={(groups) => edit({ groups })}
          frozen={frozen}
        />
        <div className="flex flex-wrap items-end gap-4 text-xs">
          <NumberField label="Temperature" value={working.temperature} step={0.1} min={0.1} max={10} disabled={blocked} onChange={(v) => edit({ temperature: v })} />
          <ReplayShare value={working.replayShare} hasReplay={working.groups.some((g) => g.replay)} disabled={blocked} onChange={(v) => edit({ replayShare: v })} />
          <div className="ml-auto flex flex-wrap items-center gap-1">
            <LaunchRun dirty={dirty} open={launch} onOpen={() => setLaunch(true)} />
            <Button size="xs" variant="outline" disabled={!dirty || saving} onClick={discard}>
              Discard
            </Button>
            <Button size="xs" disabled={!dirty || saving || blocked || !!conflict} onClick={() => void save()} aria-label="Save mix" title={reason} data-command="mixes.edit">
              Save
            </Button>
          </div>
        </div>
        {conflict ? (
          <div role="alert" data-slot="conflict" className="flex flex-wrap items-center gap-2 border-l-2 border-status-warning py-1 pl-2 text-xs">
            <span className="text-status-warning-foreground">
              Conflict: the mix moved to rev {Math.max(conflict.currentRev, mix.rev)} while you edited rev {base}; nothing was overwritten.
            </span>
            <ActorBadge actor={mix.cause?.draftAuthor ?? mix.updatedBy} toolCallId={mix.cause?.toolCallId} />
            <span className="ml-auto flex gap-1">
              <Button size="xs" variant="outline" onClick={discard}>
                Reload
              </Button>
              <Button size="xs" onClick={reapply}>
                Reapply my changes
              </Button>
            </span>
          </div>
        ) : null}
        {problem ? (
          <div role="alert" className="border-l-2 border-status-failed py-1 pl-2 text-xs text-status-failed-foreground">
            {problem.detail ?? problem.title}
            {(problem.errors ?? []).map((e) => (
              <div key={e.path}>
                <code>{e.path}</code> {e.message}
              </div>
            ))}
          </div>
        ) : null}
      </section>

      {launch ? <RunLaunch mix={mix} onClose={() => setLaunch(false)} /> : null}

      <Preview mix={mix} working={local ?? {}} dirty={dirty} />
    </div>
  );
}

function datasetLabel(names: Map<string, DatasetInfo>, id: string): string {
  const d = names.get(id);
  return d ? d.name.replace(/^dataset\//, "") : id.slice(0, 12);
}

function GroupsTable({
  groups,
  names,
  shares,
  disabled,
  firstInput,
  onChange,
  frozen,
}: {
  groups: MixGroup[];
  names: Map<string, DatasetInfo>;
  shares: Map<string, number>;
  disabled: boolean;
  firstInput: React.RefObject<HTMLInputElement | null>;
  onChange: (groups: MixGroup[]) => void;
  frozen: { id: string; name: string; version: string }[];
}) {
  const set = (i: number, g: Partial<MixGroup>) => onChange(groups.map((x, j) => (j === i ? { ...x, ...g } : x)));
  const [newName, setNewName] = useState("");
  const [newDataset, setNewDataset] = useState("");
  const add = () => {
    const ds = newDataset || frozen[0]?.id;
    if (!newName || !ds) return;
    onChange([...groups, { name: newName, weight: 1, replay: false, datasets: [ds] }]);
    setNewName("");
  };
  const cell = "px-2 py-1.5 align-middle first:pl-0 last:pr-0";
  return (
    <div className="flex flex-col gap-2">
      <div className="overflow-x-auto">
        <table data-slot="mix-groups" className="w-full min-w-md text-xs">
          <thead>
            <tr className="text-left text-muted-foreground">
              <th className={cn(cell, "font-normal")}>Group</th>
              <th className={cn(cell, "font-normal")}>Dataset versions</th>
              <th className={cn(cell, "text-center font-normal")}>Replay</th>
              <th className={cn(cell, "font-normal")}>Weight</th>
              <th className={cn(cell, "text-right font-normal")}>Sample share</th>
              <th className={cell}>
                <span className="sr-only">Remove</span>
              </th>
            </tr>
          </thead>
          <tbody>
            {groups.map((g, i) => (
              <tr key={`${g.name}-${i}`} className="border-t">
                <td className={cn(cell, "font-medium whitespace-nowrap")}>{g.name}</td>
                <td className={cn(cell, "text-muted-foreground")}>{g.datasets.map((d) => datasetLabel(names, d)).join(", ")}</td>
                <td className={cn(cell, "text-center")}>
                  <input
                    type="checkbox"
                    className="size-4 align-middle accent-primary"
                    checked={!!g.replay}
                    disabled={disabled}
                    aria-label={`Replay group ${g.name}`}
                    onChange={(e) => set(i, { replay: e.target.checked })}
                  />
                </td>
                <td className={cell}>
                  <Input
                    ref={i === 0 ? firstInput : undefined}
                    type="number"
                    step={0.1}
                    min={0.001}
                    className="w-20 text-xs tabular-nums"
                    value={g.weight ?? 1}
                    disabled={disabled}
                    aria-label={`Weight of ${g.name}`}
                    onChange={(e) => set(i, { weight: Number(e.target.value) })}
                  />
                </td>
                <td className={cn(cell, "text-right whitespace-nowrap tabular-nums")}>{shares.has(g.name) ? `${Math.round((shares.get(g.name) ?? 0) * 1000) / 10} %` : "—"}</td>
                <td className={cn(cell, "w-8 text-right")}>
                  <Button
                    size="icon-xs"
                    variant="ghost"
                    className="size-6 text-muted-foreground hover:text-destructive"
                    disabled={disabled || groups.length === 1}
                    aria-label={`Remove group ${g.name}`}
                    title={groups.length === 1 ? "A mix keeps at least one group" : `Remove group ${g.name}`}
                    onClick={() => onChange(groups.filter((_, j) => j !== i))}
                  >
                    <Trash aria-hidden />
                  </Button>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
      <div className="flex flex-wrap items-center gap-2 text-xs" role="group" aria-label="Add a group">
        <Input className="w-32 text-xs" placeholder="New group" value={newName} disabled={disabled} aria-label="New group name" onChange={(e) => setNewName(e.target.value)} onKeyDown={(e) => e.key === "Enter" && add()} />
        <NativeSelect
          className="w-auto max-w-full min-w-40 flex-1 text-xs"
          value={newDataset || frozen[0]?.id || ""}
          disabled={disabled}
          aria-label="New group dataset version"
          onChange={(e) => setNewDataset(e.target.value)}
        >
          {frozen.map((d) => (
            <option key={d.id} value={d.id}>
              {d.name.replace(/^dataset\//, "")} · {d.version}
            </option>
          ))}
        </NativeSelect>
        <Button size="xs" variant="outline" disabled={disabled || !newName} onClick={add}>
          <Plus aria-hidden />
          Add group
        </Button>
      </div>
    </div>
  );
}

function NumberField({ label, value, step, min, max, disabled, onChange }: { label: string; value: number; step: number; min: number; max: number; disabled: boolean; onChange: (v: number) => void }) {
  return (
    <label className="flex flex-col gap-1">
      <span className="text-muted-foreground">{label}</span>
      <Input type="number" className="w-24 text-xs tabular-nums" value={value} step={step} min={min} max={max} disabled={disabled} aria-label={label} onChange={(e) => onChange(Number(e.target.value))} />
    </label>
  );
}

/** A draft's proposed content, with the cells it changes marked (diff added). */
function MixContent({ content, names, changes }: { content: Partial<Mix>; names: Map<string, DatasetInfo>; changes: DraftChange[] }) {
  const hl = (path: string) => (changed(changes, path) ? "rounded bg-diff-added px-1 text-diff-added-foreground" : undefined);
  return (
    <div className="flex flex-col gap-1 text-xs">
      <table className="w-full">
        <tbody>
          {(content.groups ?? []).map((g, i) => (
            <tr key={`${g.name}-${i}`} className={cn("border-t first:border-t-0", changed(changes, `/groups/${i}`) && changes.some((c) => c.path === `/groups/${i}`) && "bg-diff-added text-diff-added-foreground")}>
              <td className="py-0.5 font-medium">
                <span className={hl(`/groups/${i}/name`)}>{g.name}</span>
              </td>
              <td className="text-muted-foreground">
                <span className={hl(`/groups/${i}/datasets`)}>{g.datasets.map((d) => datasetLabel(names, d)).join(", ")}</span>
              </td>
              <td>
                <span className={hl(`/groups/${i}/replay`)}>{g.replay ? "replay" : "target"}</span>
              </td>
              <td className="tabular-nums">
                weight <span className={hl(`/groups/${i}/weight`)}>{g.weight}</span>
              </td>
            </tr>
          ))}
        </tbody>
      </table>
      <p className="text-muted-foreground">
        Temperature <span className={hl("/temperature")}>{content.temperature}</span> · replay share <span className={hl("/replayShare")}>{content.replayShare}</span>
        {content.name ? (
          <>
            {" "}
            · name <span className={hl("/name")}>{content.name}</span>
          </>
        ) : null}
      </p>
    </div>
  );
}

export type PreviewBy = "language" | "source" | "group";
const PREVIEW_BY: { id: PreviewBy; label: string }[] = [
  { id: "language", label: "Language" },
  { id: "source", label: "Source" },
  { id: "group", label: "Group" },
];

const pct = (v: number) => `${Math.round(v * 1000) / 10} %`;
const hours = (v: number) => Math.round(v * 100) / 100;

/** Rows of the preview for one grouping: label, train hours and (where the preview has it) sample share. */
export function previewRows(p: MixPreview, by: PreviewBy): { label: string; hours: number; share?: number; note?: string }[] {
  if (by === "language") return p.languages.map((l) => ({ label: l.locale, hours: l.hours, share: l.share }));
  if (by === "group") return p.groups.map((g) => ({ label: g.name, hours: g.hours, share: g.share, note: g.replay ? "replay" : undefined }));
  return p.datasets.map((d) => ({ label: `${d.name.replace(/^dataset\//, "")} · ${d.version}`, hours: d.hours, note: d.adopted ? undefined : "not adopted" }));
}

/**
 * The preview of hours per language, source and group. While the person edits, it is recomputed from the unsaved
 * values (mixes.preview: metadata only, nothing saved); otherwise it is the saved revision's.
 */
function Preview({ mix, working, dirty }: { mix: Mix; working: Local; dirty: boolean }) {
  const project = useProject();
  const [by, setBy] = useState<PreviewBy>("language");
  const [live, setLive] = useState<{ key: string; preview: MixPreview } | null>(null);
  const [error, setError] = useState<string | null>(null);
  const body = useMemo(
    () => ({ name: mix.name, groups: working.groups ?? mix.groups, temperature: working.temperature ?? mix.temperature, replayShare: working.replayShare ?? mix.replayShare }),
    [mix, working],
  );
  const key = JSON.stringify(body);
  useEffect(() => {
    if (!dirty || !project) return;
    const t = setTimeout(() => {
      runCommand("mixes.preview", { project, body })
        .then((preview) => {
          setLive({ key, preview });
          setError(null);
        })
        .catch((err: unknown) => setError(errorMessage(err)));
    }, 400);
    return () => clearTimeout(t);
    // body is derived from key
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [dirty, project, key]);
  const p = dirty && live?.key === key ? live.preview : mix.preview;
  const pending = dirty && live?.key !== key;
  const rows = previewRows(p, by);
  const spec: AnalyticsSpec = {
    kind: "bar",
    title: `Train hours per ${by}`,
    categories: rows.map((r) => r.label),
    series: [{ id: "hours", label: "Train hours", slot: 0, values: rows.map((r) => hours(r.hours)) }],
    horizontal: true,
    unit: "h",
    xLabel: "Train hours",
  };
  return (
    <section aria-labelledby="mix-preview" className="flex flex-col gap-2" data-preview={dirty ? (pending ? "pending" : "unsaved") : "saved"}>
      <div className="flex flex-wrap items-center gap-2">
        <h3 id="mix-preview" className="text-[11px] font-medium tracking-wide text-muted-foreground uppercase">
          Preview · {hours(p.totalHours)} h of training data, from dataset metadata
        </h3>
        {dirty ? <span className="text-xs text-muted-foreground">{pending ? "updating for your changes…" : "for your unsaved changes"}</span> : null}
        <div role="radiogroup" aria-label="Preview by" className="ml-auto inline-flex rounded-md border bg-background p-0.5 text-xs">
          {PREVIEW_BY.map((o) => (
            <button
              key={o.id}
              type="button"
              role="radio"
              aria-checked={by === o.id}
              onClick={() => setBy(o.id)}
              className={cn("h-6 rounded-[4px] px-2.5", by === o.id ? "bg-selected font-medium text-foreground" : "text-muted-foreground hover:text-foreground")}
            >
              {o.label}
            </button>
          ))}
        </div>
      </div>
      {rows.length ? <AnalyticsChart spec={spec} height={Math.min(320, 64 + rows.length * 28)} hideTitle /> : null}
      <table data-slot="mix-preview" className="w-full text-xs">
        <thead>
          <tr className="text-left text-muted-foreground">
            <th className="py-1 font-normal">{PREVIEW_BY.find((o) => o.id === by)?.label}</th>
            <th className="font-normal">Train hours</th>
            <th className="w-1/2 font-normal">{by === "source" ? "" : "Sample share"}</th>
          </tr>
        </thead>
        <tbody>
          {rows.map((r) => (
            <tr key={r.label} className="border-t">
              <td className="py-1 font-medium">
                {r.label}
                {r.note ? <span className="ml-1 rounded-full border px-1.5 text-[11px] font-normal text-muted-foreground">{r.note}</span> : null}
              </td>
              <td className="tabular-nums">{hours(r.hours)}</td>
              <td>
                {r.share !== undefined ? (
                  <span className="flex items-center gap-2">
                    <span className="h-2 flex-1 rounded-full bg-muted">
                      <span className="block h-2 rounded-full bg-accent-line" style={{ width: `${Math.round(r.share * 100)}%` }} />
                    </span>
                    <span className="w-12 text-right tabular-nums">{pct(r.share)}</span>
                  </span>
                ) : null}
              </td>
            </tr>
          ))}
        </tbody>
      </table>
      {error ? (
        <p role="alert" className="text-xs text-destructive">
          Preview failed: {error}
        </p>
      ) : null}
      {p.warnings.length > 0 ? (
        <ul className="flex flex-col gap-0.5 text-xs text-status-warning-foreground">
          {p.warnings.map((w) => (
            <li key={w}>{w}</li>
          ))}
        </ul>
      ) : null}
    </section>
  );
}

/** Replay share: a slider with the number beside it, its default and "Why this default?" (defaults.yaml mix.replay_share). */
function ReplayShare({ value, hasReplay, disabled, onChange }: { value: number; hasReplay: boolean; disabled: boolean; onChange: (v: number) => void }) {
  const defaults = useDefaults();
  const def = lookupDefault(defaults.data, "mix.replay_share");
  const warn = rangeWarning(value, def?.range);
  const departs = def !== undefined && hasReplay && Number(def.value) !== value;
  const id = useId();
  return (
    <div className="flex flex-col gap-1" data-slot="replay-share">
      <div className="flex items-center gap-1">
        <label htmlFor={id} className="text-muted-foreground">
          Replay share
        </label>
        <WhyDefault label="replay share" value={def} />
      </div>
      <div className="flex items-center gap-2">
        <input
          type="range"
          aria-label="Replay share slider"
          min={0}
          max={0.9}
          step={0.01}
          value={value}
          disabled={disabled}
          aria-valuetext={pct(value)}
          className="h-6 w-36 accent-primary"
          onChange={(e) => onChange(Number(e.target.value))}
        />
        <Input id={id} type="number" className="w-20 text-xs tabular-nums" value={value} step={0.05} min={0} max={0.9} disabled={disabled} onChange={(e) => onChange(Number(e.target.value))} />
        <span className="w-12 text-muted-foreground tabular-nums">{pct(value)}</span>
      </div>
      {!hasReplay ? <span className="text-muted-foreground">No group is marked replay, so no replay samples are drawn.</span> : null}
      {departs ? <span className="text-muted-foreground">Departs from the default ({pct(Number(def.value))}).</span> : null}
      {warn ? <span className="text-status-warning-foreground">{warn}</span> : null}
    </div>
  );
}

/**
 * "Launch a run with this mix" (docs/spec/11-ui-panels.md, Mix, phase 2): opens the launch card, which asks for the
 * estimate first (runs.new?dryRun=true) and starts the run only when the person confirms it.
 */
function LaunchRun({ dirty, open, onOpen }: { dirty: boolean; open: boolean; onOpen: () => void }) {
  const cmd = useCommand("runs.new");
  const reason = !cmd ? "Arrives with runs" : dirty ? "Save the mix first: a run trains on a saved revision" : cmd.enabled === true ? undefined : cmd.enabled;
  const button = (
    <Button size="xs" variant="outline" disabled={!!reason} aria-expanded={open} onClick={onOpen} data-command="runs.new">
      <Play aria-hidden />
      Launch a run with this mix
    </Button>
  );
  if (!reason) return button;
  return (
    <Tooltip>
      <TooltipTrigger render={<span tabIndex={0} className="inline-flex rounded-md" aria-label={`Launch a run with this mix: ${reason}`} data-slot="launch-run" />}>{button}</TooltipTrigger>
      <TooltipContent>{reason}</TooltipContent>
    </Tooltip>
  );
}

const hoursRange = (r: { value: number; low: number; high: number }, unit: string, f = (v: number) => (Math.round(v * 100) / 100).toString()) =>
  `${f(r.value)} ${unit} (${f(r.low)}–${f(r.high)})`;
const minutes = (s: number) => (s < 3600 ? `${Math.round(s / 60)} min` : `${Math.round((s / 3600) * 10) / 10} h`);

/** The launch card: the estimate of a run on this mix revision, then Start run (runs.new). */
function RunLaunch({ mix, onClose }: { mix: Mix; onClose: () => void }) {
  const project = useProject();
  // The run trains the project's base model (the wizard's choice), not the instance default.
  const projectQuery = useQuery({ ...projectsGetOptions({ path: { p: project ?? "" } }), enabled: !!project });
  const base = projectQuery.data?.baseModel?.versionId;
  const [steps, setSteps] = useState("");
  const [busy, setBusy] = useState(false);
  const [message, setMessage] = useState<{ error: boolean; text: string } | null>(null);
  const body: RunNew = { mix: mix.id, mixRevision: mix.rev, ...(base ? { baseModel: base } : {}), ...(Number(steps) > 0 ? { steps: Math.round(Number(steps)) } : {}) };
  // Start sends exactly the body the shown estimate answered; a changed step count (or base model, or mix revision)
  // makes it stale until estimated again.
  const est = useKeyedEstimate<RunEstimate>(JSON.stringify(body));
  const estimate = est.estimate;
  const act = async (dryRun: boolean) => {
    if (!project || (!dryRun && !est.fresh)) return;
    const ticket = dryRun ? est.begin() : 0;
    let answer: RunEstimate | undefined;
    setBusy(true);
    setMessage(null);
    try {
      const res = await runCommand("runs.new", { project, body, dryRun });
      if ("approvalId" in res) setMessage({ error: false, text: `The run waits for an approval (${res.approvalId}); it starts when a person approves it in Approvals.` });
      else if ("basis" in res) answer = res;
      else {
        openDocument(`run:${res.id}`);
        onClose();
      }
    } catch (err) {
      setMessage({ error: true, text: errorMessage(err) });
    } finally {
      if (dryRun) est.settle(ticket, answer);
      setBusy(false);
    }
  };
  // The estimate is asked for once when the card opens, after the project (its base model) has loaded; Estimate
  // again re-asks with the edited steps.
  const ready = !!project && !projectQuery.isPending;
  const asked = useRef(false);
  useEffect(() => {
    if (!ready || asked.current) return;
    asked.current = true;
    void act(true);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [ready]);
  return (
    <section aria-labelledby="mix-launch" className="flex flex-col gap-2 rounded-md border bg-tool p-3 text-xs" data-slot="run-launch">
      <div className="flex items-center gap-2">
        <h3 id="mix-launch" className="text-[11px] font-medium tracking-wide text-muted-foreground uppercase">
          New run · {mix.name} rev {mix.rev}
        </h3>
        <Button size="xs" variant="ghost" className="ml-auto" onClick={onClose}>
          Close
        </Button>
      </div>
      <label className="flex items-center gap-2">
        <span className="text-muted-foreground">Steps</span>
        <Input type="number" min={1} className="h-6 w-28 text-xs tabular-nums" placeholder={estimate ? String(estimate.steps) : "default"} value={steps} onChange={(e) => setSteps(e.target.value)} />
        <Button size="xs" variant="outline" disabled={busy} onClick={() => void act(true)}>
          Estimate again
        </Button>
      </label>
      {estimate ? (
        <dl className={cn("grid grid-cols-[8rem_1fr] gap-x-3 gap-y-0.5", est.stale && "text-muted-foreground opacity-60")} data-slot="run-estimate" data-stale={est.stale || undefined}>
          <dt className="text-muted-foreground">GPU-hours</dt>
          <dd className="tabular-nums">{hoursRange(estimate.gpuHours, "GPU-h")}</dd>
          <dt className="text-muted-foreground">Duration</dt>
          <dd className="tabular-nums">{hoursRange(estimate.durationSeconds, "", minutes)}</dd>
          <dt className="text-muted-foreground">Steps</dt>
          <dd className="tabular-nums">
            {estimate.steps} × {Math.round(estimate.secondsPerStep * 100) / 100} s ({estimate.basis === "measured" ? "measured by calibration" : "from the estimate table"})
          </dd>
          <dt className="text-muted-foreground">Card</dt>
          <dd>
            {estimate.card.host} #{estimate.card.index} · {estimate.card.cardClass}, cap {estimate.card.memoryCapGb} GB
          </dd>
          <dt className="text-muted-foreground">Data</dt>
          <dd className="tabular-nums">{Math.round(estimate.data.hours * 100) / 100} h</dd>
          <dt className="text-muted-foreground">Today's budget</dt>
          <dd className={estimate.budget.withinDailyBudget ? undefined : "text-status-warning-foreground"}>
            {estimate.budget.remainingGpuHours !== undefined ? `${Math.round(estimate.budget.remainingGpuHours * 100) / 100} of ${estimate.budget.gpuHoursPerProjectPerDay} GPU-h left` : `${estimate.budget.gpuHoursPerProjectPerDay} GPU-h per day`}
            {estimate.budget.withinDailyBudget ? "" : " — over today's budget"}
          </dd>
          <dt className="text-muted-foreground">Source</dt>
          <dd className="text-muted-foreground">{estimate.source}</dd>
        </dl>
      ) : busy ? (
        <p className="text-muted-foreground">Estimating…</p>
      ) : null}
      {estimate ? <NeedsMaterialize items={materializeOf(estimate.warnings)} onRestored={() => void act(true)} /> : null}
      {est.stale ? (
        <p className="text-muted-foreground" data-slot="run-estimate-stale">
          {est.pending ? "Estimating…" : "The form changed since this estimate: estimate again to start."}
        </p>
      ) : null}
      <div className="flex gap-1">
        <Button size="xs" disabled={busy || !est.fresh} onClick={() => void act(false)} data-command="runs.new">
          <Play aria-hidden />
          Start run
        </Button>
      </div>
      {message ? (
        <p role={message.error ? "alert" : "status"} className={message.error ? "text-destructive" : "text-muted-foreground"}>
          {message.text}
        </p>
      ) : null}
    </section>
  );
}

function Details({ mix }: { mix: Mix }) {
  const { names } = useDatasetNames(mix);
  const when = (iso: string) => new Date(iso).toLocaleString();
  const rows: [string, React.ReactNode][] = [
    ["ID", <code className="font-mono text-[11px]">{mix.id}</code>],
    ["Revision", `rev ${mix.rev}`],
    [
      "Created",
      <span className="inline-flex flex-wrap items-center gap-1">
        {when(mix.createdAt)} <ActorBadge actor={mix.createdBy} />
      </span>,
    ],
    [
      "Updated",
      <span className="inline-flex flex-wrap items-center gap-1">
        {when(mix.updatedAt)} <ActorBadge actor={mix.updatedBy} />
      </span>,
    ],
    ...(mix.cause?.draftAuthor
      ? ([
          [
            "From a draft by",
            <ActorBadge actor={mix.cause.draftAuthor} toolCallId={mix.cause.toolCallId} />,
          ],
        ] as [string, React.ReactNode][])
      : []),
    ["Temperature", String(mix.temperature)],
    ["Replay share", `${Math.round(mix.replayShare * 1000) / 10} %`],
    [
      "Groups",
      <ul className="flex flex-col gap-1">
        {mix.groups.map((g) => (
          <li key={g.name} className="flex flex-wrap items-baseline gap-x-2">
            <span className="font-medium">{g.name}</span>
            <span className="text-muted-foreground">{g.datasets.map((d) => datasetLabel(names, d)).join(", ")}</span>
            <span className="tabular-nums">weight {g.weight ?? 1}</span>
            {g.replay ? <span className="rounded-full border px-1.5 text-[11px] text-muted-foreground">replay</span> : null}
          </li>
        ))}
      </ul>,
    ],
  ];
  return (
    <dl className="grid grid-cols-[8rem_1fr] gap-x-4 gap-y-2 p-4 text-xs">
      {rows.map(([k, v]) => (
        <div key={k} className="contents">
          <dt className="text-muted-foreground">{k}</dt>
          <dd className="min-w-0 break-words">{v}</dd>
        </div>
      ))}
    </dl>
  );
}

function Activity({ mixId }: { mixId: string }) {
  const topic = `entity.mix.${mixId}`;
  const q = useQuery(eventsListOptions({ query: { topics: topic, limit: 100 } }));
  useTopic([topic], () => void q.refetch());
  const items = [...(q.data?.items ?? [])].reverse();
  if (items.length === 0) return <EmptyState step="record" title="No activity yet" />;
  return (
    <ol className="flex flex-col px-4 py-2 text-xs">
      {items.map((e) => (
        <li key={e.seq} className="flex h-8 items-center gap-3 border-b last:border-0">
          <time className="text-muted-foreground tabular-nums">{new Date(e.at).toLocaleString()}</time>
          <span className="font-medium">{e.type}</span>
          <ActorBadge actor={e.actor} toolCallId={e.causedBy?.toolCallId} />
          {e.causedBy?.draftId ? <span className="text-muted-foreground">from {e.causedBy.draftId.slice(0, 12)}…</span> : null}
          {e.entity ? <span className="ml-auto text-muted-foreground">rev {e.entity.rev}</span> : null}
        </li>
      ))}
    </ol>
  );
}
