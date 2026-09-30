import assert from "node:assert/strict";
import { describe, test } from "node:test";
import type * as acp from "@agentclientprotocol/sdk";
import { pickOption } from "./agent.ts";
import { claudeDriver, mcpToolName, parseMcpName } from "./claude.ts";
import { UpdateNormalizer } from "./normalize.ts";
import { makeOpencodeDriver, opencodeDriver, todosToPlan } from "./opencode.ts";
import type { HostUpdate } from "./types.ts";

const note = (update: acp.SessionUpdate): acp.SessionNotification => ({ sessionId: "s", update });

function last<T>(xs: T[]): T {
  const x = xs.at(-1);
  assert.ok(x !== undefined);
  return x;
}

function toolOf(u: HostUpdate[]): Extract<HostUpdate, { kind: "tool_call" }>["call"] {
  const t = u.find((x) => x.kind === "tool_call");
  assert.ok(t && t.kind === "tool_call");
  return t.call;
}

describe("UpdateNormalizer", () => {
  test("folds tool_call and its updates into one snapshot with diffs", () => {
    const n = new UpdateNormalizer(claudeDriver);
    n.apply(note({ sessionUpdate: "tool_call", toolCallId: "t", title: "Edit", kind: "edit", status: "pending" }));
    const snap = toolOf(
      n.apply(
        note({
          sessionUpdate: "tool_call_update",
          toolCallId: "t",
          status: "completed",
          content: [{ type: "diff", path: "/w/a.txt", oldText: "a", newText: "b" }],
        }),
      ),
    );
    assert.equal(snap.title, "Edit");
    assert.equal(snap.class, "edit");
    assert.equal(snap.status, "completed");
    assert.deepEqual(snap.diffs, [{ path: "/w/a.txt", oldText: "a", newText: "b" }]);
  });

  test("maps chunks, plans and usage; passes the rest through as info", () => {
    const n = new UpdateNormalizer(opencodeDriver);
    const kinds = [
      n.apply(note({ sessionUpdate: "agent_message_chunk", content: { type: "text", text: "x" } })),
      n.apply(note({ sessionUpdate: "agent_thought_chunk", content: { type: "text", text: "y" } })),
      n.apply(note({ sessionUpdate: "plan", entries: [] })),
      n.apply(note({ sessionUpdate: "usage_update", used: 1, size: 2, cost: { amount: 0, currency: "USD" } })),
      n.apply(note({ sessionUpdate: "available_commands_update", availableCommands: [] })),
    ].map((u) => last(u).kind);
    assert.deepEqual(kinds, ["message", "thought", "plan", "usage", "info"]);
  });
});

describe("claude driver", () => {
  const read = (title: string, kind: acp.ToolKind, rawInput?: unknown, rawOutput?: unknown, toolName?: string) =>
    claudeDriver.readTool({
      toolCallId: "t",
      title,
      kind,
      status: "completed",
      content: [],
      locations: [],
      rawInput,
      rawOutput,
      meta: toolName ? { claudeCode: { toolName } } : {},
    });

  test("MCP tools are mcp__<server>__<tool>, from _meta.claudeCode.toolName or the title", () => {
    assert.deepEqual(parseMcpName("mcp__cadence__runs_new"), { server: "cadence", tool: "runs_new" });
    assert.equal(parseMcpName("Bash"), undefined);
    assert.deepEqual(read("", "other", {}, undefined, "mcp__cadence__echo"), {
      class: "mcp",
      mcp: { server: "cadence", tool: "echo" },
      title: "cadence.echo",
    });
    assert.equal(read("mcp__cadence__echo", "other").class, "mcp");
  });

  test("Bash is shell with command and output", () => {
    assert.deepEqual(read("echo hi", "execute", { command: "echo hi" }, "hi", "Bash"), {
      class: "shell",
      shell: { command: "echo hi", output: "hi" },
    });
  });

  test("thoughts ask the adapter for summarized thinking", () => {
    assert.equal(claudeDriver.sessionMeta?.({ cwd: "/w" }), undefined);
    assert.deepEqual(claudeDriver.sessionMeta?.({ cwd: "/w", thoughts: true }), {
      claudeCode: { options: { thinking: { type: "adaptive", display: "summarized" } } },
    });
  });

  test("the preset's allowed Cadence tools are pre-allowed in Claude's own naming (no permission round trip)", () => {
    assert.equal(mcpToolName("cadence", "mixes.get"), "mcp__cadence__mixes_get");
    assert.deepEqual(claudeDriver.sessionMeta?.({ cwd: "/w", thoughts: true, preAllowed: { server: "cadence", tools: ["mixes.get", "goldenSets.freeze"] } }), {
      claudeCode: {
        options: {
          thinking: { type: "adaptive", display: "summarized" },
          allowedTools: ["mcp__cadence__mixes_get", "mcp__cadence__goldenSets_freeze"],
        },
      },
    });
    assert.equal(claudeDriver.sessionMeta?.({ cwd: "/w", preAllowed: { server: "cadence", tools: [] } }), undefined);
  });

  test("model goes to ANTHROPIC_MODEL", () => {
    assert.equal(claudeDriver.launch({ cwd: "/w", model: "haiku" }).env.ANTHROPIC_MODEL, "haiku");
  });
});

describe("opencode driver", () => {
  const read = (title: string, kind: acp.ToolKind, rawInput?: unknown, rawOutput?: unknown) =>
    makeOpencodeDriver(["cadence", "cadence_x"]).readTool({
      toolCallId: "t",
      title,
      kind,
      status: "completed",
      content: [],
      locations: [],
      rawInput,
      rawOutput,
      meta: {},
    });

  test("MCP tools are <server>_<tool> for the configured servers, longest server first", () => {
    assert.deepEqual(read("cadence_echo", "other").mcp, { server: "cadence", tool: "echo" });
    assert.deepEqual(read("cadence_x_runs_new", "other").mcp, { server: "cadence_x", tool: "runs_new" });
    assert.equal(read("other_echo", "other").class, "other");
    assert.equal(read("bash", "execute").class, "shell");
  });

  test("bash reads command, output and exit code", () => {
    assert.deepEqual(read("bash", "execute", { command: "ls" }, { output: "a", metadata: { exit: 2 } }).shell, {
      command: "ls",
      output: "a",
      exitCode: 2,
    });
  });

  test("todowrite becomes a plan, emitted once per change", () => {
    assert.deepEqual(todosToPlan({ todos: [{ content: "a", status: "cancelled", priority: "high" }, { content: "b" }] }), [
      { content: "a", status: "completed", priority: "high" },
      { content: "b", status: "pending", priority: "medium" },
    ]);
    const n = new UpdateNormalizer(opencodeDriver);
    const todos = { todos: [{ content: "a", status: "pending", priority: "high" }] };
    const u1 = n.apply(note({ sessionUpdate: "tool_call", toolCallId: "t", title: "todowrite", kind: "other", rawInput: todos }));
    const u2 = n.apply(note({ sessionUpdate: "tool_call_update", toolCallId: "t", status: "completed" }));
    assert.deepEqual(u1.map((u) => u.kind), ["tool_call", "plan"]);
    assert.deepEqual(u2.map((u) => u.kind), ["tool_call"]);
  });

  test("a session model is merged into OPENCODE_CONFIG_CONTENT", () => {
    const prev = process.env.OPENCODE_CONFIG_CONTENT;
    process.env.OPENCODE_CONFIG_CONTENT = '{"permission":{"edit":"ask"}}';
    try {
      const env = opencodeDriver.launch({ cwd: "/w", model: "opencode/big-pickle" }).env;
      assert.deepEqual(JSON.parse(env.OPENCODE_CONFIG_CONTENT ?? ""), { permission: { edit: "ask" }, model: "opencode/big-pickle" });
    } finally {
      if (prev === undefined) delete process.env.OPENCODE_CONFIG_CONTENT;
      else process.env.OPENCODE_CONFIG_CONTENT = prev;
    }
  });
});

describe("pickOption", () => {
  const options: acp.PermissionOption[] = [
    { optionId: "1", name: "Allow", kind: "allow_once" },
    { optionId: "2", name: "Reject", kind: "reject_once" },
  ];
  test("selects by kind, falls back from always to once, and by id", () => {
    assert.equal(pickOption(options, { select: "allow_once" })?.optionId, "1");
    assert.equal(pickOption(options, { select: "allow_always" })?.optionId, "1");
    assert.equal(pickOption(options, { select: "reject_always" })?.optionId, "2");
    assert.equal(pickOption(options, { optionId: "2" })?.optionId, "2");
    assert.equal(pickOption(options, "cancelled"), undefined);
  });
});
