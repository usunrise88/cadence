import type { JobLogLine } from "@/api/gen/types.gen";

// Pure pieces of the log view: level filter, merge of history and live lines, the copy format and the virtual
// window. The server filters history (jobLogs.list level/text); live lines are filtered here with the same rules.

export type LogLevel = JobLogLine["level"];
export const LOG_LEVELS: LogLevel[] = ["debug", "info", "warn", "error"];

const RANK: Record<LogLevel, number> = { debug: 0, info: 1, warn: 2, error: 3 };

/** Minimum-level and case-insensitive text match, as jobLogs.list applies them. */
export function matches(line: JobLogLine, level: LogLevel, text: string): boolean {
  if ((RANK[line.level] ?? 1) < RANK[level]) return false;
  return !text || line.msg.toLowerCase().includes(text.toLowerCase());
}

/** History then live lines, in line-number order, without duplicates (a refetch may overlap live events). */
export function mergeLines(history: JobLogLine[], live: JobLogLine[]): JobLogLine[] {
  const last = history.length ? history[history.length - 1]!.seq : 0;
  const extra = live.filter((l) => l.seq > last);
  if (extra.length === 0) return history;
  const seen = new Set<number>();
  const out = [...history];
  for (const l of extra) {
    if (seen.has(l.seq)) continue;
    seen.add(l.seq);
    out.push(l);
  }
  return out;
}

/** Keeps at most `max` lines (the newest). */
export function cap(lines: JobLogLine[], max: number): JobLogLine[] {
  return lines.length > max ? lines.slice(lines.length - max) : lines;
}

function fieldsText(f: JobLogLine["fields"]): string {
  if (!f || typeof f !== "object") return "";
  const parts = Object.entries(f).map(([k, v]) => `${k}=${typeof v === "string" ? v : JSON.stringify(v)}`);
  return parts.length ? ` ${parts.join(" ")}` : "";
}

/** One line as copied: ISO time, level, message, fields as key=value. */
export function formatLine(l: JobLogLine): string {
  return `${l.t} ${l.level.toUpperCase().padEnd(5)} ${l.msg}${fieldsText(l.fields)}`;
}

/** The rows to render for a fixed-height virtual list: [start, end) with overscan. */
export function visibleRange(scrollTop: number, viewport: number, rowHeight: number, count: number, overscan = 10): [number, number] {
  if (count === 0) return [0, 0];
  const first = Math.floor(Math.max(0, scrollTop) / rowHeight);
  const rows = Math.ceil(Math.max(0, viewport) / rowHeight) + 1;
  return [Math.max(0, first - overscan), Math.min(count, first + rows + overscan)];
}

/** Whether a scroll position counts as "at the bottom" (follow stays on). */
export function atBottom(scrollTop: number, viewport: number, total: number, slack = 4): boolean {
  return scrollTop + viewport >= total - slack;
}
