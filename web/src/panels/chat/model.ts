import type { AgentMessage, AgentSession, AgentToolCall } from "@/api/gen/types.gen";
import { operations, type OperationId } from "@/api/operations.gen";

// Pure presentation logic of the Chat panel (docs/spec/05-agents.md "What the Chat panel shows"): which transcript
// entries render, the header's state and budget, what a tool call names, and the inline diff of a file edit.

/** Entries the transcript renders: a turn's start is implied by the next user message; its end shows usage. */
export function visibleEntries(items: AgentMessage[]): AgentMessage[] {
  return items.filter((m) => !(m.kind === "turn" && m.turnInfo?.state !== "ended"));
}

export type Tone = "neutral" | "running" | "done" | "warning" | "failed";

/** The header chip: running / waiting approval / paused with the reason / ended. */
const DRIVER_SHORT: Record<string, string> = { "claude-code": "CC", opencode: "OC" };

/** The tab's short name: "CC · S4" (Claude Code, session 4), "OC · S1" (opencode). */
export function tabLabel(s: Pick<AgentSession, "driver" | "number">): string {
  const short = DRIVER_SHORT[s.driver] ?? s.driver.split(/[^a-z0-9]+/i).map((w) => w.charAt(0).toUpperCase()).join("");
  return `${short} · S${s.number}`;
}

/** The tab icon's colour: working, ready for you, wants a decision, paused, failed; none once it is over. */
export type TabTone = "working" | "ready" | "attention" | "paused" | "failed" | "none";

export function tabTone(s: Pick<AgentSession, "state" | "busy">): TabTone {
  switch (s.state) {
    case "running":
      return s.busy ? "working" : "ready";
    case "waiting_approval":
      return "attention";
    case "paused":
      return "paused";
    case "failed":
      return "failed";
    default:
      return "none";
  }
}

export function sessionStatus(s: AgentSession): { label: string; tone: Tone; detail?: string } {
  const pending = s.pendingControl ? ` · ${s.pendingControl === "cancel" ? "stopping" : s.pendingControl === "end" ? "ending" : `${s.pendingControl === "pause" ? "pausing" : "resuming"}`}…` : "";
  switch (s.state) {
    case "created":
      return { label: `starting${pending}`, tone: "neutral", detail: "Waiting for the agent host to take the session" };
    case "running":
      return { label: `${s.busy ? "running" : "idle"}${pending}`, tone: "running", detail: s.busy ? `Turn ${s.turn ?? 1} in progress` : "Waiting for your message" };
    case "waiting_approval":
      return { label: `waiting approval${pending}`, tone: "warning", detail: "A request below waits for your decision" };
    case "paused":
      return { label: `paused${pending}`, tone: "warning", detail: s.pauseReason?.message };
    case "done":
      return { label: "done", tone: "done" };
    case "failed":
      return { label: "failed", tone: "failed", detail: s.error };
    case "cancelled":
      return { label: "cancelled", tone: "neutral" };
  }
}

export type Meter = { used: number; limit: number; ratio: number };

function meter(used: number, limit: number): Meter {
  return { used, limit, ratio: limit > 0 ? Math.min(1, used / limit) : 0 };
}

/** Turns and tokens used against the session budget. */
export function budgetUse(s: AgentSession): { turns: Meter; tokens: Meter } {
  return { turns: meter(s.use.turns, s.budget.turns), tokens: meter(s.use.inputTokens + s.use.outputTokens, s.budget.tokens) };
}

/** 1234 → "1.2k", 1_500_000 → "1.5M". */
export function compact(n: number): string {
  if (n < 1000) return String(n);
  if (n < 1_000_000) return `${(n / 1000).toFixed(n < 10_000 ? 1 : 0)}k`;
  return `${(n / 1_000_000).toFixed(1)}M`;
}

/** The Cadence tool result envelope (internal/mcp): the answer's status, data or problem, and help. */
export type ToolResult = { operation?: string; status?: number; data?: unknown; error?: { title?: string; detail?: string; type?: string; status?: number }; help?: string };

export function toolResult(tc: AgentToolCall): ToolResult | undefined {
  const o = tc.output;
  if (!o || typeof o !== "object") return undefined;
  const r = o as ToolResult;
  return "status" in r || "data" in r || "error" in r ? r : undefined;
}

function obj(v: unknown): Record<string, unknown> | undefined {
  return v && typeof v === "object" && !Array.isArray(v) ? (v as Record<string, unknown>) : undefined;
}

/** The operation of an MCP call: the one the host reported, else the tool's name after the server prefix. */
export function toolOperation(tc: AgentToolCall): string | undefined {
  if (tc.operation) return tc.operation;
  const m = /(?:^|__|\.|:)([a-z][a-zA-Z0-9]*\.[a-z]+)$/.exec(tc.title.trim());
  return m ? m[1] : undefined;
}

/**
 * The entity an MCP call acted on, as a reference (`@mix:mix_1`): the operation's entity kind with the id from the
 * arguments (id, or the body's id) or from the answer's data.
 */
export function toolEntityRef(tc: AgentToolCall): string | undefined {
  const op = toolOperation(tc);
  const spec = op ? operations[op as OperationId] : undefined;
  if (!spec) return undefined;
  const input = obj(tc.input);
  const data = obj(toolResult(tc)?.data);
  const id = [input?.id, obj(input?.body)?.id, data?.id, obj(data?.draft)?.entityId].find((v): v is string => typeof v === "string" && v.length > 0);
  if (!id) return undefined;
  const kind = spec.kind === "agent_session" ? "session" : spec.kind === "help_article" ? "help" : spec.kind;
  return /^[A-Za-z0-9][A-Za-z0-9._/-]*$/.test(id) ? `@${kind}:${id}` : undefined;
}

/** The draft a call left (draftable kinds under the draft policy): its revision, for the "Draft" line. */
export function toolDraft(tc: AgentToolCall): { id: string; rev?: number } | undefined {
  const d = obj(obj(toolResult(tc)?.data)?.draft);
  return d && typeof d.id === "string" ? { id: d.id, rev: typeof d.rev === "number" ? d.rev : undefined } : undefined;
}

function range(v: unknown): number | undefined {
  const r = obj(v);
  return typeof r?.value === "number" ? r.value : typeof v === "number" ? v : undefined;
}

function duration(s: number): string {
  if (s < 90) return `${Math.round(s)} s`;
  if (s < 5400) return `${Math.round(s / 60)} min`;
  return `${(s / 3600).toFixed(1)} h`;
}

/** A dry run's estimate inline: GPU-hours (±), duration, card and data volume. Undefined for anything else. */
export function dryRunEstimate(tc: AgentToolCall): string | undefined {
  const input = obj(tc.input);
  const data = obj(toolResult(tc)?.data);
  const est = obj(data?.estimate) ?? (input?.dryRun === true || input?.dryRun === "true" ? data : undefined);
  if (!est) return undefined;
  const parts: string[] = [];
  const gpu = range(est.gpuHours);
  if (gpu !== undefined) {
    const pm = typeof est.plusMinus === "number" ? ` ±${Math.round(est.plusMinus * 100)}%` : "";
    parts.push(`${gpu.toFixed(gpu >= 10 ? 0 : 1)} GPU-h${pm}`);
  }
  const dur = range(est.durationSeconds);
  if (dur !== undefined) parts.push(`~${duration(dur)}`);
  const card = obj(est.card);
  if (card && typeof card.host === "string") parts.push(`${card.host} card ${String(card.index ?? "")}`.trim());
  const d = obj(est.data);
  if (d && typeof d.hours === "number") parts.push(`${d.hours.toFixed(1)} h of audio`);
  if (parts.length === 0) return input?.dryRun ? "Dry run: nothing was written" : undefined;
  return `Dry run: ${parts.join(" · ")}`;
}

// ---------------------------------------------------------------- inline diff

export type DiffLine = { op: " " | "+" | "-"; text: string } | { op: "…"; count: number };

const MAX_CELLS = 400_000;

/**
 * A line diff (LCS) of a file edit, unchanged runs longer than 2 × context folded to "… n unchanged lines". A file
 * too large to diff in the browser shows as removed and added.
 */
export function lineDiff(oldText: string | undefined, newText: string, context = 3): DiffLine[] {
  const a = oldText === undefined || oldText === "" ? [] : oldText.replace(/\n$/, "").split("\n");
  const b = newText === "" ? [] : newText.replace(/\n$/, "").split("\n");
  let ops: { op: " " | "+" | "-"; text: string }[];
  if (a.length * b.length > MAX_CELLS) {
    ops = [...a.map((text) => ({ op: "-" as const, text })), ...b.map((text) => ({ op: "+" as const, text }))];
  } else {
    // Trim the common head and tail, then LCS on the middle.
    let head = 0;
    while (head < a.length && head < b.length && a[head] === b[head]) head++;
    let tail = 0;
    while (tail < a.length - head && tail < b.length - head && a[a.length - 1 - tail] === b[b.length - 1 - tail]) tail++;
    const am = a.slice(head, a.length - tail);
    const bm = b.slice(head, b.length - tail);
    const n = am.length;
    const m = bm.length;
    const lcs: Uint32Array[] = Array.from({ length: n + 1 }, () => new Uint32Array(m + 1));
    for (let i = n - 1; i >= 0; i--) for (let j = m - 1; j >= 0; j--) lcs[i]![j] = am[i] === bm[j] ? lcs[i + 1]![j + 1]! + 1 : Math.max(lcs[i + 1]![j]!, lcs[i]![j + 1]!);
    const mid: { op: " " | "+" | "-"; text: string }[] = [];
    let i = 0;
    let j = 0;
    while (i < n && j < m) {
      if (am[i] === bm[j]) {
        mid.push({ op: " ", text: am[i]! });
        i++;
        j++;
      } else if (lcs[i + 1]![j]! >= lcs[i]![j + 1]!) mid.push({ op: "-", text: am[i++]! });
      else mid.push({ op: "+", text: bm[j++]! });
    }
    while (i < n) mid.push({ op: "-", text: am[i++]! });
    while (j < m) mid.push({ op: "+", text: bm[j++]! });
    ops = [...a.slice(0, head).map((text) => ({ op: " " as const, text })), ...mid, ...b.slice(b.length - tail).map((text) => ({ op: " " as const, text }))];
  }
  // Fold long unchanged runs.
  const out: DiffLine[] = [];
  let k = 0;
  while (k < ops.length) {
    if (ops[k]!.op !== " ") {
      out.push(ops[k]!);
      k++;
      continue;
    }
    let end = k;
    while (end < ops.length && ops[end]!.op === " ") end++;
    const run = end - k;
    const lead = k === 0 ? 0 : context;
    const trail = end === ops.length ? 0 : context;
    if (run > lead + trail + 1) {
      out.push(...ops.slice(k, k + lead));
      out.push({ op: "…", count: run - lead - trail });
      out.push(...ops.slice(end - trail, end));
    } else {
      out.push(...ops.slice(k, end));
    }
    k = end;
  }
  return out;
}

/** "+3 −1" of a diff. */
export function diffStat(lines: DiffLine[]): { added: number; removed: number } {
  let added = 0;
  let removed = 0;
  for (const l of lines) {
    if (l.op === "+") added++;
    else if (l.op === "-") removed++;
  }
  return { added, removed };
}

/** Whether an entry answers a tool call (the badge's jump target). */
export function entryMatchesToolCall(m: AgentMessage, toolCallId: string): boolean {
  return m.toolCall?.id === toolCallId || m.permission?.toolCallId === toolCallId;
}

// ---------------------------------------------------------------- windowing (long transcripts)

export const VIRTUALIZE_AFTER = 200;

/** Offsets of each row from measured heights (estimate for rows not measured yet). */
export function rowOffsets(ids: string[], heights: Map<string, number>, estimate: number): number[] {
  const out = new Array<number>(ids.length + 1);
  out[0] = 0;
  for (let i = 0; i < ids.length; i++) out[i + 1] = out[i]! + (heights.get(ids[i]!) ?? estimate);
  return out;
}

/** The rows to render for a scroll position: those intersecting the viewport, plus overscan on both sides. */
export function visibleRange(offsets: number[], scrollTop: number, viewport: number, overscan = 8): { first: number; last: number } {
  const n = offsets.length - 1;
  if (n <= 0) return { first: 0, last: 0 };
  let lo = 0;
  let hi = n - 1;
  while (lo < hi) {
    const mid = (lo + hi + 1) >> 1;
    if (offsets[mid]! <= scrollTop) lo = mid;
    else hi = mid - 1;
  }
  let last = lo;
  while (last < n && offsets[last]! < scrollTop + viewport) last++;
  return { first: Math.max(0, lo - overscan), last: Math.min(n, last + overscan) };
}
