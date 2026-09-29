// Claude Code through the claude-agent-acp adapter (Claude Agent SDK underneath).
//
// Launch: `node <claude-agent-acp>/dist/index.js`, cwd = worktree. Authentication is the Claude Code login found
// through CLAUDE_CONFIG_DIR (R3: a per-session copy of the agent-credentials volume) or the user's default.
// Model: ANTHROPIC_MODEL, which the adapter ranks above settings. Tool names follow the SDK: MCP tools are
// `mcp__<server>__<tool>`, shell is `Bash`, edits are `Edit`/`Write`/`MultiEdit`/`NotebookEdit`.

import { fileURLToPath } from "node:url";
import type { LaunchSpec } from "../acp/transport.ts";
import { defaultReading, isRecord } from "./normalize.ts";
import type { Driver, LaunchOptions, RawToolCall, ToolReading } from "./types.ts";

const ADAPTER = fileURLToPath(import.meta.resolve("@agentclientprotocol/claude-agent-acp/dist/index.js"));

// The SDK tool name, which the adapter reports in `_meta.claudeCode.toolName`.
function toolName(call: RawToolCall): string | undefined {
  const cc = call.meta.claudeCode;
  return isRecord(cc) && typeof cc.toolName === "string" ? cc.toolName : undefined;
}

export function parseMcpName(name: string): { server: string; tool: string } | undefined {
  const m = /^mcp__(.+?)__(.+)$/.exec(name);
  return m?.[1] && m[2] ? { server: m[1], tool: m[2] } : undefined;
}

function shellResult(call: RawToolCall, command: string): ToolReading["shell"] {
  const shell: NonNullable<ToolReading["shell"]> = { command };
  const out = call.rawOutput;
  if (typeof out === "string") shell.output = out;
  else if (isRecord(out)) {
    const parts = [out.stdout, out.stderr].filter((p): p is string => typeof p === "string" && p !== "");
    if (parts.length) shell.output = parts.join("\n");
    if (typeof out.exitCode === "number") shell.exitCode = out.exitCode;
    else if (typeof out.returnCode === "number") shell.exitCode = out.returnCode;
  }
  return shell;
}

export const claudeDriver: Driver = {
  name: "claude",

  launch(opts: LaunchOptions): LaunchSpec {
    const env: NodeJS.ProcessEnv = { ...process.env, ...opts.env };
    if (opts.model) env.ANTHROPIC_MODEL = opts.model;
    const cmd = opts.command ?? { command: process.execPath, args: [ADAPTER] };
    return { command: cmd.command, args: cmd.args, cwd: opts.cwd, env };
  },

  // Claude Agent SDK options travel in `_meta.claudeCode.options` (the adapter merges them into its query options).
  sessionMeta(opts: LaunchOptions): Record<string, unknown> | undefined {
    if (!opts.thoughts) return undefined;
    return { claudeCode: { options: { thinking: { type: "adaptive", display: "summarized" } } } };
  },

  readTool(call: RawToolCall): ToolReading {
    const base = defaultReading(call);
    const name = toolName(call) ?? call.title;
    const mcp = parseMcpName(name);
    if (mcp) return { class: "mcp", mcp, title: `${mcp.server}.${mcp.tool}` };
    if (base.class === "shell" || name === "Bash") {
      const command = isRecord(call.rawInput) && typeof call.rawInput.command === "string" ? call.rawInput.command : "";
      return { class: "shell", shell: shellResult(call, command) };
    }
    return base;
  },
};
