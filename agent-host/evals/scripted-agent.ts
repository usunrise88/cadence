// The offline agent of the evals: an ACP agent over stdio that recognises an eval's prompt (evals.ts) and runs its
// reference policy against the real Cadence MCP server the host named in session/new — asking permission before
// every call as Claude does, so the preset answers it, and reporting each call in the driver's own shape (Claude:
// `mcp__cadence__mixes_edit` in _meta.claudeCode.toolName and its tool-use id in the MCP _meta; opencode:
// `cadence_mixes_edit` as the title, no id), so the host's drivers, transcript and the server's attribution run as
// they do for the real agents. No model, no network beyond the control plane.
//
//   node --import tsx evals/scripted-agent.ts <claude|opencode>

import { Readable, Writable } from "node:stream";
import * as acp from "@agentclientprotocol/sdk";
import { evalForPrompt } from "./evals.ts";
import { McpClient } from "./mcp.ts";
import type { ScriptedTools } from "./types.ts";

type Ctx = { notify: (method: string, params: unknown) => Promise<void>; request: (method: string, params: unknown) => Promise<unknown> };
type Session = { cwd: string; mcp?: { url: string; headers: Record<string, string>; project: string }; client?: McpClient; tools?: Set<string> };

const driver = process.argv[2] === "opencode" ? "opencode" : "claude";
const sessions = new Map<string, Session>();
let n = 0;

function toolName(operation: string): string {
  const sanitized = operation.replace(".", "_");
  return driver === "claude" ? `mcp__cadence__${sanitized}` : `cadence_${sanitized}`;
}

function tools(cx: Ctx, sessionId: string, s: Session): ScriptedTools {
  const mcp = s.mcp;
  if (!mcp) throw new Error("session/new named no cadence MCP server");
  const update = (u: Record<string, unknown>) => cx.notify("session/update", { sessionId, update: u });
  return {
    project: mcp.project,
    async has(operation) {
      s.client ??= await connect(mcp);
      s.tools ??= await s.client.tools();
      return s.tools.has(operation);
    },
    async say(text) {
      await update({ sessionUpdate: "agent_message_chunk", content: { type: "text", text } });
    },
    async call(operation, args) {
      const name = toolName(operation);
      const toolCallId = driver === "claude" ? `toolu_eval_${process.pid}_${++n}` : `call_eval_${process.pid}_${++n}`;
      const shape = driver === "claude" ? { title: name, _meta: { claudeCode: { toolName: name } } } : { title: name };
      // Claude's order: a pending call with {} as its input, the arguments streamed in, then the permission request.
      await update({ sessionUpdate: "tool_call", toolCallId, kind: "other", status: "pending", rawInput: {}, ...shape });
      await update({ sessionUpdate: "tool_call_update", toolCallId, status: "pending", rawInput: args, ...shape });
      const toolCall = { toolCallId, kind: "other", status: "pending", rawInput: args, ...shape };
      const res = (await cx.request("session/request_permission", {
        sessionId,
        toolCall,
        options: [
          { optionId: "allow", name: "Allow", kind: "allow_once" },
          { optionId: "always", name: "Always allow", kind: "allow_always" },
          { optionId: "reject", name: "Reject", kind: "reject_once" },
        ],
      })) as acp.RequestPermissionResponse;
      const allowed = res.outcome.outcome === "selected" && res.outcome.optionId !== "reject";
      if (!allowed) {
        await update({ sessionUpdate: "tool_call_update", toolCallId, status: "failed", content: [text("The user rejected this tool call.")] });
        return { rejected: true };
      }
      await update({ sessionUpdate: "tool_call_update", toolCallId, status: "in_progress" });
      s.client ??= await connect(mcp);
      const { result, isError } = await s.client.call(operation, args, driver === "claude" ? toolCallId : undefined);
      await update({
        sessionUpdate: "tool_call_update",
        toolCallId,
        status: isError ? "failed" : "completed",
        rawOutput: result,
        content: [text(JSON.stringify(result))],
      });
      return { rejected: false, result };
    },
  };
}

function text(t: string) {
  return { type: "content", content: { type: "text", text: t } };
}

async function connect(mcp: NonNullable<Session["mcp"]>): Promise<McpClient> {
  const c = new McpClient(mcp.url, mcp.headers);
  await c.connect();
  return c;
}

async function turn(cx: Ctx, sessionId: string, prompt: string): Promise<acp.PromptResponse> {
  const s = sessions.get(sessionId);
  if (!s) throw new Error("unknown session");
  const say = (t: string) => cx.notify("session/update", { sessionId, update: { sessionUpdate: "agent_message_chunk", content: { type: "text", text: t } } });
  const e = evalForPrompt(prompt);
  if (!e) {
    await say("The scripted agent knows no eval with this prompt.");
  } else {
    try {
      await e.offline(tools(cx, sessionId, s));
    } catch (err) {
      await say(`The scripted agent failed: ${err instanceof Error ? err.message : String(err)}`);
    }
  }
  return { stopReason: "end_turn", usage: { inputTokens: 1200, outputTokens: 150, totalTokens: 1350 } };
}

function readMcp(servers: acp.McpServer[]): Session["mcp"] {
  for (const m of servers) {
    if (!("url" in m) || m.name !== "cadence") continue;
    const headers = Object.fromEntries((m.headers ?? []).map((h) => [h.name, h.value]));
    return { url: m.url, headers, project: headers["Cadence-Project"] ?? "" };
  }
  return undefined;
}

const stream = acp.ndJsonStream(Writable.toWeb(process.stdout) as WritableStream<Uint8Array>, Readable.toWeb(process.stdin) as ReadableStream<Uint8Array>);
acp
  .agent({ name: "cadence-evals-scripted-agent" })
  .onRequest("initialize", () => ({
    protocolVersion: acp.PROTOCOL_VERSION,
    agentCapabilities: { loadSession: false, mcpCapabilities: { http: true } },
  }))
  .onRequest("session/new", (ctx) => {
    const sessionId = `scripted-${process.pid}-${++n}`;
    const mcp = readMcp(ctx.params.mcpServers);
    sessions.set(sessionId, mcp ? { cwd: ctx.params.cwd, mcp } : { cwd: ctx.params.cwd });
    return { sessionId };
  })
  .onRequest("session/prompt", async (ctx) => {
    const t = ctx.params.prompt.map((b) => (b.type === "text" ? b.text : "")).join("");
    return turn(ctx.client as unknown as Ctx, ctx.params.sessionId, t);
  })
  .onNotification("session/cancel", () => undefined)
  .connect(stream);
