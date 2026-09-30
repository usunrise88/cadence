import type { CardTelemetry, ComputeHost, QueueEntry } from "@/api/gen/types.gen";

// Pure logic of Queue & GPU: queue order and grouping by card, the training slot, reorder targets, the telemetry
// ring buffer and the memory split between resident services and Cadence.

const STATE_ORDER: Record<QueueEntry["state"], number> = { running: 0, stopping: 1, waiting: 2, paused: 3 };

/** Running and stopping first, then the queue order the scheduler uses: priority (higher first), then FIFO. */
export function sortEntries(items: QueueEntry[]): QueueEntry[] {
  return [...items].sort(
    (a, b) => STATE_ORDER[a.state] - STATE_ORDER[b.state] || b.priority - a.priority || a.enqueuedAt.localeCompare(b.enqueuedAt) || a.jobId.localeCompare(b.jobId),
  );
}

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

/**
 * The priority that moves a waiting entry one place up or down in the queue: one above the entry it passes (or one
 * below), within the contract's range. Undefined when it is already first or last.
 */
export function reorderPriority(queue: QueueEntry[], jobId: string, dir: -1 | 1): number | undefined {
  const i = queue.findIndex((e) => e.jobId === jobId);
  const j = i + (dir < 0 ? -1 : 1);
  if (i < 0 || j < 0 || j >= queue.length) return undefined;
  const other = queue[j]!.priority;
  const p = dir < 0 ? other + 1 : other - 1;
  return Math.max(-1000, Math.min(1000, p));
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
