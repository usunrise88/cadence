import { useMemo, useRef, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { datasetsListOptions, eventsListOptions, mixesGetQueryKey } from "@/api/gen/@tanstack/react-query.gen";
import type { DraftChange, Mix, MixGroup, Problem } from "@/api/gen/types.gen";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { cn } from "@/lib/utils";
import { DraftOutline, PresenceNotice, changed, presenceLabel, useActivePresence, useDrafts } from "@/shell/entity/drafts";
import { ActorBadge, EmptyState, StatusChip } from "@/shell/entity/primitives";
import { runCommand, useCommand, useEditRequest, useTopic, type PanelProps } from "@/shell/panel";

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
          <NumberField label="Replay share" value={working.replayShare} step={0.05} min={0} max={0.9} disabled={blocked} onChange={(v) => edit({ replayShare: v })} />
          <div className="ml-auto flex gap-1">
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

      <Preview mix={mix} />
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
  return (
    <table data-slot="mix-groups" className="w-full text-xs">
      <thead>
        <tr className="text-left text-muted-foreground">
          <th className="py-1 font-normal">Group</th>
          <th className="font-normal">Dataset versions</th>
          <th className="font-normal">Replay</th>
          <th className="font-normal">Weight</th>
          <th className="font-normal">Sample share</th>
          <th />
        </tr>
      </thead>
      <tbody>
        {groups.map((g, i) => (
          <tr key={`${g.name}-${i}`} className="border-t">
            <td className="py-1 font-medium">{g.name}</td>
            <td className="text-muted-foreground">{g.datasets.map((d) => datasetLabel(names, d)).join(", ")}</td>
            <td>
              <input
                type="checkbox"
                className="size-4 align-middle"
                checked={!!g.replay}
                disabled={disabled}
                aria-label={`Replay group ${g.name}`}
                onChange={(e) => set(i, { replay: e.target.checked })}
              />
            </td>
            <td>
              <Input
                ref={i === 0 ? firstInput : undefined}
                type="number"
                step={0.1}
                min={0.001}
                className="h-6 w-20 text-xs"
                value={g.weight ?? 1}
                disabled={disabled}
                aria-label={`Weight of ${g.name}`}
                onChange={(e) => set(i, { weight: Number(e.target.value) })}
              />
            </td>
            <td className="tabular-nums">{shares.has(g.name) ? `${Math.round((shares.get(g.name) ?? 0) * 1000) / 10} %` : "—"}</td>
            <td className="text-right">
              <Button size="xs" variant="ghost" disabled={disabled || groups.length === 1} aria-label={`Remove group ${g.name}`} onClick={() => onChange(groups.filter((_, j) => j !== i))}>
                Remove
              </Button>
            </td>
          </tr>
        ))}
        <tr className="border-t">
          <td className="py-1">
            <Input className="h-6 w-28 text-xs" placeholder="New group" value={newName} disabled={disabled} aria-label="New group name" onChange={(e) => setNewName(e.target.value)} />
          </td>
          <td colSpan={4}>
            <select className="h-6 rounded-md border bg-background px-1 text-xs" value={newDataset || frozen[0]?.id || ""} disabled={disabled} aria-label="New group dataset version" onChange={(e) => setNewDataset(e.target.value)}>
              {frozen.map((d) => (
                <option key={d.id} value={d.id}>
                  {d.name} · {d.version}
                </option>
              ))}
            </select>
          </td>
          <td className="text-right">
            <Button size="xs" variant="outline" disabled={disabled || !newName} onClick={add}>
              Add group
            </Button>
          </td>
        </tr>
      </tbody>
    </table>
  );
}

function NumberField({ label, value, step, min, max, disabled, onChange }: { label: string; value: number; step: number; min: number; max: number; disabled: boolean; onChange: (v: number) => void }) {
  return (
    <label className="flex flex-col gap-1">
      <span className="text-muted-foreground">{label}</span>
      <Input type="number" className="h-6 w-24 text-xs" value={value} step={step} min={min} max={max} disabled={disabled} aria-label={label} onChange={(e) => onChange(Number(e.target.value))} />
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

function Preview({ mix }: { mix: Mix }) {
  const p = mix.preview;
  return (
    <section aria-labelledby="mix-preview" className="flex flex-col gap-2">
      <h3 id="mix-preview" className="text-[11px] font-medium tracking-wide text-muted-foreground uppercase">
        Preview · hours per language ({p.totalHours} h of training data, from dataset metadata)
      </h3>
      <table data-slot="mix-preview" className="w-full text-xs">
        <thead>
          <tr className="text-left text-muted-foreground">
            <th className="py-1 font-normal">Language</th>
            <th className="font-normal">Train hours</th>
            <th className="w-1/2 font-normal">Sample share</th>
          </tr>
        </thead>
        <tbody>
          {p.languages.map((l) => (
            <tr key={l.locale} className="border-t">
              <td className="py-1 font-medium">{l.locale}</td>
              <td className="tabular-nums">{l.hours}</td>
              <td>
                <span className="flex items-center gap-2">
                  <span className="h-2 flex-1 rounded-full bg-muted">
                    <span className="block h-2 rounded-full bg-accent-line" style={{ width: `${Math.round(l.share * 100)}%` }} />
                  </span>
                  <span className="w-12 text-right tabular-nums">{Math.round(l.share * 1000) / 10} %</span>
                </span>
              </td>
            </tr>
          ))}
        </tbody>
      </table>
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

function Details({ mix }: { mix: Mix }) {
  const rows: [string, string][] = [
    ["id", mix.id],
    ["revision", String(mix.rev)],
    ["created", `${new Date(mix.createdAt).toLocaleString()} by ${mix.createdBy.name ?? mix.createdBy.id}`],
    ["updated", `${new Date(mix.updatedAt).toLocaleString()} by ${mix.updatedBy.name ?? mix.updatedBy.id}`],
    ["cause", mix.cause ? JSON.stringify(mix.cause) : "—"],
    ["groups", JSON.stringify(mix.groups)],
  ];
  return (
    <dl className="grid grid-cols-[9rem_1fr] gap-x-4 gap-y-1.5 p-4 text-xs">
      {rows.map(([k, v]) => (
        <div key={k} className="contents">
          <dt className="text-muted-foreground">{k}</dt>
          <dd className="break-all">{k === "revision" ? <StatusChip state="active" /> : null} {v}</dd>
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
