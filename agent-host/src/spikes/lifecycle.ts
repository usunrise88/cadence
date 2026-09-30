// Spike A1 lifecycle, driver-agnostic: MCP turn, file edit (+ plan), shell, cancel mid-turn, then a new agent
// process that restores the session. The live spike (a1.ts) runs it against real agents and records the ACP
// traffic; the driver contract tests run it against a replayer speaking those recordings.

import type * as acp from "@agentclientprotocol/sdk";
import type { AnyMessage } from "@agentclientprotocol/sdk";
import type { Direction } from "../acp/transport.ts";
import { Agent, RestoreUnsupportedError } from "../drivers/agent.ts";
import type { Driver, FileDiff, HostUpdate, LaunchOptions, PermissionOptionKind, ToolCallSnapshot } from "../drivers/types.ts";

export type Phase = "main" | "restore";

export interface RecordedMessage {
  d: Direction;
  t: number; // ms since the phase started
  m: AnyMessage;
}

export interface LifecycleOptions {
  driver: Driver;
  cwd: string;
  mcp: { url: string; token: string };
  launch: (phase: Phase) => Omit<LaunchOptions, "cwd">;
  record?: (phase: Phase, msg: RecordedMessage) => void;
  log?: (line: string) => void;
  cancelFallbackMs?: number;
}

export interface StepResult {
  name: string;
  prompt: string;
  stopReason: acp.StopReason | "error";
  error?: string;
  ms: number;
  firstUpdateMs?: number;
  firstThoughtMs?: number;
  firstTokenMs?: number;
  cancelToStopMs?: number;
  text: string;
}

export interface PermissionSeen {
  step: string;
  title: string;
  class: ToolCallSnapshot["class"];
  options: PermissionOptionKind[];
  answered: string;
}

export interface LifecycleResult {
  driver: string;
  agentInfo?: acp.Implementation | null;
  capabilities?: acp.AgentCapabilities;
  initializeMs: number;
  newSessionMs: number;
  sessionId: string;
  steps: StepResult[];
  hostKinds: Record<string, number>;
  acpKinds: Record<string, number>;
  permissions: PermissionSeen[];
  mcpCalls: ToolCallSnapshot[];
  editCalls: ToolCallSnapshot[];
  shellCalls: ToolCallSnapshot[];
  diffs: FileDiff[];
  plans: acp.PlanEntry[][];
  usage: HostUpdate[];
  restore: { mode: string; ms: number; replayedUpdates: number; error?: string };
  clientFsCalls: string[];
}

export const PROMPTS = {
  mcp: 'Call the tool "echo" of the MCP server "cadence" with text "ping". Then reply with the exact text the tool returned and nothing else.',
  edit: 'First create a task list (todo list) with two tasks: "edit notes.txt" and "reply". Then in notes.txt replace the word "alpha" with "beta" using your file edit tool (no shell), mark both tasks done, and reply "done".',
  shell: 'Run this shell command exactly: echo a1-shell-ok > shell.txt. Reply "done".',
  cancel: "Without using any tool, write the numbers from one to three hundred as English words in your reply, one per line, with no other text.",
  restore: "What exact text did the echo tool return earlier in this conversation? Reply with that text only.",
} as const;

function count(map: Record<string, number>, key: string): void {
  map[key] = (map[key] ?? 0) + 1;
}

export async function runLifecycle(o: LifecycleOptions): Promise<LifecycleResult> {
  const log = o.log ?? (() => undefined);
  const res: LifecycleResult = {
    driver: o.driver.name,
    initializeMs: 0,
    newSessionMs: 0,
    sessionId: "",
    steps: [],
    hostKinds: {},
    acpKinds: {},
    permissions: [],
    mcpCalls: [],
    editCalls: [],
    shellCalls: [],
    diffs: [],
    plans: [],
    usage: [],
    restore: { mode: "none", ms: 0, replayedUpdates: 0 },
    clientFsCalls: [],
  };
  const mcpServers: acp.McpServer[] = [
    { type: "http", name: "cadence", url: o.mcp.url, headers: [{ name: "Authorization", value: `Bearer ${o.mcp.token}` }] },
  ];
  let step = "init";
  let turn: { start: number; cur: StepResult; onFirst?: () => void } | undefined;
  const finalCalls = new Map<string, ToolCallSnapshot>();

  const start = async (phase: Phase): Promise<Agent> => {
    const t0 = performance.now();
    const agent = await Agent.start(o.driver, {
      cwd: o.cwd,
      ...o.launch(phase),
      tap: (d, m) => {
        o.record?.(phase, { d, t: Math.round(performance.now() - t0), m });
        if (d === "in" && "method" in m) {
          if (m.method === "session/update") {
            const u = (m.params as acp.SessionNotification).update;
            count(res.acpKinds, u.sessionUpdate);
          } else if (m.method.startsWith("fs/")) res.clientFsCalls.push(m.method);
        }
      },
      onPermission: async (req) => {
        const decision: PermissionOptionKind = "allow_once";
        res.permissions.push({
          step,
          title: req.call.title,
          class: req.call.class,
          options: req.options.map((x) => x.kind),
          answered: decision,
        });
        log(`  permission: ${req.call.title} [${req.call.class}] → ${decision}`);
        return { select: decision };
      },
    });
    agent.onUpdate((u) => {
      count(res.hostKinds, u.kind);
      if (turn) {
        const now = performance.now() - turn.start;
        const cur = turn.cur;
        if (u.kind !== "state" && u.kind !== "usage") cur.firstUpdateMs ??= Math.round(now);
        if (u.kind === "thought") cur.firstThoughtMs ??= Math.round(now);
        if (u.kind === "message") {
          cur.firstTokenMs ??= Math.round(now);
          cur.text += u.text;
        }
        // Cancel mid-turn: as soon as the model streams anything (reasoning or text).
        if (u.kind === "thought" || u.kind === "message") turn.onFirst?.();
      }
      if (u.kind === "tool_call") finalCalls.set(u.call.id, u.call);
      if (u.kind === "plan") res.plans.push(u.entries);
      if (u.kind === "usage") res.usage.push(u);
    });
    return agent;
  };

  const runTurn = async (agent: Agent, name: string, prompt: string, cancel = false): Promise<StepResult> => {
    step = name;
    const cur: StepResult = { name, prompt, stopReason: "error", ms: 0, text: "" };
    const t = performance.now();
    turn = { start: t, cur };
    let cancelledAt: number | undefined;
    let fallback: NodeJS.Timeout | undefined;
    if (cancel) {
      const doCancel = (): void => {
        if (cancelledAt !== undefined) return;
        cancelledAt = performance.now();
        log(`  cancel at ${Math.round(cancelledAt - t)} ms`);
        void agent.cancel(res.sessionId);
      };
      turn.onFirst = doCancel;
      fallback = setTimeout(doCancel, o.cancelFallbackMs ?? 20000);
    }
    try {
      const r = await agent.prompt(res.sessionId, prompt);
      cur.stopReason = r.stopReason;
    } catch (err) {
      cur.error = err instanceof Error ? err.message : String(err);
    } finally {
      if (fallback) clearTimeout(fallback);
    }
    const end = performance.now();
    cur.ms = Math.round(end - t);
    if (cancelledAt !== undefined) cur.cancelToStopMs = Math.round(end - cancelledAt);
    turn = undefined;
    log(`  ${name}: ${cur.stopReason} in ${cur.ms} ms, first token ${cur.firstTokenMs ?? "-"} ms: ${cur.text.slice(0, 80).replace(/\n/g, " ")}`);
    res.steps.push(cur);
    return cur;
  };

  // Phase 1: one process, one session, four turns.
  let t = performance.now();
  const agent = await start("main");
  res.initializeMs = Math.round(performance.now() - t);
  const init = agent.initializeResponse;
  res.agentInfo = init.agentInfo;
  if (init.agentCapabilities) res.capabilities = init.agentCapabilities;
  log(`${o.driver.name}: ${init.agentInfo?.name ?? "?"} ${init.agentInfo?.version ?? ""} initialize ${res.initializeMs} ms`);
  try {
    t = performance.now();
    res.sessionId = await agent.newSession({ cwd: o.cwd, mcpServers });
    res.newSessionMs = Math.round(performance.now() - t);
    log(`  session/new ${res.newSessionMs} ms`);
    await runTurn(agent, "mcp", PROMPTS.mcp);
    await runTurn(agent, "edit", PROMPTS.edit);
    await runTurn(agent, "shell", PROMPTS.shell);
    await runTurn(agent, "cancel", PROMPTS.cancel, true);
  } finally {
    await agent.close();
  }

  // Phase 2: a new process restores the session and answers from the earlier context.
  step = "restore";
  const replayCount = (): number => Object.values(res.hostKinds).reduce((a, v) => a + v, 0);
  const again = await start("restore");
  try {
    t = performance.now();
    const n0 = replayCount();
    try {
      res.restore.mode = await again.restoreSession(res.sessionId, { cwd: o.cwd, mcpServers });
      res.restore.ms = Math.round(performance.now() - t);
      res.restore.replayedUpdates = replayCount() - n0;
      log(`  restore: ${res.restore.mode} in ${res.restore.ms} ms, ${res.restore.replayedUpdates} replayed updates`);
      await runTurn(again, "restore", PROMPTS.restore);
    } catch (err) {
      res.restore.mode = err instanceof RestoreUnsupportedError ? "unsupported" : "error";
      res.restore.error = err instanceof Error ? err.message : String(err);
      log(`  restore failed: ${res.restore.error}`);
    }
  } finally {
    await again.close();
  }

  for (const c of finalCalls.values()) {
    if (c.class === "mcp") res.mcpCalls.push(c);
    if (c.class === "edit") res.editCalls.push(c);
    if (c.class === "shell") res.shellCalls.push(c);
    res.diffs.push(...c.diffs);
  }
  return res;
}
