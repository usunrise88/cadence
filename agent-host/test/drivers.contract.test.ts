// Driver contract tests: each driver runs the A1 lifecycle against a fake agent process that replays the ACP
// traffic recorded from the real agent (test/fixtures/<driver>-{main,restore}.jsonl). No agent, model or network.
// The live run that produced the fixtures is `npm run spike:a1 -- --record`; its measurements are in
// <driver>-summary.json, which these tests hold the replay to.

import assert from "node:assert/strict";
import { mkdtemp, readFile, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { describe, test } from "node:test";
import { fileURLToPath } from "node:url";
import type * as acp from "@agentclientprotocol/sdk";
import { drivers } from "../src/drivers/index.ts";
import type { DriverName } from "../src/drivers/types.ts";
import { type LifecycleResult, type Phase, type RecordedMessage, runLifecycle } from "../src/spikes/lifecycle.ts";

const REPLAYER = fileURLToPath(new URL("./replay-agent.ts", import.meta.url));
const fixture = (name: string): string => fileURLToPath(new URL(`./fixtures/${name}`, import.meta.url));

type Summary = LifecycleResult & { notesAfter: string };

async function replay(name: DriverName): Promise<{ res: LifecycleResult; out: RecordedMessage[]; notes: string; cwd: string }> {
  const root = await mkdtemp(join(tmpdir(), `a1-replay-${name}-`));
  await writeFile(join(root, "notes.txt"), "alpha\n");
  const out: RecordedMessage[] = [];
  try {
    const res = await runLifecycle({
      driver: drivers[name],
      cwd: root,
      mcp: { url: "http://127.0.0.1:0/mcp", token: "cst_test" },
      launch: (phase: Phase) => ({
        command: { command: process.execPath, args: [REPLAYER, fixture(`${name}-${phase}.jsonl`)] },
      }),
      record: (_phase, m) => {
        if (m.d === "out") out.push(m);
      },
    });
    return { res, out, notes: await readFile(join(root, "notes.txt"), "utf8"), cwd: root };
  } finally {
    await rm(root, { recursive: true, force: true });
  }
}

for (const name of ["claude", "opencode"] as const) {
  describe(`${name} driver on recorded transcripts`, async () => {
    const summary = JSON.parse(await readFile(fixture(`${name}-summary.json`), "utf8")) as Summary;
    const { res, out, notes } = await replay(name);

    test("session/new carries the cwd and the MCP server with its Authorization header (R2)", () => {
      const req = out.find((m) => "method" in m.m && m.m.method === "session/new");
      assert.ok(req);
      const params = (req.m as { params: acp.NewSessionRequest }).params;
      assert.deepEqual(params.mcpServers, [
        { type: "http", name: "cadence", url: "http://127.0.0.1:0/mcp", headers: [{ name: "Authorization", value: "Bearer cst_test" }] },
      ]);
    });

    test("every turn ends as recorded; the cancelled turn stops with `cancelled`", () => {
      assert.deepEqual(
        res.steps.map((s) => [s.name, s.stopReason]),
        summary.steps.map((s) => [s.name, s.stopReason]),
      );
      assert.equal(res.steps.find((s) => s.name === "cancel")?.stopReason, "cancelled");
      const outCancel = out.filter((m) => "method" in m.m && m.m.method === "session/cancel");
      assert.equal(outCancel.length, 1);
    });

    test("the same ACP update kinds arrive and normalise to the same host kinds", () => {
      assert.deepEqual(res.acpKinds, summary.acpKinds);
      assert.deepEqual(res.hostKinds, summary.hostKinds);
      for (const k of ["message", "thought", "plan", "tool_call", "permission", "usage", "state"]) {
        assert.ok((res.hostKinds[k] ?? 0) > 0, `no ${k} update`);
      }
    });

    test("the MCP tool call is read as MCP, server cadence, tool echo", () => {
      assert.equal(res.mcpCalls.length, 1);
      const call = res.mcpCalls[0];
      assert.deepEqual(call?.mcp, { server: "cadence", tool: "echo" });
      assert.equal(call?.title, "cadence.echo");
      assert.equal(call?.status, "completed");
      assert.match(res.steps[0]?.text ?? "", /^echo:ping:[0-9a-f]{6}$/);
    });

    test("the file edit arrives as a structured diff", () => {
      const diff = res.diffs.find((d) => d.path.endsWith("/notes.txt"));
      assert.ok(diff, "no diff for notes.txt");
      assert.match(diff.oldText ?? "", /alpha/);
      assert.match(diff.newText, /beta/);
      assert.ok(res.editCalls.some((c) => c.status === "completed" && c.diffs.length > 0));
    });

    test("the shell call names its command", () => {
      assert.ok(res.shellCalls.some((c) => c.shell?.command === "echo a1-shell-ok > shell.txt"));
    });

    test("permission requests for MCP, edit and shell are answered by the host", () => {
      assert.deepEqual(
        res.permissions.map((p) => [p.step, p.class, p.answered]),
        [
          ["mcp", "mcp", "allow_once"],
          ["edit", "edit", "allow_once"],
          ["shell", "shell", "allow_once"],
        ],
      );
      for (const p of res.permissions) assert.ok(p.options.includes("reject_once"));
    });

    test("a new process restores the session and keeps its context", () => {
      assert.equal(res.restore.mode, summary.restore.mode);
      assert.equal(res.steps.at(-1)?.text, res.steps[0]?.text);
    });

    test("turn usage carries token counts", () => {
      const turns = res.usage.flatMap((u) => (u.kind === "usage" && u.turn ? [u.turn] : []));
      assert.ok(turns.some((t) => t.totalTokens > 0 && t.outputTokens > 0));
    });

    test("client fs writes stay in the workspace and land there", () => {
      assert.deepEqual(res.clientFsCalls, summary.clientFsCalls);
      if (res.clientFsCalls.includes("fs/write_text_file")) assert.equal(notes, "beta\n");
      else assert.equal(notes, "alpha\n"); // the agent wrote the file itself; the replay has no agent
    });
  });
}
