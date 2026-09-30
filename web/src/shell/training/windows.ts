import type { AvailabilityWindow, AvailabilityWindows } from "@/api/gen/types.gen";

// Availability windows (docs/spec/08-resolutions.md R19): how Queue & GPU shows them and Settings → Compute edits them.

export const DAYS = ["mon", "tue", "wed", "thu", "fri", "sat", "sun"] as const satisfies AvailabilityWindow["days"];

const DAY_ORDER = DAYS;
export const DAY_LABEL = ["Mon", "Tue", "Wed", "Thu", "Fri", "Sat", "Sun"];

/** "Mon–Fri", "Sat, Sun", "Every day". */
export function formatDays(days: AvailabilityWindow["days"]): string {
  const idx = [...new Set(days)].map((d) => DAY_ORDER.indexOf(d)).filter((i) => i >= 0).sort((a, b) => a - b);
  if (idx.length === 7) return "Every day";
  const runs: number[][] = [];
  for (const i of idx) {
    const r = runs[runs.length - 1];
    if (r && r[r.length - 1] === i - 1) r.push(i);
    else runs.push([i]);
  }
  return runs.map((r) => (r.length >= 3 ? `${DAY_LABEL[r[0]!]}–${DAY_LABEL[r[r.length - 1]!]}` : r.map((i) => DAY_LABEL[i]).join(", "))).join(", ");
}

/** "training: Mon–Fri 20:00–08:00 (next day) UTC" per job kind; empty when every kind may run any time. */
export function formatWindows(w: AvailabilityWindows | undefined): string[] {
  if (!w) return [];
  const out: string[] = [];
  for (const [kind, list] of Object.entries(w)) {
    if (!list?.length) continue;
    const parts = list.map((x) => `${formatDays(x.days)} ${x.start}–${x.end}${x.end !== "24:00" && x.end <= x.start ? " (next day)" : ""} ${x.timezone ?? "UTC"}`);
    out.push(`${kind}: ${parts.join("; ")}`);
  }
  return out;
}

const START = /^([01][0-9]|2[0-3]):[0-5][0-9]$/;
const END = /^(([01][0-9]|2[0-3]):[0-5][0-9]|24:00)$/;

/** Why a window would be refused (the contract's patterns), or undefined. */
export function windowError(w: AvailabilityWindow): string | undefined {
  if (w.days.length === 0) return "Pick at least one day";
  if (!START.test(w.start)) return "Opens at HH:MM (00:00–23:59)";
  if (!END.test(w.end)) return "Closes at HH:MM (or 24:00)";
  if (w.start === w.end) return "Opens and closes at the same time";
  return undefined;
}
