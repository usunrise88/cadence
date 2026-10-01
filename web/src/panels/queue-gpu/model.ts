import type { CardTelemetry, ComputeHost, QueueEntry } from "@/api/gen/types.gen";
import { sortEntries } from "@/shell/panel";

// Pure logic of Queue & GPU: queue order and grouping by card, the training slot, reorder targets, the telemetry
// ring buffer and the memory split between resident services and Cadence.

export { sortEntries };

export const cardKey = (host: string, index: number) => `${host}#${index}`;

export type Groups = {
  /** Leased entries per card (host name # card index). */
  byCard: Map<string, QueueEntry[]>;
  /** Entries waiting for a card (no lease yet), in queue order. */
  waiting: QueueEntry[];
  /** Leased steps that need no card (card −1). */
  noCard: QueueEntry[];
};

export function groupEntries(items: QueueEntry[]): Groups {
  const byCard = new Map<string, QueueEntry[]>();
  const waiting: QueueEntry[] = [];
  const noCard: QueueEntry[] = [];
  for (const e of sortEntries(items)) {
    const l = e.lease;
    if (!l) waiting.push(e);
    else if (l.card < 0) noCard.push(e);
    else {
      const k = cardKey(l.host, l.card);
      byCard.set(k, [...(byCard.get(k) ?? []), e]);
    }
  }
  return { byCard, waiting, noCard };
}

/** The entry holding a card's training slot (one per card), if any. */
export function trainingSlot(entries: QueueEntry[] | undefined): QueueEntry | undefined {
  return entries?.find((e) => e.jobKind === "training" && (e.state === "running" || e.state === "stopping"));
}

/** One `jobs.edit` of a reorder: the entry and its new priority. */
export type PriorityEdit = { jobId: string; priority: number };

const PRIORITY_MIN = -1000;
const PRIORITY_MAX = 1000;

/**
 * The priority edits that move a waiting entry exactly one place up (dir −1) or down (dir 1) in `queue` (sorted with
 * `sortEntries`): it swaps places with its neighbour and nothing else moves. The rule, smallest change first:
 *
 * 1. One edit, preferring the moved entry: give it the neighbour's priority, or one past it, whichever is the smaller
 *    change that sorts it right past the neighbour (FIFO and the job id break ties); failing that, give the neighbour
 *    the moved entry's priority or one past it.
 * 2. When neighbours share a priority no single value can do it (one above the neighbour would also pass every entry
 *    tied with it). Then the moved entry goes one past the neighbour together with the fewest entries on the far side
 *    of the neighbour (those it would otherwise pass), each shifted by one in the same direction so their own order
 *    holds — or, if that takes fewer edits, the neighbour moves one the other way together with the entries it would
 *    otherwise pass.
 *
 * Every candidate is checked by sorting the edited queue; values stay within the contract's range [−1000, 1000].
 * Undefined when the entry is already first or last, or no edit within the range moves it exactly one place.
 */
export function reorderPriority(queue: QueueEntry[], jobId: string, dir: -1 | 1): PriorityEdit[] | undefined {
  const i = queue.findIndex((e) => e.jobId === jobId);
  const j = i + dir;
  if (i < 0 || j < 0 || j >= queue.length) return undefined;
  const target = queue.map((e) => e.jobId);
  target[i] = queue[j]!.jobId;
  target[j] = jobId;
  const valid = (edits: PriorityEdit[]) => {
    if (edits.some((e) => e.priority < PRIORITY_MIN || e.priority > PRIORITY_MAX)) return false;
    const next = new Map(edits.map((e) => [e.jobId, e.priority]));
    const order = sortEntries(queue.map((e) => (next.has(e.jobId) ? { ...e, priority: next.get(e.jobId)! } : e)));
    return order.every((e, k) => e.jobId === target[k]);
  };
  // The pair ends up swapped: `bottom` rises above `top`, or `top` sinks below `bottom`.
  const hi = Math.min(i, j);
  const lo = Math.max(i, j);
  const top = queue[hi]!;
  const bottom = queue[lo]!;
  // `rise(k)`: bottom one above top, the k entries ahead of top one up each. `sink(k)`: top one below bottom, the k
  // entries behind bottom one down each.
  const rise = (k: number): PriorityEdit[] | undefined =>
    hi - k < 0 ? undefined : [{ jobId: bottom.jobId, priority: top.priority + 1 }, ...queue.slice(hi - k, hi).map((e) => ({ jobId: e.jobId, priority: e.priority + 1 }))];
  const sink = (k: number): PriorityEdit[] | undefined =>
    lo + k >= queue.length ? undefined : [{ jobId: top.jobId, priority: bottom.priority - 1 }, ...queue.slice(lo + 1, lo + 1 + k).map((e) => ({ jobId: e.jobId, priority: e.priority - 1 }))];
  const sameAs = (who: QueueEntry, priority: number): PriorityEdit[] | undefined => (who.priority === priority ? undefined : [{ jobId: who.jobId, priority }]);
  // The moved entry's own change first, then its neighbour's.
  const [first, second] = dir < 0 ? [rise, sink] : [sink, rise];
  const single = dir < 0 ? [sameAs(bottom, top.priority), rise(0), sameAs(top, bottom.priority), sink(0)] : [sameAs(top, bottom.priority), sink(0), sameAs(bottom, top.priority), rise(0)];
  for (const edits of single) if (edits && valid(edits)) return edits;
  const smallest = (shift: (k: number) => PriorityEdit[] | undefined) => {
    for (let k = 1; ; k++) {
      const edits = shift(k);
      if (!edits) return undefined;
      if (valid(edits)) return edits;
    }
  };
  const a = smallest(first);
  const b = smallest(second);
  if (a && b) return b.length < a.length ? b : a;
  return a ?? b;
}

// ---------------------------------------------------------------- telemetry

export type Sample = { t: number; usedMb: number; totalMb: number; util: number | null; cadence: boolean };
export type Telemetry = Record<string, Sample[]>;

/** How long the chart looks back. */
export const WINDOW_SECONDS = 30 * 60;

/** Appends samples per card, drops those older than the window and keeps time order. */
export function appendTelemetry(cur: Telemetry, host: string, cards: CardTelemetry[], t: number, cadenceOn: (key: string) => boolean): Telemetry {
  const next: Telemetry = { ...cur };
  for (const c of cards) {
    if (c.memoryUsedMb === undefined && c.utilization === undefined) continue;
    const k = cardKey(host, c.index);
    const list = (next[k] ?? []).filter((s) => s.t !== t);
    list.push({ t, usedMb: c.memoryUsedMb ?? 0, totalMb: c.memoryTotalMb ?? 0, util: c.utilization ?? null, cadence: cadenceOn(k) });
    list.sort((a, b) => a.t - b.t);
    const newest = list[list.length - 1]!.t;
    next[k] = list.filter((s) => s.t > newest - WINDOW_SECONDS);
  }
  return next;
}

/** Seeds the buffer from the last report compute.list carries per card. */
export function seedTelemetry(hosts: ComputeHost[], cadenceOn: (key: string) => boolean): Telemetry {
  let out: Telemetry = {};
  for (const h of hosts)
    for (const c of h.cards) {
      if (!c.telemetry) continue;
      out = appendTelemetry(out, h.name, [c.telemetry], Date.parse(c.telemetry.reportedAt) / 1000, cadenceOn);
    }
  return out;
}

/**
 * Splits a card's used memory between resident services (vLLM and anything else outside Cadence) and Cadence. The
 * worker reports one number for every process, so the resident share is the last reading taken while no Cadence
 * job held the card; without one, a running job is assumed to use its whole cap.
 */
export function splitMemory(samples: Sample[], capMb: number): { residentMb: number; cadenceMb: number; estimated: boolean } | undefined {
  const last = samples[samples.length - 1];
  if (!last) return undefined;
  if (!last.cadence) return { residentMb: last.usedMb, cadenceMb: 0, estimated: false };
  const idle = [...samples].reverse().find((s) => !s.cadence);
  const resident = idle ? Math.min(idle.usedMb, last.usedMb) : Math.max(0, last.usedMb - capMb);
  return { residentMb: resident, cadenceMb: Math.max(0, last.usedMb - resident), estimated: true };
}

/** Seconds as "1 h 20 min", "45 s". */
export function formatDuration(seconds: number | undefined): string {
  if (seconds === undefined || !Number.isFinite(seconds)) return "unknown";
  const s = Math.round(seconds);
  if (s < 60) return `${s} s`;
  const m = Math.round(s / 60);
  if (m < 60) return `${m} min`;
  const h = Math.floor(m / 60);
  return m % 60 ? `${h} h ${m % 60} min` : `${h} h`;
}
