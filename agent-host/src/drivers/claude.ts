// Claude Code through the claude-agent-acp adapter (Claude Agent SDK underneath).
//
// Launch: `node <claude-agent-acp>/dist/index.js`, cwd = worktree. Authentication is the Claude Code login found
// through CLAUDE_CONFIG_DIR (R3: a per-session copy of the agent-credentials volume) or the user's default.
// Model: ANTHROPIC_MODEL, which the adapter ranks above settings. Tool names follow the SDK: MCP tools are
// `mcp__<server>__<tool>`, shell is `Bash`, edits are `Edit`/`Write`/`MultiEdit`/`NotebookEdit`.

import { existsSync } from "node:fs";
import { mkdir, mkdtemp, rm } from "node:fs/promises";
import { createRequire } from "node:module";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import type { LaunchSpec } from "../acp/transport.ts";
import { ask, copyIfPresent, readSecretFile, runInteractive, writeSecretFile } from "./home.ts";

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
import type {
  CredentialTask,
  Driver,
  LaunchOptions,
  RawToolCall,
  RunResult,
  ToolReading,
  VerifyContext,
  VerifyResult,
} from "./types.ts";

const VERIFY_MODEL = "haiku";
const VERIFY_PROMPT = "Reply OK";

async function writeOAuthToken(root: string, token: string): Promise<void> {
  const t = token.trim();
  if (!t || /\s/.test(t)) throw new Error("a Claude token is one word without spaces or line breaks");
  await writeSecretFile(join(root, "claude", "oauth-token"), `${t}\n`);
}

// readClaudeVerify reads `claude -p --output-format json`: one result object ({type: "result", is_error, result,
// api_error_status}); ok when the CLI exited 0 and the result is not an error.
export function readClaudeVerify(r: RunResult): { ok: boolean; detail: string } {
  if (r.timedOut) return { ok: false, detail: "Claude Code did not answer in time" };
  let result: Record<string, unknown> | undefined;
  for (const line of r.stdout.split("\n").reverse()) {
    const s = line.trim();
    if (!s.startsWith("{")) continue;
    try {
      const v: unknown = JSON.parse(s);
      if (isRecord(v) && v.type === "result") {
        result = v;
        break;
      }
    } catch {
      // not JSON
    }
  }
  if (!result) {
    const err = r.stderr.trim() || r.stdout.trim() || `exit ${r.code ?? r.signal}`;
    return { ok: false, detail: `Claude Code gave no result: ${err}` };
  }
  const text = typeof result.result === "string" ? result.result.trim() : "";
  if (result.is_error === true || r.code !== 0) {
    const status = typeof result.api_error_status === "number" ? ` (HTTP ${result.api_error_status})` : "";
    return { ok: false, detail: `${text || "Claude Code reported an error"}${status}` };
  }
  return { ok: true, detail: text ? `Claude answered: ${text}` : "Claude answered" };
}

const ADAPTER = fileURLToPath(import.meta.resolve("@agentclientprotocol/claude-agent-acp/dist/index.js"));

// The SDK tool name, which the adapter reports in `_meta.claudeCode.toolName`.
function toolName(call: RawToolCall): string | undefined {
  const cc = call.meta.claudeCode;
  return isRecord(cc) && typeof cc.toolName === "string" ? cc.toolName : undefined;
}

// The name Claude gives an MCP tool in tool calls and permission rules: mcp__<server>__<tool>, characters outside
// [A-Za-z0-9_-] replaced by underscores (mixes.get → mcp__cadence__mixes_get; policy.ClaudeMCPTool on the server).
export function mcpToolName(server: string, tool: string): string {
  const clean = (s: string) => s.replace(/[^A-Za-z0-9_-]/g, "_");
  return `mcp__${clean(server)}__${clean(tool)}`;
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
  // Claude Code applies the ask and deny rules of the worktree's .claude/settings.json but not its allow rules (a
  // repository cannot widen its own permissions), so every Cadence call raised a permission request the preset then
  // answered. The preset's allowed Cadence tools go in as `allowedTools` instead (a CLI-argument rule); the file's
  // deny rules still win over them.
  sessionMeta(opts: LaunchOptions): Record<string, unknown> | undefined {
    const options: Record<string, unknown> = {};
    if (opts.thoughts) options.thinking = { type: "adaptive", display: "summarized" };
    const pre = opts.preAllowed;
    const allowed = pre ? pre.tools.map((t) => mcpToolName(pre.server, t)) : [];
    if (allowed.length) options.allowedTools = allowed;
    return Object.keys(options).length ? { claudeCode: { options } } : undefined;
  },

  // The agent-credentials volume holds `claude/`: either `oauth-token` (a long-lived token from `claude setup-token`,
  // preferred: no refresh-token rotation between session copies) or a whole Claude config dir from an interactive
  // login. The session gets its own CLAUDE_CONFIG_DIR, so the user's ~/.claude (skills, plugins, e-mail) never
  // reaches it (A1 surprise 3).
  async prepareHome(home: string, credentials: string | undefined): Promise<NodeJS.ProcessEnv> {
    // Development without a credentials volume runs on the developer's own login: still no claude.ai connectors.
    if (!credentials) return { ENABLE_CLAUDEAI_MCP_SERVERS: "false" };
    const dir = join(home, ".claude");
    await mkdir(dir, { recursive: true, mode: 0o700 });
    const src = join(credentials, "claude");
    await copyIfPresent(src, dir, (p) => !p.endsWith("/oauth-token"));
    // No telemetry, error reports or auto-update: the egress proxy allows the model API only (R4). No claude.ai
    // connectors (the account's Gmail, Drive, …): a setup-token lacks the user:mcp_servers scope anyway (checked on
    // the staging stand, 2026-09-30), but a config dir from an interactive login would carry it.
    const env: NodeJS.ProcessEnv = { CLAUDE_CONFIG_DIR: dir, CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC: "1", ENABLE_CLAUDEAI_MCP_SERVERS: "false" };
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
    await writeOAuthToken(credentials, token);
    console.log(`Saved ${join(dir, "oauth-token")}; Claude Code sessions use it from their next start.`);
  },

  // Settings → Agents: the same file the login writes.
  async writeCredential(root: string, task: CredentialTask): Promise<void> {
    if (!task.value) throw new Error("the task carries no token to write");
    await writeOAuthToken(root, task.value);
  },

  // Disconnect removes the whole Claude login (the token and any interactive login's config dir).
  async removeCredential(root: string): Promise<void> {
    await rm(join(root, "claude"), { recursive: true, force: true });
  },

  // `claude -p` with a cheap model through the stored token; the JSON result says whether the API accepted it.
  async verify(ctx: VerifyContext): Promise<VerifyResult> {
    const model = ctx.task.verifyModel || VERIFY_MODEL;
    const r = await ctx.run(claudeBinary(), ["-p", VERIFY_PROMPT, "--model", model, "--output-format", "json", "--max-turns", "1"]);
    return { ...readClaudeVerify(r), model };
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
