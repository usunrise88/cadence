// A fake ACP agent process that replays a recorded transcript over stdio (newline-delimited JSON-RPC).
//
//   node --import tsx test/replay-agent.ts <fixture.jsonl>
//
// Recorded lines are {d, t, m}: d = "in" (agent → client, which this process sends) or "out" (client → agent, which
// it waits for). An awaited message matches by method (requests, notifications) or by id (responses to the agent's
// own requests); client messages that arrive early wait in a queue, so a client may cancel sooner than recorded.
// Request ids are mapped from recorded to live, and the recorded workspace path to the live cwd from session/*.

import { readFileSync } from "node:fs";
import { createInterface } from "node:readline";

type Msg = { jsonrpc: "2.0"; id?: string | number | null; method?: string; params?: unknown; result?: unknown; error?: unknown };
type Line = { d: "in" | "out"; t: number; m: Msg };

const WORKSPACE = "/workspace";
const file = process.argv[2];
if (!file) throw new Error("usage: replay-agent <fixture.jsonl>");
const script: Line[] = readFileSync(file, "utf8")
  .split("\n")
  .filter((l) => l.trim() !== "")
  .map((l) => JSON.parse(l) as Line);

const queue: Msg[] = [];
let wake: (() => void) | undefined;
let stdinClosed = false;
const rl = createInterface({ input: process.stdin });
rl.on("line", (l) => {
  if (l.trim() === "") return;
  queue.push(JSON.parse(l) as Msg);
  wake?.();
});
rl.on("close", () => {
  stdinClosed = true;
  wake?.();
});

const idMap = new Map<string, string | number | null>();
let cwd: string | undefined;

function matches(expected: Msg, got: Msg): boolean {
  if (expected.method !== undefined) return got.method === expected.method;
  return got.method === undefined && String(got.id) === String(expected.id);
}

async function take(expected: Msg): Promise<Msg | undefined> {
  for (;;) {
    const i = queue.findIndex((g) => matches(expected, g));
    if (i >= 0) return queue.splice(i, 1)[0];
    if (stdinClosed) return undefined;
    await new Promise<void>((r) => (wake = r));
  }
}

function relocate(v: unknown): unknown {
  if (typeof v === "string") return cwd && v.startsWith(WORKSPACE) ? cwd + v.slice(WORKSPACE.length) : v;
  if (Array.isArray(v)) return v.map(relocate);
  if (typeof v === "object" && v !== null) return Object.fromEntries(Object.entries(v).map(([k, x]) => [k, relocate(x)]));
  return v;
}

function send(m: Msg): void {
  const out = relocate(m) as Msg;
  if (out.method === undefined && out.id !== undefined) {
    const live = idMap.get(String(out.id));
    if (live !== undefined) out.id = live;
  }
  process.stdout.write(`${JSON.stringify(out)}\n`);
}

for (const line of script) {
  if (line.d === "in") {
    send(line.m);
    await new Promise<void>((r) => setImmediate(r));
    continue;
  }
  const got = await take(line.m);
  if (!got) break;
  if (got.method !== undefined && got.id !== undefined) idMap.set(String(line.m.id), got.id);
  if (got.method?.startsWith("session/") && typeof (got.params as { cwd?: unknown })?.cwd === "string") {
    cwd = (got.params as { cwd: string }).cwd;
  }
}
// Stay alive until the client closes stdin (or kills us), as a real agent would.
await new Promise<void>((r) => (stdinClosed ? r() : rl.on("close", r)));
