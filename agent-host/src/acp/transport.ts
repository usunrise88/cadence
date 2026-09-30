// Stdio transport: spawn an agent subprocess and speak newline-delimited JSON-RPC over its stdin/stdout.
// `tap` observes every message in both directions (transcripts, debugging) without changing them.

import { type ChildProcessByStdio, spawn } from "node:child_process";
import { Readable, Writable } from "node:stream";
import * as acp from "@agentclientprotocol/sdk";

export interface LaunchSpec {
  command: string;
  args: readonly string[];
  cwd: string;
  env: NodeJS.ProcessEnv;
  // The Unix user the process runs as (the host must be root to switch).
  uid?: number;
  gid?: number;
}

// "in" = agent → client, "out" = client → agent.
export type Direction = "in" | "out";
export type MessageTap = (direction: Direction, message: acp.AnyMessage) => void;

export interface AgentProcess {
  stream: acp.Stream;
  pid: number | undefined;
  exited: Promise<{ code: number | null; signal: NodeJS.Signals | null }>;
  stderr: () => string;
  // Signals the agent's whole process group: the adapter, the CLI it spawned, and anything the agent started.
  kill: (signal?: NodeJS.Signals) => void;
  // Resolves once no process of the group is left (after the agent itself exited), false on timeout.
  groupGone: (timeoutMs: number) => Promise<boolean>;
}

const STDERR_KEEP = 64 * 1024;

export function tapStream(stream: acp.Stream, tap: MessageTap): acp.Stream {
  const inbound = new TransformStream<acp.AnyMessage, acp.AnyMessage>({
    transform(msg, ctl) {
      tap("in", msg);
      ctl.enqueue(msg);
    },
  });
  const outbound = new TransformStream<acp.AnyMessage, acp.AnyMessage>({
    transform(msg, ctl) {
      tap("out", msg);
      ctl.enqueue(msg);
    },
  });
  void stream.readable.pipeTo(inbound.writable).catch(() => undefined);
  void outbound.readable.pipeTo(stream.writable).catch(() => undefined);
  return { readable: inbound.readable, writable: outbound.writable };
}

export function launch(spec: LaunchSpec, tap?: MessageTap): AgentProcess {
  const child: ChildProcessByStdio<Writable, Readable, Readable> = spawn(spec.command, [...spec.args], {
    cwd: spec.cwd,
    env: spec.env,
    stdio: ["pipe", "pipe", "pipe"],
    // Its own process group, so ending the session reaches every descendant: Claude's CLI outlives the adapter by
    // a moment and wrote its MCP logs into the session's HOME after the host had removed it.
    detached: true,
    ...(spec.uid !== undefined ? { uid: spec.uid } : {}),
    ...(spec.gid !== undefined ? { gid: spec.gid } : {}),
  });
  let stderr = "";
  child.stderr.setEncoding("utf8");
  child.stderr.on("data", (chunk: string) => {
    stderr = (stderr + chunk).slice(-STDERR_KEEP);
  });
  const exited = new Promise<{ code: number | null; signal: NodeJS.Signals | null }>((res) => {
    child.on("exit", (code, signal) => res({ code, signal }));
    child.on("error", () => res({ code: null, signal: null }));
  });
  // A write after the agent exits must not crash the host.
  child.stdin.on("error", () => undefined);
  const raw = acp.ndJsonStream(
    Writable.toWeb(child.stdin) as WritableStream<Uint8Array>,
    Readable.toWeb(child.stdout) as ReadableStream<Uint8Array>,
  );
  return {
    stream: tap ? tapStream(raw, tap) : raw,
    pid: child.pid,
    exited,
    stderr: () => stderr,
    kill: (signal = "SIGTERM") => {
      if (child.pid !== undefined && signalGroup(child.pid, signal)) return;
      if (child.exitCode === null && child.signalCode === null) child.kill(signal);
    },
    groupGone: async (timeoutMs) => {
      if (child.pid === undefined) return true;
      const until = Date.now() + timeoutMs;
      while (signalGroup(child.pid, 0)) {
        if (Date.now() >= until) return false;
        await new Promise((r) => setTimeout(r, 50));
      }
      return true;
    },
  };
}

// signalGroup sends a signal to the process group led by pid; false when no process of the group is left.
export function signalGroup(pid: number, signal: NodeJS.Signals | 0): boolean {
  try {
    process.kill(-pid, signal);
    return true;
  } catch {
    return false;
  }
}
