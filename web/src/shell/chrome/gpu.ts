import type { CardTelemetry, ComputeHost } from "@/api/gen/types.gen";

// Pure logic of the status bar's GPU badge: the latest reading per card, seeded from compute.list and replaced by
// `gpu.telemetry` events (docs/spec/06-platform.md "Worker protocol": card telemetry rides on claims and heartbeats).

export type CardReading = {
  key: string;
  host: string;
  index: number;
  name: string;
  usedMb?: number;
  totalMb?: number;
  capGb?: number;
  util?: number;
  /** Seconds since the epoch of the reading. */
  at: number;
};

export type Readings = Record<string, CardReading>;

/** A reading older than this reads as stale: no worker has claimed or beaten on that host lately. */
export const STALE_SECONDS = 120;

const key = (host: string, index: number) => `${host}#${index}`;

/** The readings compute.list carries (each card's last telemetry report). */
export function seedReadings(hosts: ComputeHost[]): Readings {
  const out: Readings = {};
  for (const h of hosts)
    for (const c of h.cards) {
      const t = c.telemetry;
      if (!t || (t.memoryUsedMb === undefined && t.utilization === undefined)) continue;
      out[key(h.name, c.index)] = {
        key: key(h.name, c.index),
        host: h.name,
        index: c.index,
        name: t.name ?? c.name,
        usedMb: t.memoryUsedMb,
        totalMb: t.memoryTotalMb ?? c.memoryGb * 1024,
        capGb: c.memoryCapGb,
        util: t.utilization,
        at: Date.parse(t.reportedAt) / 1000,
      };
    }
  return out;
}

/** Merges one `gpu.telemetry` payload; an older reading never replaces a newer one. */
export function applyTelemetry(cur: Readings, host: string, cards: CardTelemetry[], at: number): Readings {
  let next: Readings | undefined;
  for (const c of cards) {
    if (c.memoryUsedMb === undefined && c.utilization === undefined) continue;
    const k = key(host, c.index);
    const prev = cur[k];
    if (prev && prev.at > at) continue;
    next ??= { ...cur };
    next[k] = {
      key: k,
      host,
      index: c.index,
      name: c.name ?? prev?.name ?? `GPU ${c.index}`,
      usedMb: c.memoryUsedMb,
      totalMb: c.memoryTotalMb ?? prev?.totalMb,
      capGb: prev?.capGb,
      util: c.utilization,
      at,
    };
  }
  return next ?? cur;
}

/** The seed from compute.list with live readings over it: a live reading wins unless the seed is newer; the seed carries each card's cap. */
export function mergeReadings(seed: Readings, live: Readings): Readings {
  const out: Readings = { ...seed };
  for (const [k, r] of Object.entries(live)) {
    const s = seed[k];
    if (!s || s.at <= r.at) out[k] = { ...r, capGb: s?.capGb ?? r.capGb };
  }
  return out;
}

const gb = (mb: number) => (Math.round((mb / 1024) * 10) / 10).toString();

/** "23.8/48 GB · 41 %" — memory used of total and utilisation, what is known of it. */
export function formatReading(r: CardReading): string {
  const parts: string[] = [];
  if (r.usedMb !== undefined) parts.push(r.totalMb ? `${gb(r.usedMb)}/${gb(r.totalMb)} GB` : `${gb(r.usedMb)} GB`);
  if (r.util !== undefined) parts.push(`${Math.round(r.util)} %`);
  return parts.join(" · ");
}

/** Cards in host, then index order. */
export function sortedReadings(r: Readings): CardReading[] {
  return Object.values(r).sort((a, b) => a.host.localeCompare(b.host) || a.index - b.index);
}

export const isStale = (r: CardReading, now: number) => now - r.at > STALE_SECONDS;
