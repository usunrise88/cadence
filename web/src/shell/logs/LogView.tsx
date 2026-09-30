import { useEffect, useLayoutEffect, useMemo, useRef, useState, type UIEvent } from "react";
import { useQuery } from "@tanstack/react-query";
import { Copy, NavArrowDown } from "iconoir-react";
import { jobLogsListOptions } from "@/api/gen/@tanstack/react-query.gen";
import type { JobLogLine } from "@/api/gen/types.gen";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { NativeSelect } from "@/components/ui/native-select";
import { cn } from "@/lib/utils";
import { copyText } from "@/shell/charts/table";
import { useElementSize, useOwnerDocument } from "@/shell/charts/hooks";
import { useTopic } from "@/shell/panel/context";
import { atBottom, cap, formatLine, LOG_LEVELS, matches, mergeLines, visibleRange, type LogLevel } from "./model";

// The log view (docs/spec/11-ui-panels.md, Logs): a job's log from its worker. History comes from jobLogs.list
// (the last lines, filtered by level and text on the server); live lines arrive on job.{id}.log while the panel is
// visible and are filtered here the same way. Follow keeps the newest line in view until the person scrolls up.
// Rows have a fixed height, so a long log renders only the lines in view. The Logs panel shows it for the active
// job; Pipeline run embeds it for a step's job.

export const LOG_ROW_HEIGHT = 20;
const HISTORY = 2000;
const MAX_LINES = 20000;

const LEVEL_CLASS: Record<LogLevel, string> = {
  debug: "text-muted-foreground",
  info: "text-foreground",
  warn: "text-status-warning-foreground",
  error: "text-status-failed-foreground",
};

type LiveBatch = { jobId?: string; lines?: JobLogLine[]; dropped?: number };

export type LogViewProps = {
  jobId: string;
  /** Accessible name of the log list. */
  label?: string;
  /** Smaller toolbar for an embedded view. */
  compact?: boolean;
  className?: string;
};

export function LogView({ jobId, label, compact, className }: LogViewProps) {
  const [level, setLevel] = useState<LogLevel>("debug");
  const [text, setText] = useState("");
  const [query, setQuery] = useState("");
  // The server filter follows the search box after a short pause.
  useEffect(() => {
    const t = setTimeout(() => setQuery(text.trim()), 250);
    return () => clearTimeout(t);
  }, [text]);
  const [follow, setFollow] = useState(true);
  const [live, setLive] = useState<JobLogLine[]>([]);
  const [status, setStatus] = useState("");

  const opts = jobLogsListOptions({ path: { id: jobId }, query: { tail: true, limit: HISTORY, ...(level !== "debug" ? { level } : {}), ...(query ? { text: query } : {}) } });
  const history = useQuery({ ...opts, staleTime: Infinity });
  const refetch = history.refetch;
  // A new job or filter starts from its own history.
  useEffect(() => setLive([]), [jobId, level, query]);

  useTopic([`job.${jobId}.log`], (batch) => {
    let gap = false;
    const add: JobLogLine[] = [];
    for (const e of batch) {
      const p = e.payload as LiveBatch | undefined;
      if (!p || (p.jobId && p.jobId !== jobId)) continue;
      if (p.dropped) gap = true;
      for (const l of p.lines ?? []) if (matches(l, level, query)) add.push(l);
    }
    if (gap) void refetch(); // the event carried only the first lines of a large batch
    if (add.length) setLive((cur) => cap([...cur, ...add], MAX_LINES));
  });

  const lines = useMemo(() => cap(mergeLines(history.data?.items ?? [], live), MAX_LINES), [history.data, live]);

  const scroller = useRef<HTMLDivElement>(null);
  const [el, setEl] = useState<HTMLDivElement | null>(null);
  const doc = useOwnerDocument(el);
  const { height } = useElementSize(el, doc);
  const [scrollTop, setScrollTop] = useState(0);
  const total = lines.length * LOG_ROW_HEIGHT;
  const [start, end] = visibleRange(scrollTop, height, LOG_ROW_HEIGHT, lines.length);

  useLayoutEffect(() => {
    const s = scroller.current;
    if (!follow || !s) return;
    s.scrollTop = s.scrollHeight;
    setScrollTop(s.scrollTop);
  }, [follow, lines.length, height]);

  const onScroll = (e: UIEvent<HTMLDivElement>) => {
    const s = e.currentTarget;
    setScrollTop(s.scrollTop);
    const bottom = atBottom(s.scrollTop, s.clientHeight, s.scrollHeight);
    if (follow && !bottom) setFollow(false);
    else if (!follow && bottom && lines.length > 0) setFollow(true);
  };

  const copy = async () => {
    const ok = await copyText(lines.map(formatLine).join("\n"), scroller.current);
    setStatus(ok ? `Copied ${lines.length} line${lines.length === 1 ? "" : "s"}` : "Copy failed: the clipboard is not available");
  };

  return (
    <div className={cn("flex h-full min-h-0 flex-col", className)} data-slot="log-view" data-job={jobId}>
      <div className={cn("flex shrink-0 flex-wrap items-center gap-1.5 border-b px-2 text-xs", compact ? "py-1" : "min-h-9 py-1")}>
        <label className="sr-only" htmlFor={`log-level-${jobId}`}>
          Minimum level
        </label>
        <NativeSelect id={`log-level-${jobId}`} className="h-6 w-auto text-xs" value={level} onChange={(e) => setLevel(e.target.value as LogLevel)}>
          {LOG_LEVELS.map((l) => (
            <option key={l} value={l}>
              {l === "debug" ? "All levels" : `${l} and above`}
            </option>
          ))}
        </NativeSelect>
        <Input type="search" className="h-6 w-40 min-w-24 flex-1 text-xs" placeholder="Search the log" aria-label="Search the log" value={text} onChange={(e) => setText(e.target.value)} />
        <Button
          size="xs"
          variant={follow ? "secondary" : "ghost"}
          aria-pressed={follow}
          onClick={() => setFollow((f) => !f)}
          title="Keep the newest line in view"
          data-slot="log-follow"
        >
          <NavArrowDown aria-hidden />
          Follow
        </Button>
        <Button size="xs" variant="ghost" onClick={() => void copy()} disabled={lines.length === 0} aria-label="Copy the shown lines">
          <Copy aria-hidden />
          Copy
        </Button>
        <span className="ml-auto text-muted-foreground tabular-nums" data-slot="log-count">
          {lines.length.toLocaleString()} line{lines.length === 1 ? "" : "s"}
        </span>
        <span role="status" aria-live="polite" className="sr-only">
          {status}
        </span>
      </div>
      <div
        ref={(n) => {
          scroller.current = n;
          setEl(n);
        }}
        className="relative min-h-0 flex-1 overflow-auto font-mono text-xs"
        onScroll={onScroll}
        tabIndex={0}
        role="log"
        aria-label={label ?? `Log of ${jobId}`}
        aria-live="off"
        data-testid="log-lines"
      >
        {history.isLoading ? <p className="p-2 text-muted-foreground">Loading…</p> : null}
        {history.error ? <p className="p-2 text-status-failed-foreground">The log could not be read.</p> : null}
        {!history.isLoading && !history.error && lines.length === 0 ? (
          <p className="p-2 text-muted-foreground">{query || level !== "debug" ? "No line matches." : "No log lines yet; they stream in while the job runs."}</p>
        ) : null}
        <div style={{ height: total }} className="relative">
          {lines.slice(start, end).map((l, i) => (
            <LogRow key={l.seq} line={l} top={(start + i) * LOG_ROW_HEIGHT} />
          ))}
        </div>
      </div>
    </div>
  );
}

function LogRow({ line, top }: { line: JobLogLine; top: number }) {
  const fields = line.fields && typeof line.fields === "object" ? Object.entries(line.fields) : [];
  return (
    <div
      className="absolute inset-x-0 flex items-center gap-2 px-2 whitespace-pre hover:bg-hover"
      style={{ top, height: LOG_ROW_HEIGHT }}
      data-seq={line.seq}
      data-level={line.level}
    >
      <span className="w-10 shrink-0 text-right text-muted-foreground select-none tabular-nums">{line.seq}</span>
      <time className="shrink-0 text-muted-foreground tabular-nums" dateTime={line.t}>
        {new Date(line.t).toLocaleTimeString()}
      </time>
      <span className={cn("w-11 shrink-0 font-medium uppercase", LEVEL_CLASS[line.level])}>{line.level}</span>
      <span className={cn("min-w-0 truncate", LEVEL_CLASS[line.level])} title={line.msg}>
        {line.msg}
        {fields.length ? <span className="text-muted-foreground"> {fields.map(([k, v]) => `${k}=${typeof v === "string" ? v : JSON.stringify(v)}`).join(" ")}</span> : null}
      </span>
    </div>
  );
}
