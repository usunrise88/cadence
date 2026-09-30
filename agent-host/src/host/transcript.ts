// HostUpdates → transcript entries (the contract's HostEntry), coalesced before they are persisted (A1: a Claude run
// sends ~280 token-sized thought chunks). Consecutive message or thought chunks grow one entry per block; a tool call
// is one entry updated when what the Chat card shows changes; the plan is one entry per turn. Entries are keyed, so
// the latest state of each waits in `pending` until the next flush and a retried report upserts the same rows.

import { isAbsolute, relative } from "node:path";
import type { ToolCallSnapshot } from "../drivers/types.ts";
import type { HostUpdate } from "../drivers/types.ts";
import type { AgentToolCall, HostEntry } from "./api.ts";

const TEXT_CAP = 200_000;
const FIELD_CAP = 20_000;
const DIFF_CAP = 50_000;

function cap(s: string, n: number): string {
  return s.length <= n ? s : `${s.slice(0, n)}\n…(${s.length - n} more characters)`;
}

function capJSON(v: unknown): unknown {
  if (v === undefined) return undefined;
  const s = JSON.stringify(v);
  if (s === undefined || s.length <= FIELD_CAP) return v;
  return { truncated: true, preview: s.slice(0, FIELD_CAP) };
}

const APPROVAL_RE = /\\?"approvalId\\?"\s*:\s*\\?"(apr_[A-Za-z0-9-]+)/;
const JOB_RE = /\\?"jobId\\?"\s*:\s*\\?"(job_[A-Za-z0-9-]+)/;

// toolCall renders a snapshot as the contract's AgentToolCall, with worktree-relative paths and capped payloads.
export function toolCall(c: ToolCallSnapshot, root: string): AgentToolCall {
  const rel = (p: string): string => (isAbsolute(p) && root ? relative(root, p) || "." : p);
  const out: AgentToolCall = { id: c.id, title: c.title, class: c.class, status: c.status };
  if (c.mcp) {
    out.server = c.mcp.server;
    out.operation = c.mcp.tool;
  }
  if (c.diffs.length) {
    out.diffs = c.diffs.map((d) => {
      const x: { path: string; newText: string; oldText?: string } = { path: rel(d.path), newText: cap(d.newText, DIFF_CAP) };
      if (d.oldText !== null) x.oldText = cap(d.oldText, DIFF_CAP);
      return x;
    });
  }
  if (c.shell) {
    const sh: { command: string; exitCode?: number; output?: string } = { command: c.shell.command };
    if (c.shell.exitCode !== undefined) sh.exitCode = c.shell.exitCode;
    if (c.shell.output !== undefined) sh.output = cap(c.shell.output, FIELD_CAP);
    out.shell = sh;
  }
  if (c.locations.length) out.locations = c.locations.map(rel);
  if (c.text) out.text = cap(c.text, FIELD_CAP);
  if (c.rawInput !== undefined) out.input = capJSON(c.rawInput);
  if (c.rawOutput !== undefined) out.output = capJSON(c.rawOutput);
  const blob = `${c.text}\n${JSON.stringify(c.rawOutput ?? null)}`;
  const apr = APPROVAL_RE.exec(blob)?.[1];
  if (apr) out.approvalId = apr;
  const job = JOB_RE.exec(blob)?.[1];
  if (job) out.jobId = job;
  return out;
}

type Block = { kind: "agent_message" | "thought"; key: string; text: string; messageId?: string };

export class Transcript {
  private turn = 0;
  private blocks = 0;
  private open: Block | undefined;
  private readonly pending = new Map<string, HostEntry>();
  private readonly shown = new Map<string, string>();

  // root is the worktree: tool call paths are reported relative to it.
  constructor(private root = "") {}

  setRoot(root: string): void {
    this.root = root;
  }

  get currentTurn(): number {
    return this.turn;
  }

  startTurn(turn: number): void {
    this.close();
    this.turn = turn;
    this.blocks = 0;
  }

  put(e: HostEntry): void {
    this.pending.set(e.key, e);
  }

  notice(key: string, text: string, level: "info" | "warning" | "error" = "info"): void {
    this.close();
    this.put({ key, kind: "notice", turn: this.turn, text, level });
  }

  apply(u: HostUpdate): void {
    switch (u.kind) {
      case "message":
        this.text("agent_message", u.text, u.messageId);
        return;
      case "thought":
        this.text("thought", u.text);
        return;
      case "plan":
        this.put({
          key: `t${this.turn}:plan`,
          kind: "plan",
          turn: this.turn,
          plan: u.entries.map((e) => ({ content: e.content, status: e.status, priority: e.priority })),
        });
        return;
      case "tool_call": {
        this.close();
        const tc = toolCall(u.call, this.root);
        const key = `tool:${tc.id}`;
        const sig = JSON.stringify(tc);
        if (this.shown.get(key) === sig) return;
        this.shown.set(key, sig);
        this.put({ key, kind: "tool_call", turn: this.turn, toolCall: tc });
        return;
      }
      default:
        // user_message (replayed history), permission (the server records it), usage and state (the session
        // reports them), info (commands, modes, titles): not transcript entries.
        return;
    }
  }

  private text(kind: Block["kind"], chunk: string, messageId?: string): void {
    if (chunk === "") return;
    const b = this.open;
    if (!b || b.kind !== kind || (messageId !== undefined && b.messageId !== undefined && b.messageId !== messageId)) {
      this.close();
      this.blocks++;
      const tag = kind === "agent_message" ? "m" : "th";
      this.open = { kind, key: `t${this.turn}:${tag}${this.blocks}`, text: "", ...(messageId ? { messageId } : {}) };
    }
    const cur = this.open;
    if (!cur) return;
    cur.text = cap(cur.text + chunk, TEXT_CAP);
    this.put({ key: cur.key, kind: cur.kind, turn: this.turn, text: cur.text, final: false });
  }

  // close ends the open text block: its entry is final.
  close(): void {
    const b = this.open;
    if (!b) return;
    this.open = undefined;
    this.put({ key: b.key, kind: b.kind, turn: this.turn, text: b.text, final: true });
  }

  // pendingCount is how many entries wait for the next report.
  get pendingCount(): number {
    return this.pending.size;
  }

  take(max = 400): HostEntry[] {
    const out: HostEntry[] = [];
    for (const [k, e] of this.pending) {
      if (out.length >= max) break;
      out.push(e);
      this.pending.delete(k);
    }
    return out;
  }

  // restore puts entries back in front after a failed report (a newer state of the same key keeps their place), so
  // the transcript order the server assigns on first insert stays the order things happened.
  restore(entries: readonly HostEntry[]): void {
    const newer = new Map(this.pending);
    this.pending.clear();
    for (const e of entries) {
      this.pending.set(e.key, newer.get(e.key) ?? e);
      newer.delete(e.key);
    }
    for (const [k, e] of newer) this.pending.set(k, e);
  }
}
