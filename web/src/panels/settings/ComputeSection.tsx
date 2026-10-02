import { useRef, useState, type FormEvent } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { EditPencil } from "iconoir-react";
import { computeGetQueryKey, computeListOptions, computeListQueryKey } from "@/api/gen/@tanstack/react-query.gen";
import type { AvailabilityWindows, ComputeCard, ComputeCardEdit, ComputeHost, ComputeList, DefaultHost, DefaultValue, JobKind } from "@/api/gen/types.gen";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { cn } from "@/lib/utils";
import { errorMessage, formatWindows, problemOf, rangeWarning, runCommand, useDefaults, useTopic, WhyDefault, windowError } from "@/shell/panel";
import { Chip, describedBy, Field, FieldGroup, SectionHeading, Table, Td, when } from "./ui";
import { windowRows, WindowsEditor } from "./WindowsEditor";

// Compute (docs/spec/11-ui-panels.md "Settings"): hosts, cards, memory caps and allowed job kinds. The read view is a
// compact table per host; a card is edited in its own dialog (identity, memory, job kinds, availability windows).
// Edits carry If-Match with the revision the edit started from; a change made meanwhile (another tab, an agent)
// answers 412 and the dialog shows both instead of overwriting (docs/spec/11 "Risks": revision conflicts, not silent
// overwrites). Availability windows per card and job kind (R19) are edited here too; Queue & GPU shows them.

export const JOB_KINDS: JobKind[] = ["training", "eval", "shadow", "export", "data", "interactive"];

/** One line per job kind in the card dialog (scheduling rules: docs/review/2026-09-30-phase-2-plan.md, R19, R27). */
export const JOB_KIND_HINT: Record<JobKind, string> = {
  training: "Fine-tuning runs. One at a time per card: they hold its training slot.",
  eval: "Evaluations. Share the card beside training when their memory fits.",
  shadow: "Shadow replay of production audio against a candidate; throughput work that can share.",
  export: "Turns a checkpoint into a deployable model.",
  data: "Imports, preparation and augmentation. Share the card when their memory fits.",
  interactive: "Live transcription tests. Highest priority; beside training under the cap, never beside a benchmark (R49).",
};

// A card's name, class and memory are editable too: a host seeded with the wrong card (an older defaults.yaml) is
// corrected here, and the class keys the estimate table.
type CardDraft = {
  memoryCapGb: string;
  allowedJobKinds: JobKind[];
  windows?: AvailabilityWindows;
  name?: string;
  cardClass?: string;
  memoryGb?: string;
};

const CARD_CLASS = /^[a-z0-9][a-z0-9-]{0,62}$/;

/** Why a card draft cannot be saved, or undefined. */
export function cardDraftError(d: CardDraft, c: ComputeCard): string | undefined {
  const mem = d.memoryGb === undefined ? c.memoryGb : Number(d.memoryGb);
  const cap = Number(d.memoryCapGb);
  if (!(mem > 0)) return "memory must be above 0";
  if (!(cap > 0)) return "memory cap must be above 0";
  if (cap > mem) return "the cap cannot exceed the card's memory";
  if (d.cardClass !== undefined && !CARD_CLASS.test(d.cardClass)) return "class: lowercase letters, digits and dashes";
  if (d.name !== undefined && !d.name.trim()) return "name is required";
  return undefined;
}

type CardField = "name" | "cardClass" | "memoryGb" | "memoryCapGb";

/** The same rules as cardDraftError, per field, so the dialog shows each message under its own field. */
export function cardFieldErrors(d: CardDraft, c: ComputeCard): Partial<Record<CardField, string>> {
  const out: Partial<Record<CardField, string>> = {};
  const mem = d.memoryGb === undefined ? c.memoryGb : Number(d.memoryGb);
  const cap = Number(d.memoryCapGb);
  if (d.name !== undefined && !d.name.trim()) out.name = "A name is required";
  if (d.cardClass !== undefined && !CARD_CLASS.test(d.cardClass)) out.cardClass = "Lowercase letters, digits and dashes, starting with a letter or digit (up to 63)";
  if (!(mem > 0)) out.memoryGb = "Must be above 0 GB";
  if (!(cap > 0)) out.memoryCapGb = "Must be above 0 GB";
  else if (mem > 0 && cap > mem) out.memoryCapGb = `Cannot exceed the card's memory (${mem} GB)`;
  return out;
}

const sameWindows = (a: AvailabilityWindows | undefined, b: AvailabilityWindows | undefined) => JSON.stringify(windowRows(a)) === JSON.stringify(windowRows(b));

/** The card edits that differ from the host they were started from (only those are sent). */
export function cardEdits(base: ComputeHost, draft: Record<number, CardDraft>): ComputeCardEdit[] {
  const out: ComputeCardEdit[] = [];
  for (const c of base.cards) {
    const d = draft[c.index];
    if (!d) continue;
    const cap = Number(d.memoryCapGb);
    const e: ComputeCardEdit = { index: c.index };
    if (d.name !== undefined && d.name.trim() !== c.name) e.name = d.name.trim();
    if (d.cardClass !== undefined && d.cardClass !== c.cardClass) e.cardClass = d.cardClass;
    if (d.memoryGb !== undefined && Number(d.memoryGb) !== c.memoryGb) e.memoryGb = Number(d.memoryGb);
    if (cap !== c.memoryCapGb) e.memoryCapGb = cap;
    const kinds = JOB_KINDS.filter((k) => d.allowedJobKinds.includes(k));
    if (kinds.join() !== JOB_KINDS.filter((k) => c.allowedJobKinds.includes(k)).join()) e.allowedJobKinds = kinds;
    if (d.windows !== undefined && !sameWindows(d.windows, c.windows)) e.windows = d.windows;
    if (Object.keys(e).length > 1) out.push(e);
  }
  return out;
}

/** "Why this default?" for a card's cap, from the host seeded by defaults.yaml. */
export function capDefault(host: DefaultHost | undefined, card: ComputeCard): DefaultValue | undefined {
  const d = host?.cards.find((c) => c.index === card.index);
  if (!host || !d) return undefined;
  return {
    value: d.memory_cap_gb,
    unit: "GB",
    description: "The memory a Cadence job may use on this card (CADENCE_GPU_MEMORY_CAP_GB); other services stay resident on the rest.",
    source: host.source,
    range: { min: 1, max: card.memoryGb },
  };
}

const draftOf = (c: ComputeCard): CardDraft => ({
  memoryCapGb: String(c.memoryCapGb),
  allowedJobKinds: [...c.allowedJobKinds],
  windows: c.windows ?? {},
  name: c.name,
  cardClass: c.cardClass,
  memoryGb: String(c.memoryGb),
});

export function ComputeSection() {
  const qc = useQueryClient();
  const { data, isLoading } = useQuery(computeListOptions());
  const defaults = useDefaults();
  useTopic(["entity.compute.*"], (batch) => {
    for (const e of batch) {
      const host = (e.payload as { compute?: ComputeHost } | undefined)?.compute;
      if (!host) continue;
      qc.setQueryData(computeGetQueryKey({ path: { id: host.id } }), host);
      qc.setQueryData<ComputeList>(computeListQueryKey(), (old) => (old ? { ...old, items: old.items.map((h) => (h.id === host.id ? host : h)) } : old));
    }
  });
  return (
    <section aria-labelledby="settings-compute" className="flex flex-col gap-3">
      <SectionHeading id="settings-compute" title="Compute" hint="Hosts and their cards. The queue runs one training job per card, capped at the card's memory cap." />
      {isLoading ? <p className="text-xs text-muted-foreground">Loading…</p> : null}
      {(data?.items ?? []).map((h) => (
        <HostCard key={h.id} host={h} seeded={defaults.data?.compute.hosts.find((d) => d.name === h.name)} />
      ))}
    </section>
  );
}

function HostCard({ host, seeded }: { host: ComputeHost; seeded?: DefaultHost }) {
  // The card being edited stays set while the dialog closes (its exit animation, focus back to its Edit button); each
  // opening is a new session, so a reopened dialog starts from the host as it is then.
  const [editing, setEditing] = useState<{ index: number; open: boolean; session: number } | null>(null);
  const editButtons = useRef(new Map<number, HTMLButtonElement>());
  const card = editing === null ? undefined : host.cards.find((c) => c.index === editing.index);
  const close = () => setEditing((e) => (e ? { ...e, open: false } : e));
  return (
    <div className="rounded-md border" data-testid={`compute-host-${host.name}`}>
      <div className="flex flex-wrap items-center gap-2 border-b px-3 py-2">
        <span className="text-[13px] font-medium">{host.name}</span>
        <Chip tone={host.health.state === "healthy" ? "accent" : host.health.state === "unknown" ? "neutral" : "warning"} title={host.health.detail ?? "Health arrives when a worker reports"}>
          {host.health.state}
        </Chip>
        <span className="text-xs text-muted-foreground tabular-nums">rev {host.rev}</span>
        <span className="min-w-0 flex-1 truncate text-xs text-muted-foreground" title={host.description}>
          {host.description}
        </span>
      </div>
      <Table label={`Cards of ${host.name}`} head={["#", "Card and class", "Memory", "Memory cap", "Job kinds", "Availability", ""]} className="rounded-none border-0">
        {host.cards.map((c) => {
          const def = capDefault(seeded, c);
          const departs = def && Number(def.value) !== c.memoryCapGb;
          const windows = formatWindows(c.windows);
          return (
            <tr key={c.index}>
              <Td className="text-muted-foreground tabular-nums">{c.index}</Td>
              <Td className="min-w-36">
                <div className="font-medium">{c.name}</div>
                <div className="font-mono text-[11px] text-muted-foreground">{c.cardClass}</div>
              </Td>
              <Td className="whitespace-nowrap tabular-nums">{c.memoryGb} GB</Td>
              <Td className="whitespace-nowrap">
                <div className="flex flex-col items-start gap-0.5">
                  <span className="inline-flex items-center gap-0.5">
                    <span className="tabular-nums">{c.memoryCapGb} GB</span>
                    <WhyDefault label={`memory cap of card ${c.index}`} value={def} />
                  </span>
                  {departs ? (
                    <Chip tone="accent" title={`Default ${String(def?.value)} GB`}>
                      departs from default
                    </Chip>
                  ) : null}
                </div>
              </Td>
              <Td className="min-w-28">{c.allowedJobKinds.length ? <span className="text-muted-foreground">{JOB_KINDS.filter((k) => c.allowedJobKinds.includes(k)).join(", ")}</span> : <span className="text-status-warning-foreground">none</span>}</Td>
              <Td>
                <span className="text-muted-foreground" data-testid={`windows-${c.index}`}>
                  {windows.length ? windows.join(" · ") : "any time"}
                </span>
              </Td>
              <Td className="w-0 text-right">
                <Button
                  ref={(el) => {
                    if (el) editButtons.current.set(c.index, el);
                    else editButtons.current.delete(c.index);
                  }}
                  size="xs"
                  variant="outline"
                  aria-label={`Edit card ${c.index}`}
                  onClick={() => setEditing((e) => ({ index: c.index, open: true, session: (e?.session ?? 0) + 1 }))}
                  data-command="compute.edit"
                >
                  <EditPencil aria-hidden />
                  Edit
                </Button>
              </Td>
            </tr>
          );
        })}
      </Table>
      <p className="border-t px-3 py-1.5 text-[11px] text-muted-foreground">Updated {when(host.updatedAt)}</p>
      {card && editing ? (
        <Dialog open={editing.open} onOpenChange={(o) => !o && close()}>
          <DialogContent className="flex max-h-[calc(100dvh-2rem)] flex-col gap-0 p-0 sm:max-w-2xl" finalFocus={() => editButtons.current.get(card.index) ?? true} data-testid={`compute-card-dialog-${card.index}`}>
            <CardForm key={editing.session} host={host} card={card} seeded={seeded} onDone={close} />
          </DialogContent>
        </Dialog>
      ) : null}
    </div>
  );
}

/** The card dialog's body: the form, the conflict banner and the footer. */
function CardForm({ host, card, seeded, onDone }: { host: ComputeHost; card: ComputeCard; seeded?: DefaultHost; onDone: () => void }) {
  const qc = useQueryClient();
  const [base, setBase] = useState<ComputeHost>(host); // the revision the edit started from
  const [draft, setDraft] = useState<CardDraft>(() => draftOf(card));
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [conflict, setConflict] = useState<number | null>(null);
  const baseCard = base.cards.find((c) => c.index === card.index) ?? card;
  const patch = (p: Partial<CardDraft>) => setDraft((d) => ({ ...d, ...p }));

  const fieldErrors = cardFieldErrors(draft, baseCard);
  const windowsInvalid = windowRows(draft.windows).some((r) => windowError(r.window));
  const invalid = !!cardDraftError(draft, baseCard) || windowsInvalid;
  const dirty = cardEdits(base, { [card.index]: draft }).length > 0;

  const reload = () => {
    setBase(host);
    setDraft(draftOf(host.cards.find((c) => c.index === card.index) ?? card));
    setError(null);
    setConflict(null);
  };
  const save = async (from: ComputeHost) => {
    const cards = cardEdits(from, { [card.index]: draft });
    if (cards.length === 0) return onDone();
    setBusy(true);
    setError(null);
    try {
      const next = await runCommand("compute.edit", { host: from, body: { cards } });
      qc.setQueryData<ComputeList>(computeListQueryKey(), (old) => (old ? { ...old, items: old.items.map((h) => (h.id === next.id ? next : h)) } : old));
      onDone();
    } catch (err) {
      const p = problemOf(err);
      if (p?.status === 412) {
        setConflict(p.currentRev ?? host.rev);
        void qc.invalidateQueries({ queryKey: computeListQueryKey() });
      } else setError(errorMessage(err));
    } finally {
      setBusy(false);
    }
  };
  const submit = (e: FormEvent) => {
    e.preventDefault();
    if (busy || invalid || !dirty || conflict !== null) return;
    void save(base);
  };

  const id = `card-${host.id}-${card.index}`;
  const def = capDefault(seeded, baseCard);
  const mem = Number(draft.memoryGb ?? baseCard.memoryGb);
  const cap = Number(draft.memoryCapGb);
  const capWarning = fieldErrors.memoryCapGb ? undefined : rangeWarning(cap, def ? { ...def.range, max: mem > 0 ? mem : def.range?.max } : undefined);
  const capHint = `${def ? `Default ${String(def.value)} GB (see Why this default?). ` : ""}What a Cadence job may use; the rest of the card stays free for resident services.`;
  const noKinds = draft.allowedJobKinds.length === 0;

  return (
    <form onSubmit={submit} aria-label={`Edit card ${card.index} of ${host.name}`} className="flex min-h-0 flex-1 flex-col">
      <DialogHeader className="gap-1 border-b px-4 pt-4 pb-3 pr-12">
        <DialogTitle className="text-sm">
          Edit card {card.index} · {baseCard.name}
        </DialogTitle>
        <DialogDescription className="text-xs">
          {host.name} · rev {base.rev}. Only what you change is sent; a change made meanwhile is shown, never overwritten.
        </DialogDescription>
      </DialogHeader>

      <div className="flex min-h-0 flex-1 flex-col gap-5 overflow-y-auto px-4 py-4">
        <FieldGroup title="Identity">
          <div className="grid gap-3 sm:grid-cols-2">
            <Field label="Name" htmlFor={`${id}-name`} error={fieldErrors.name}>
              <Input
                id={`${id}-name`}
                value={draft.name ?? ""}
                onChange={(e) => patch({ name: e.target.value })}
                aria-invalid={!!fieldErrors.name || undefined}
                aria-describedby={describedBy(`${id}-name`, { error: fieldErrors.name })}
                autoComplete="off"
                required
              />
            </Field>
            <Field label="Card class" htmlFor={`${id}-class`} hint="Keys the estimate table in defaults.yaml, e.g. blackwell-48gb." error={fieldErrors.cardClass}>
              <Input
                id={`${id}-class`}
                value={draft.cardClass ?? ""}
                onChange={(e) => patch({ cardClass: e.target.value })}
                className="font-mono"
                spellCheck={false}
                autoComplete="off"
                aria-invalid={!!fieldErrors.cardClass || undefined}
                aria-describedby={describedBy(`${id}-class`, { hint: true, error: fieldErrors.cardClass })}
              />
            </Field>
          </div>
        </FieldGroup>

        <FieldGroup title="Memory">
          <div className="grid gap-3 sm:grid-cols-2">
            <Field label="Card memory (GB)" htmlFor={`${id}-memory`} hint="What the card has; correct it when the host was seeded with the wrong card." error={fieldErrors.memoryGb}>
              <Input
                id={`${id}-memory`}
                type="number"
                min={1}
                step={1}
                value={draft.memoryGb ?? ""}
                onChange={(e) => patch({ memoryGb: e.target.value })}
                className="w-28 tabular-nums"
                aria-invalid={!!fieldErrors.memoryGb || undefined}
                aria-describedby={describedBy(`${id}-memory`, { hint: true, error: fieldErrors.memoryGb })}
              />
            </Field>
            <Field
              label={`Memory cap of card ${card.index} (GB)`}
              htmlFor={`${id}-cap`}
              hint={capHint}
              warning={capWarning}
              error={fieldErrors.memoryCapGb}
              extra={<WhyDefault label={`memory cap of card ${card.index}`} value={def} />}
            >
              <Input
                id={`${id}-cap`}
                type="number"
                min={1}
                max={mem > 0 ? mem : undefined}
                step={1}
                value={draft.memoryCapGb}
                onChange={(e) => patch({ memoryCapGb: e.target.value })}
                className="w-28 tabular-nums"
                aria-invalid={!!fieldErrors.memoryCapGb || undefined}
                aria-describedby={describedBy(`${id}-cap`, { hint: true, warning: capWarning, error: fieldErrors.memoryCapGb })}
              />
            </Field>
          </div>
          <MemoryBar memoryGb={mem} capGb={cap} card={baseCard} />
        </FieldGroup>

        <FieldGroup title="Job kinds" hint="The kinds of queued job this card accepts.">
          <div className="grid gap-1.5 sm:grid-cols-2" role="group" aria-label={`Allowed job kinds of card ${card.index}`} aria-describedby={noKinds ? `${id}-kinds-warning` : undefined}>
            {JOB_KINDS.map((k) => {
              const checked = draft.allowedJobKinds.includes(k);
              return (
                <label key={k} className="flex cursor-pointer items-start gap-2 rounded-md border px-2.5 py-2 text-xs hover:bg-hover">
                  <input
                    type="checkbox"
                    className="mt-0.5 size-3.5 shrink-0 accent-primary"
                    checked={checked}
                    aria-describedby={`${id}-kind-${k}`}
                    onChange={(e) => patch({ allowedJobKinds: e.target.checked ? [...draft.allowedJobKinds, k] : draft.allowedJobKinds.filter((x) => x !== k) })}
                  />
                  <span className="flex min-w-0 flex-col gap-0.5">
                    <span className="font-medium">{k}</span>
                    <span id={`${id}-kind-${k}`} className="text-[11px] text-muted-foreground">
                      {JOB_KIND_HINT[k]}
                    </span>
                  </span>
                </label>
              );
            })}
          </div>
          {noKinds ? (
            <p id={`${id}-kinds-warning`} className="text-xs text-status-warning-foreground">
              No job kind is allowed: nothing will be scheduled on this card.
            </p>
          ) : null}
        </FieldGroup>

        <FieldGroup title="Availability windows" hint="Per job kind, the days and hours a job may run. A kind without windows runs any time; a training job running at the close pauses and resumes when a window opens.">
          <WindowsEditor card={card.index} kinds={JOB_KINDS} value={draft.windows} onChange={(w) => patch({ windows: w })} />
        </FieldGroup>
      </div>

      {conflict !== null ? (
        <div role="alert" className="flex flex-wrap items-center gap-2 border-t bg-hover px-4 py-2.5 text-xs" data-testid="compute-conflict">
          <span className="min-w-0 flex-1">
            {host.name} changed while you were editing (you started from rev {base.rev}; it is now rev {conflict}). Nothing was saved.
          </span>
          <Button type="button" size="xs" variant="outline" onClick={reload}>
            Discard mine and reload
          </Button>
          <Button type="button" size="xs" onClick={() => void save(host)} disabled={busy || host.rev < conflict}>
            Apply mine on rev {conflict}
          </Button>
        </div>
      ) : null}
      {error ? (
        <p role="alert" className="border-t px-4 py-2.5 text-xs text-destructive">
          {error}
        </p>
      ) : null}

      <DialogFooter className="mx-0 mb-0 items-center px-4 py-3">
        {invalid ? <span className="mr-auto text-xs text-muted-foreground">Fix the marked fields to save.</span> : !dirty ? <span className="mr-auto text-xs text-muted-foreground">No changes yet.</span> : null}
        <Button type="button" size="sm" variant="outline" onClick={onDone} disabled={busy}>
          Cancel
        </Button>
        <Button type="submit" size="sm" disabled={busy || invalid || !dirty || conflict !== null} data-command="compute.edit">
          {busy ? "Saving…" : "Save"}
        </Button>
      </DialogFooter>
    </form>
  );
}

const gb = (mb: number) => Math.round(mb / 102.4) / 10;

/**
 * Card memory, the cap from the right (where a Cadence job lives) and, from the left, what the worker's last report
 * says is in use — resident services such as vLLM, plus any Cadence job running then (one number for every process).
 */
function MemoryBar({ memoryGb, capGb, card }: { memoryGb: number; capGb: number; card: ComputeCard }) {
  if (!(memoryGb > 0)) return null;
  const usedGb = card.telemetry?.memoryUsedMb !== undefined ? gb(card.telemetry.memoryUsedMb) : undefined;
  const capOk = capGb > 0 ? Math.min(capGb, memoryGb) : 0;
  const pct = (v: number) => `${Math.min(100, Math.max(0, (v / memoryGb) * 100))}%`;
  const overlap = usedGb !== undefined && usedGb + capOk > memoryGb;
  const summary = `Card ${memoryGb} GB; Cadence cap ${capOk} GB; ${memoryGb - capOk} GB left for resident services${usedGb !== undefined ? `; ${usedGb} GB in use at the last report` : "; no memory report yet"}`;
  return (
    <div className="flex flex-col gap-1.5 rounded-md border bg-tool p-2.5 text-xs" data-testid="compute-memory-bar">
      <div className="relative h-3 overflow-hidden rounded-full bg-muted" role="img" aria-label={summary}>
        {usedGb !== undefined ? <span className="absolute inset-y-0 left-0 bg-muted-foreground/60" style={{ width: pct(usedGb) }} /> : null}
        <span className={cn("absolute inset-y-0 right-0 bg-accent-line", overlap && "opacity-80")} style={{ width: pct(capOk) }} />
        <span aria-hidden className="absolute inset-y-0 w-px bg-foreground" style={{ left: pct(memoryGb - capOk) }} />
      </div>
      <div className="flex flex-wrap gap-x-3 gap-y-0.5 text-muted-foreground tabular-nums" aria-hidden>
        <span>
          <span className="mr-1 inline-block size-2 rounded-full bg-accent-line" />
          Cadence cap {capOk} GB
        </span>
        <span>
          <span className="mr-1 inline-block size-2 rounded-full bg-muted-foreground/60" />
          {usedGb !== undefined ? `In use at last report ${usedGb} GB` : "No memory report yet"}
        </span>
        <span className="ml-auto">of {memoryGb} GB</span>
      </div>
      {usedGb !== undefined && card.telemetry ? <p className="text-[11px] text-muted-foreground">Reported {when(card.telemetry.reportedAt)}; the reading counts every process on the card, a running Cadence job included.</p> : null}
      {overlap ? <p className="text-status-warning-foreground">The cap plus what was in use exceeds the card's memory: a job using its whole cap may not fit beside those services.</p> : null}
    </div>
  );
}
