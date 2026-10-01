import { Fragment, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { computeGetQueryKey, computeListOptions, computeListQueryKey } from "@/api/gen/@tanstack/react-query.gen";
import type { AvailabilityWindows, ComputeCard, ComputeCardEdit, ComputeHost, ComputeList, DefaultHost, DefaultValue, JobKind } from "@/api/gen/types.gen";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { errorMessage, formatWindows, problemOf, rangeWarning, runCommand, useDefaults, useTopic, WhyDefault, windowError } from "@/shell/panel";
import { Chip, SectionHeading, Table, Td, when } from "./ui";
import { windowRows, WindowsEditor } from "./WindowsEditor";

// Compute (docs/spec/11-ui-panels.md "Settings"): hosts, cards, memory caps and allowed job kinds. Edits carry
// If-Match with the revision the edit started from; a change made meanwhile (another tab, an agent) answers 412 and
// the form shows both instead of overwriting (docs/spec/11 "Risks": revision conflicts, not silent overwrites).
// Availability windows per card and job kind (R19) are edited here too; Queue & GPU shows them.

export const JOB_KINDS: JobKind[] = ["training", "eval", "shadow", "export", "data"];

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
      <SectionHeading id="settings-compute" title="Compute" hint="Hosts and their cards. The queue (phase 2) runs one training job per card, capped at the card's memory cap." />
      {isLoading ? <p className="text-xs text-muted-foreground">Loading…</p> : null}
      {(data?.items ?? []).map((h) => (
        <HostCard key={h.id} host={h} seeded={defaults.data?.compute.hosts.find((d) => d.name === h.name)} />
      ))}
    </section>
  );
}

function HostCard({ host, seeded }: { host: ComputeHost; seeded?: DefaultHost }) {
  const qc = useQueryClient();
  const [base, setBase] = useState<ComputeHost | null>(null); // the revision the edit started from
  const [draft, setDraft] = useState<Record<number, CardDraft>>({});
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [conflict, setConflict] = useState<number | null>(null);
  const editing = base !== null;

  const start = () => {
    setBase(host);
    setDraft(Object.fromEntries(host.cards.map((c) => [c.index, { memoryCapGb: String(c.memoryCapGb), allowedJobKinds: [...c.allowedJobKinds], windows: c.windows ?? {}, name: c.name, cardClass: c.cardClass, memoryGb: String(c.memoryGb) }])));
    setError(null);
    setConflict(null);
  };
  const cancel = () => {
    setBase(null);
    setError(null);
    setConflict(null);
  };
  const save = async (from: ComputeHost) => {
    const cards = cardEdits(from, draft);
    if (cards.length === 0) return cancel();
    setBusy(true);
    setError(null);
    try {
      const next = await runCommand("compute.edit", { host: from, body: { cards } });
      qc.setQueryData<ComputeList>(computeListQueryKey(), (old) => (old ? { ...old, items: old.items.map((h) => (h.id === next.id ? next : h)) } : old));
      setBase(null);
      setConflict(null);
    } catch (err) {
      const p = problemOf(err);
      if (p?.status === 412) {
        setConflict(p.currentRev ?? host.rev);
        void qc.invalidateQueries({ queryKey: computeListQueryKey() });
      }
      else setError(errorMessage(err));
    } finally {
      setBusy(false);
    }
  };

  const invalid = editing && host.cards.some((c) => { const d = draft[c.index]; return !!d && (!!cardDraftError(d, c) || windowRows(d.windows).some((r) => windowError(r.window))); });
  return (
    <div className="rounded-md border" data-testid={`compute-host-${host.name}`}>
      <div className="flex flex-wrap items-center gap-2 border-b px-3 py-2">
        <span className="text-[13px] font-medium">{host.name}</span>
        <Chip tone={host.health.state === "healthy" ? "accent" : host.health.state === "unknown" ? "neutral" : "warning"} title={host.health.detail ?? "Health arrives when a worker reports (phase 2)"}>
          {host.health.state}
        </Chip>
        <span className="text-xs text-muted-foreground">rev {host.rev}</span>
        <span className="min-w-0 flex-1 truncate text-xs text-muted-foreground" title={host.description}>
          {host.description}
        </span>
        {editing ? (
          <>
            <Button size="xs" variant="ghost" onClick={cancel} disabled={busy}>
              Cancel
            </Button>
            <Button size="xs" onClick={() => void save(base)} disabled={busy || invalid || conflict !== null} data-command="compute.edit">
              Save
            </Button>
          </>
        ) : (
          <Button size="xs" variant="outline" onClick={start} data-command="compute.edit">
            Edit
          </Button>
        )}
      </div>
      <Table label={`Cards of ${host.name}`} head={["#", "Card", "Class", "Memory", "Memory cap", "Allowed job kinds", "Availability"]} className="rounded-none border-0">
        {host.cards.map((c) => {
          const d = draft[c.index];
          const def = capDefault(seeded, c);
          const capValue = editing && d ? Number(d.memoryCapGb) : c.memoryCapGb;
          const warn = rangeWarning(capValue, def?.range);
          const departs = def && Number(def.value) !== c.memoryCapGb;
          const capId = `cap-${host.id}-${c.index}`;
          const windows = formatWindows(c.windows);
          return (
            <Fragment key={c.index}>
              <tr>
                <Td className="tabular-nums">{c.index}</Td>
                {editing && d ? (
                  <>
                    <Td>
                      <Input
                        aria-label={`Name of card ${c.index}`}
                        value={d.name ?? c.name}
                        onChange={(e) => setDraft((s) => ({ ...s, [c.index]: { ...d, name: e.target.value } }))}
                        className="h-6 w-56 text-xs"
                      />
                    </Td>
                    <Td>
                      <Input
                        aria-label={`Class of card ${c.index}`}
                        value={d.cardClass ?? c.cardClass}
                        onChange={(e) => setDraft((s) => ({ ...s, [c.index]: { ...d, cardClass: e.target.value } }))}
                        className="h-6 w-36 font-mono text-xs"
                        aria-invalid={(d.cardClass !== undefined && !CARD_CLASS.test(d.cardClass)) || undefined}
                      />
                    </Td>
                    <Td>
                      <div className="flex items-center gap-1">
                        <Input
                          aria-label={`Memory of card ${c.index} (GB)`}
                          type="number"
                          min={1}
                          step={1}
                          value={d.memoryGb ?? String(c.memoryGb)}
                          onChange={(e) => setDraft((s) => ({ ...s, [c.index]: { ...d, memoryGb: e.target.value } }))}
                          className="h-6 w-20 text-xs"
                        />
                        <span className="text-muted-foreground">GB</span>
                      </div>
                    </Td>
                  </>
                ) : (
                  <>
                    <Td>{c.name}</Td>
                    <Td className="font-mono">{c.cardClass}</Td>
                    <Td className="tabular-nums">{c.memoryGb} GB</Td>
                  </>
                )}
                <Td>
                  <div className="flex items-center gap-1">
                    {editing && d ? (
                      <>
                        <label htmlFor={capId} className="sr-only">
                          Memory cap of card {c.index} (GB)
                        </label>
                        <Input
                          id={capId}
                          type="number"
                          min={1}
                          max={Number(d.memoryGb ?? c.memoryGb)}
                          step={1}
                          value={d.memoryCapGb}
                          onChange={(e) => setDraft((s) => ({ ...s, [c.index]: { ...d, memoryCapGb: e.target.value } }))}
                          className="h-6 w-20 text-xs"
                          aria-invalid={!(Number(d.memoryCapGb) > 0) || undefined}
                        />
                        <span className="text-muted-foreground">GB</span>
                      </>
                    ) : (
                      <span className="tabular-nums">{c.memoryCapGb} GB</span>
                    )}
                    <WhyDefault label={`memory cap of card ${c.index}`} value={def} />
                    {!editing && departs ? <Chip tone="accent" title={`Default ${String(def?.value)} GB`}>departs from default</Chip> : null}
                  </div>
                  {warn ? <span className="text-status-warning-foreground">{warn}</span> : null}
                  {editing && d && cardDraftError(d, c) ? <span className="text-status-failed-foreground">{cardDraftError(d, c)}</span> : null}
                </Td>
                <Td>
                  {editing && d ? (
                    <fieldset className="flex flex-wrap gap-2">
                      <legend className="sr-only">Allowed job kinds of card {c.index}</legend>
                      {JOB_KINDS.map((k) => (
                        <label key={k} className="inline-flex min-h-6 items-center gap-1">
                          <input
                            type="checkbox"
                            className="size-3.5 accent-primary"
                            checked={d.allowedJobKinds.includes(k)}
                            onChange={(e) =>
                              setDraft((s) => ({
                                ...s,
                                [c.index]: { ...d, allowedJobKinds: e.target.checked ? [...d.allowedJobKinds, k] : d.allowedJobKinds.filter((x) => x !== k) },
                              }))
                            }
                          />
                          {k}
                        </label>
                      ))}
                    </fieldset>
                  ) : (
                    <span className="text-muted-foreground">{c.allowedJobKinds.join(", ") || "none"}</span>
                  )}
                </Td>
                <Td>
                  <span className="text-muted-foreground" data-testid={`windows-${c.index}`}>
                    {windows.length ? windows.join(" · ") : "any time"}
                  </span>
                </Td>
              </tr>
              {editing && d ? (
                <tr>
                  <td colSpan={7} className="px-2 py-1.5">
                    <WindowsEditor card={c.index} kinds={[...JOB_KINDS, "data"]} value={d.windows} onChange={(w) => setDraft((s) => ({ ...s, [c.index]: { ...d, windows: w } }))} />
                  </td>
                </tr>
              ) : null}
            </Fragment>
          );
        })}
      </Table>
      {conflict !== null && base ? (
        <div role="alert" className="flex flex-wrap items-center gap-2 border-t bg-hover px-3 py-2 text-xs" data-testid="compute-conflict">
          <span>
            {host.name} changed while you were editing (you started from rev {base.rev}; it is now rev {conflict}). Nothing was saved.
          </span>
          <Button size="xs" variant="outline" className="ml-auto" onClick={start}>
            Discard mine and reload
          </Button>
          <Button size="xs" onClick={() => void save(host)} disabled={busy || host.rev < conflict}>
            Apply mine on rev {conflict}
          </Button>
        </div>
      ) : null}
      {error ? (
        <p role="alert" className="border-t px-3 py-2 text-xs text-destructive">
          {error}
        </p>
      ) : null}
      <p className="border-t px-3 py-1.5 text-[11px] text-muted-foreground">Updated {when(host.updatedAt)}</p>
    </div>
  );
}
