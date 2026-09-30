// Claude Code through the claude-agent-acp adapter (Claude Agent SDK underneath).
//
// Launch: `node <claude-agent-acp>/dist/index.js`, cwd = worktree. Authentication is the Claude Code login found
// through CLAUDE_CONFIG_DIR (R3: a per-session copy of the agent-credentials volume) or the user's default.
// Model: ANTHROPIC_MODEL, which the adapter ranks above settings. Tool names follow the SDK: MCP tools are
// `mcp__<server>__<tool>`, shell is `Bash`, edits are `Edit`/`Write`/`MultiEdit`/`NotebookEdit`.

import { existsSync } from "node:fs";
import { mkdir, mkdtemp, rm, writeFile } from "node:fs/promises";
import { createRequire } from "node:module";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import type { LaunchSpec } from "../acp/transport.ts";
import { ask, copyIfPresent, readSecretFile, runInteractive } from "./home.ts";

// The Claude Code CLI the Agent SDK bundles for this platform (CLAUDE_BIN overrides; else `claude` on PATH).
function claudeBinary(): string {
  if (process.env.CLAUDE_BIN) return process.env.CLAUDE_BIN;
  const require = createRequire(import.meta.url);
  const pkg = `@anthropic-ai/claude-agent-sdk-${process.platform}-${process.arch}`;
  for (const name of [pkg, `${pkg}-musl`]) {
    try {
      const bin = join(dirname(require.resolve(`${name}/package.json`)), "claude");
      if (existsSync(bin)) return bin;
    } catch {
      // not installed for this platform
    }
  }
  return "claude";
}
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
    const env: NodeJS.ProcessEnv = { ...(opts.baseEnv ?? process.env), ...opts.env };
    if (opts.model) env.ANTHROPIC_MODEL = opts.model;
    const cmd = opts.command ?? { command: process.execPath, args: [ADAPTER] };
    return { command: cmd.command, args: cmd.args, cwd: opts.cwd, env };
  },

  // Claude Agent SDK options travel in `_meta.claudeCode.options` (the adapter merges them into its query options).
  sessionMeta(opts: LaunchOptions): Record<string, unknown> | undefined {
    if (!opts.thoughts) return undefined;
    return { claudeCode: { options: { thinking: { type: "adaptive", display: "summarized" } } } };
  },

  // The agent-credentials volume holds `claude/`: either `oauth-token` (a long-lived token from `claude setup-token`,
  // preferred: no refresh-token rotation between session copies) or a whole Claude config dir from an interactive
  // login. The session gets its own CLAUDE_CONFIG_DIR, so the user's ~/.claude (skills, plugins, e-mail) never
  // reaches it (A1 surprise 3).
  async prepareHome(home: string, credentials: string | undefined): Promise<NodeJS.ProcessEnv> {
    if (!credentials) return {};
    const dir = join(home, ".claude");
    await mkdir(dir, { recursive: true, mode: 0o700 });
    const src = join(credentials, "claude");
    await copyIfPresent(src, dir, (p) => !p.endsWith("/oauth-token"));
    // No telemetry, error reports or auto-update: the egress proxy allows the model API only (R4).
    const env: NodeJS.ProcessEnv = { CLAUDE_CONFIG_DIR: dir, CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC: "1" };
    const token = await readSecretFile(join(src, "oauth-token"));
    if (token) env.CLAUDE_CODE_OAUTH_TOKEN = token;
    return env;
  },

  // `claude setup-token` (the CLI bundled with the Agent SDK) signs in with the owner's Claude account in a browser
  // and prints a long-lived token for headless use on the subscription (R6); it is stored as claude/oauth-token.
  async login(credentials: string): Promise<void> {
    const dir = join(credentials, "claude");
    await mkdir(dir, { recursive: true, mode: 0o700 });
    const scratch = await mkdtemp(join(tmpdir(), "claude-login-"));
    try {
      console.log("Running `claude setup-token`: open the link it prints, sign in with the Claude account, paste the code back.");
      runInteractive(claudeBinary(), ["setup-token"], { ...process.env, HOME: scratch, CLAUDE_CONFIG_DIR: scratch });
    } finally {
      await rm(scratch, { recursive: true, force: true });
    }
    const token = await ask("Paste the token it printed (sk-ant-oat…): ");
    if (!token.startsWith("sk-ant-")) throw new Error("that is not a Claude token (sk-ant-…); nothing was saved");
    await writeFile(join(dir, "oauth-token"), `${token}\n`, { mode: 0o600 });
    console.log(`Saved ${join(dir, "oauth-token")}; Claude Code sessions use it from their next start.`);
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
