// ACP `session/update` → HostUpdate. Plain ACP; the only agent-specific step is `driver.readTool`.

import type * as acp from "@agentclientprotocol/sdk";
import type { Driver, FileDiff, HostUpdate, RawToolCall, ToolCallSnapshot, ToolClass, ToolReading } from "./types.ts";

const CLASS_BY_KIND: Record<acp.ToolKind, ToolClass> = {
  read: "read",
  edit: "edit",
  delete: "edit",
  move: "edit",
  search: "search",
  execute: "shell",
  think: "think",
  fetch: "fetch",
  switch_mode: "other",
  other: "other",
};

export function isRecord(v: unknown): v is Record<string, unknown> {
  return typeof v === "object" && v !== null && !Array.isArray(v);
}

export function contentText(blocks: readonly acp.ToolCallContent[]): string {
  const out: string[] = [];
  for (const b of blocks) if (b.type === "content" && b.content.type === "text") out.push(b.content.text);
  return out.join("\n");
}

// The reading any ACP agent gets from `kind` alone; drivers refine it.
export function defaultReading(call: RawToolCall): ToolReading {
  const cls = call.kind ? CLASS_BY_KIND[call.kind] : "other";
  if (cls !== "shell") return { class: cls };
  const command = isRecord(call.rawInput) && typeof call.rawInput.command === "string" ? call.rawInput.command : "";
  return { class: cls, shell: { command } };
}

function blockText(b: acp.ContentBlock): string {
  return b.type === "text" ? b.text : "";
}

export class UpdateNormalizer {
  private readonly calls = new Map<string, RawToolCall>();
  private readonly plans = new Map<string, string>();

  constructor(private readonly driver: Driver) {}

  // Folds a tool_call / tool_call_update / permission toolCall into the stored state and returns the snapshot.
  foldTool(u: acp.ToolCallUpdate & { title?: string | null }): ToolCallSnapshot {
    return this.fold(u).snapshot;
  }

  private fold(u: acp.ToolCallUpdate): { snapshot: ToolCallSnapshot; plan?: acp.PlanEntry[] } {
    const prev = this.calls.get(u.toolCallId);
    const next: RawToolCall = {
      toolCallId: u.toolCallId,
      title: u.title ?? prev?.title ?? "",
      kind: u.kind ?? prev?.kind,
      status: u.status ?? prev?.status ?? "pending",
      content: u.content ?? prev?.content ?? [],
      locations: u.locations ?? prev?.locations ?? [],
      rawInput: u.rawInput !== undefined ? u.rawInput : prev?.rawInput,
      rawOutput: u.rawOutput !== undefined ? u.rawOutput : prev?.rawOutput,
      meta: { ...prev?.meta, ...(u._meta ?? {}) },
    };
    this.calls.set(u.toolCallId, next);
    const reading = this.driver.readTool(next);
    const snapshot = this.snapshot(next, reading);
    if (!reading.plan) return { snapshot };
    const key = JSON.stringify(reading.plan);
    if (this.plans.get(u.toolCallId) === key) return { snapshot };
    this.plans.set(u.toolCallId, key);
    return { snapshot, plan: reading.plan };
  }

  private snapshot(call: RawToolCall, reading: ToolReading): ToolCallSnapshot {
    const diffs: FileDiff[] = [];
    for (const c of call.content) {
      if (c.type === "diff") diffs.push({ path: c.path, oldText: c.oldText ?? null, newText: c.newText });
    }
    const snap: ToolCallSnapshot = {
      id: call.toolCallId,
      title: reading.title ?? call.title,
      acpKind: call.kind,
      class: reading.class,
      status: call.status,
      diffs,
      locations: call.locations.map((l) => l.path),
      text: contentText(call.content),
    };
    if (reading.mcp) snap.mcp = reading.mcp;
    if (reading.shell) snap.shell = reading.shell;
    if (call.rawInput !== undefined) snap.rawInput = call.rawInput;
    if (call.rawOutput !== undefined) snap.rawOutput = call.rawOutput;
    return snap;
  }

  apply(n: acp.SessionNotification): HostUpdate[] {
    const sessionId = n.sessionId;
    const u = n.update;
    switch (u.sessionUpdate) {
      case "agent_message_chunk": {
        const out: HostUpdate = { kind: "message", sessionId, text: blockText(u.content), content: u.content };
        if (u.messageId) out.messageId = u.messageId;
        return [out];
      }
      case "user_message_chunk":
        return [{ kind: "user_message", sessionId, text: blockText(u.content), content: u.content }];
      case "agent_thought_chunk":
        return [{ kind: "thought", sessionId, text: blockText(u.content) }];
      case "plan":
        return [{ kind: "plan", sessionId, entries: u.entries }];
      case "tool_call":
      case "tool_call_update": {
        const { snapshot, plan } = this.fold(u);
        const out: HostUpdate[] = [{ kind: "tool_call", sessionId, call: snapshot }];
        if (plan) out.push({ kind: "plan", sessionId, entries: plan });
        return out;
      }
      case "usage_update": {
        const out: HostUpdate = { kind: "usage", sessionId, context: { used: u.used, size: u.size } };
        if (u.cost) out.cost = { amount: u.cost.amount, currency: u.cost.currency };
        return [out];
      }
      default:
        return [{ kind: "info", sessionId, update: u }];
    }
  }
}
