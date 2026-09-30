// A scripted ACP agent for the session manager tests: it speaks the agent side of ACP over stdio (like the replayed
// transcripts of the contract tests, but it reacts to what the host sends) and acts on the last line of each prompt:
//
//   edit <file> <text>   writes the file in its cwd, reports an edit tool call with the diff, answers "edited"
//   secret               writes leak.txt holding a session token (the credential scan must refuse it)
//   loop                 calls the same Cadence tool with the same arguments until cancelled (runaway)
//   hang                 sends nothing until cancelled (stuck turn)
//   ask <command>        asks permission to run a shell command, answers with the option it got
//   usage <n>            answers with n input tokens used
//   think                streams 50 thought chunks, then answers
//   anything else        answers "ok: <the whole prompt>" in three chunks
//
//   node test/script-agent.ts

import { Readable, Writable } from "node:stream";
import { writeFileSync } from "node:fs";
import { join } from "node:path";
import * as acp from "@agentclientprotocol/sdk";

type Ctx = { notify: (method: string, params: unknown) => Promise<void>; request: (method: string, params: unknown) => Promise<unknown> };

const sessions = new Map<string, { cwd: string; abort?: AbortController }>();
let n = 0;

const tick = (): Promise<void> => new Promise((r) => setImmediate(r));

function cancelled(signal: AbortSignal): Promise<void> {
  return new Promise((r) => (signal.aborted ? r() : signal.addEventListener("abort", () => r(), { once: true })));
}

async function say(cx: Ctx, sessionId: string, text: string, kind: "agent_message_chunk" | "agent_thought_chunk" = "agent_message_chunk"): Promise<void> {
  await cx.notify("session/update", { sessionId, update: { sessionUpdate: kind, content: { type: "text", text } } });
}

async function turn(cx: Ctx, sessionId: string, prompt: string, signal: AbortSignal): Promise<acp.PromptResponse> {
  const s = sessions.get(sessionId);
  if (!s) throw new Error("unknown session");
  const line = prompt.trim().split("\n").at(-1) ?? "";
  const [cmd, ...rest] = line.split(" ");
  const usage = { inputTokens: 100, outputTokens: 20, totalTokens: 120 };
  switch (cmd) {
    case "edit": {
      const [file = "x.txt", ...words] = rest;
      const text = `${words.join(" ")}\n`;
      writeFileSync(join(s.cwd, file), text);
      const toolCallId = `edit-${++n}`;
      await cx.notify("session/update", { sessionId, update: { sessionUpdate: "tool_call", toolCallId, title: `Edit ${file}`, kind: "edit", status: "in_progress", rawInput: { file_path: join(s.cwd, file) } } });
      await cx.notify("session/update", {
        sessionId,
        update: { sessionUpdate: "tool_call_update", toolCallId, status: "completed", content: [{ type: "diff", path: join(s.cwd, file), oldText: null, newText: text }] },
      });
      await say(cx, sessionId, "edited");
      return { stopReason: "end_turn", usage };
    }
    case "secret":
      writeFileSync(join(s.cwd, "leak.txt"), `token=cst_${"a".repeat(52)}\n`);
      await say(cx, sessionId, "wrote a secret");
      return { stopReason: "end_turn", usage };
    case "loop":
      for (let i = 0; i < 10 && !signal.aborted; i++) {
        const toolCallId = `loop-${++n}`;
        await cx.notify("session/update", {
          sessionId,
          update: {
            sessionUpdate: "tool_call", toolCallId, title: "mcp__cadence__mixes.get", kind: "other", status: "pending",
            rawInput: { id: "mix_1" }, _meta: { claudeCode: { toolName: "mcp__cadence__mixes.get" } },
          },
        });
        await tick();
      }
      await cancelled(signal);
      return { stopReason: "cancelled" };
    case "hang":
      await cancelled(signal);
      return { stopReason: "cancelled" };
    case "ask": {
      const command = rest.join(" ");
      const toolCallId = `ask-${++n}`;
      const toolCall = { toolCallId, title: command, kind: "execute", status: "pending", rawInput: { command } };
      await cx.notify("session/update", { sessionId, update: { sessionUpdate: "tool_call", ...toolCall } });
      const res = (await cx.request("session/request_permission", {
        sessionId,
        toolCall,
        options: [
          { optionId: "yes", name: "Allow", kind: "allow_once" },
          { optionId: "always", name: "Always", kind: "allow_always" },
          { optionId: "no", name: "Reject", kind: "reject_once" },
        ],
      })) as acp.RequestPermissionResponse;
      if (signal.aborted) return { stopReason: "cancelled" };
      const got = res.outcome.outcome === "selected" ? res.outcome.optionId : "cancelled";
      await say(cx, sessionId, `permission:${got}`);
      return { stopReason: "end_turn", usage };
    }
    case "usage":
      await say(cx, sessionId, "counted");
      return { stopReason: "end_turn", usage: { inputTokens: Number(rest[0]), outputTokens: 1, totalTokens: Number(rest[0]) + 1 } };
    case "think":
      for (let i = 0; i < 50; i++) await say(cx, sessionId, `t${i} `, "agent_thought_chunk");
      await say(cx, sessionId, "thought");
      return { stopReason: "end_turn", usage };
    default:
      for (const part of ["ok: ", prompt.slice(0, prompt.length / 2), prompt.slice(prompt.length / 2)]) await say(cx, sessionId, part);
      return { stopReason: "end_turn", usage };
  }
}

const stream = acp.ndJsonStream(Writable.toWeb(process.stdout) as WritableStream<Uint8Array>, Readable.toWeb(process.stdin) as ReadableStream<Uint8Array>);
acp
  .agent({ name: "script-agent" })
  .onRequest("initialize", () => ({
    protocolVersion: acp.PROTOCOL_VERSION,
    agentCapabilities: { loadSession: true, sessionCapabilities: { resume: {} }, mcpCapabilities: { http: true } },
  }))
  .onRequest("session/new", (ctx) => {
    const sessionId = `script-${process.pid}-${++n}`;
    sessions.set(sessionId, { cwd: ctx.params.cwd });
    return { sessionId };
  })
  .onRequest("session/resume", (ctx) => {
    sessions.set(ctx.params.sessionId, { cwd: ctx.params.cwd });
    return {};
  })
  .onRequest("session/prompt", async (ctx) => {
    const s = sessions.get(ctx.params.sessionId);
    const abort = new AbortController();
    if (s) s.abort = abort;
    const text = ctx.params.prompt.map((b) => (b.type === "text" ? b.text : "")).join("");
    return turn(ctx.client as unknown as Ctx, ctx.params.sessionId, text, abort.signal);
  })
  .onNotification("session/cancel", (ctx) => {
    sessions.get(ctx.params.sessionId)?.abort?.abort();
  })
  .connect(stream);
