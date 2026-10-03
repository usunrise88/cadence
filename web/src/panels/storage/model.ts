import type { Mount, MountNew, StorageDataset, StorageUse } from "@/api/gen/types.gen";

// Pure helpers of the Storage panel: byte and rate formatting, the health line of a mount, the cache bar's marks and
// the request a person's Add mount form becomes.

const UNITS = ["B", "KB", "MB", "GB", "TB", "PB"];

/** Decimal units, one decimal from KB up: 1 500 000 → "1.5 MB". */
export function formatBytes(n: number | undefined): string {
  if (n === undefined || !Number.isFinite(n)) return "—";
  let v = n;
  let u = 0;
  while (Math.abs(v) >= 1000 && u < UNITS.length - 1) {
    v /= 1000;
    u++;
  }
  return u === 0 ? `${Math.round(v)} B` : `${v.toFixed(1)} ${UNITS[u]}`;
}

/** "free 812 GB of 1.2 TB · 410 MB/s" from the last health check, or what is missing. */
export function healthLine(m: Mount): string {
  const h = m.health;
  const parts: string[] = [];
  if (h.freeBytes !== undefined && h.totalBytes !== undefined) parts.push(`free ${formatBytes(h.freeBytes)} of ${formatBytes(h.totalBytes)}`);
  if (h.throughputMBps !== undefined) parts.push(`${h.throughputMBps} MB/s`);
  if (h.writable) parts.push("writable");
  if (h.host) parts.push(`from ${h.host}`);
  if (h.state === "unhealthy" && h.detail) return h.detail;
  if (parts.length === 0) return h.state === "unknown" ? "not checked yet" : (h.detail ?? "");
  return parts.join(" · ");
}

/** "3 412 files · 81.2 GB · 2 blob copies", or "not scanned". */
export function inventoryLine(m: Mount): string {
  const inv = m.inventory;
  if (!inv) return "not scanned";
  const parts = [`${inv.files.toLocaleString("en")} files`, formatBytes(inv.bytes)];
  if (inv.blobs > 0) parts.push(`${inv.blobs.toLocaleString("en")} blob copies`);
  if (inv.truncated) parts.push("truncated");
  return parts.join(" · ");
}

/** Where the cache stands against its marks: over the high mark, between the marks, or fine. */
export function cacheLevel(u: StorageUse): "over" | "between" | "ok" {
  if (u.usedPct > u.highWaterPct) return "over";
  if (u.usedPct > u.lowWaterPct) return "between";
  return "ok";
}

/** Which cache action a dataset version offers: evict a cached evictable one, materialise an evicted one. */
export function datasetAction(d: StorageDataset): { command: "datasets.evict" | "datasets.materialize"; label: string; enabled: true | string } {
  if (d.state === "evicted") return { command: "datasets.materialize", label: "Materialize", enabled: true };
  if (d.pinned.length > 0) return { command: "datasets.evict", label: "Evict", enabled: `Pinned: ${d.pinned.join("; ")}` };
  if (!d.evictable) {
    const missing = (d.shards ?? 0) - d.copies;
    return { command: "datasets.evict", label: "Evict", enabled: `${missing} of ${d.shards ?? 0} shards exist on no mount` };
  }
  return { command: "datasets.evict", label: "Evict", enabled: true };
}

export type MountForm = {
  name: string;
  kind: MountNew["kind"];
  root: string;
  endpoint: string;
  credentials: string;
  revision: string;
  readOnly: boolean;
};

export const EMPTY_FORM: MountForm = { name: "", kind: "local", root: "", endpoint: "", credentials: "", revision: "", readOnly: true };

/** The mounts.new body of a form: only the fields its kind takes. */
export function mountBody(f: MountForm): MountNew {
  const body: MountNew = { name: f.name.trim(), kind: f.kind, root: f.root.trim(), readOnly: f.kind === "hf" ? true : f.readOnly };
  if (f.kind === "s3") {
    body.endpoint = f.endpoint.trim();
    body.credentials = f.credentials.trim();
  }
  if (f.kind === "hf") {
    body.revision = f.revision.trim();
    if (f.credentials.trim()) body.credentials = f.credentials.trim();
  }
  return body;
}

/** The root field's hint for a kind. */
export function rootHint(kind: MountNew["kind"]): string {
  switch (kind) {
    case "s3":
      return "bucket[/prefix]";
    case "hf":
      return "datasets/<org>/<name> or <org>/<model>";
    default:
      return "absolute path the workers and the control plane see, e.g. /mnt/corpora";
  }
}
