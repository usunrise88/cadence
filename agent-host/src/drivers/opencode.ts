// opencode through its native ACP server, `opencode acp --cwd <worktree>`.
//
// Binary: OPENCODE_BIN, else the pinned `opencode-ai` package in node_modules, else `opencode` on PATH.
// Model and providers come from the project's opencode.json; a session model override is merged into
// OPENCODE_CONFIG_CONTENT (opencode's inline config, highest precedence). Tool names are opencode's: MCP tools are
// `<server>_<tool>`, shell is `bash`, edits are `edit`/`write`/`patch`.

import type * as acp from "@agentclientprotocol/sdk";
import { existsSync } from "node:fs";
import { chmod, mkdir, mkdtemp, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { fileURLToPath } from "node:url";
import type { LaunchSpec } from "../acp/transport.ts";
import { copyIfPresent, runInteractive } from "./home.ts";
import { defaultReading, isRecord } from "./normalize.ts";
import type { Driver, LaunchOptions, RawToolCall, ToolReading } from "./types.ts";

const LOCAL_BIN = fileURLToPath(new URL("../../node_modules/.bin/opencode", import.meta.url));

// Tools opencode ships; anything else with a `<server>_<tool>` shape is an MCP tool (see mcpServers below).
const BUILTIN = new Set([
  "bash", "edit", "write", "patch", "read", "grep", "glob", "list", "webfetch", "websearch", "codesearch",
  "todowrite", "todoread", "task", "skill", "lsp", "multiedit", "invalid", "question", "plan_enter", "plan_exit",
]);

function binary(): string {
  if (process.env.OPENCODE_BIN) return process.env.OPENCODE_BIN;
  return existsSync(LOCAL_BIN) ? LOCAL_BIN : "opencode";
}

function inlineConfig(existing: string | undefined, model: string): string {
  let base: Record<string, unknown> = {};
  if (existing) {
    const parsed: unknown = JSON.parse(existing);
    if (isRecord(parsed)) base = parsed;
  }
  return JSON.stringify({ ...base, model });
}

function shellResult(call: RawToolCall, command: string): ToolReading["shell"] {
  const shell: NonNullable<ToolReading["shell"]> = { command };
  const out = call.rawOutput;
  if (isRecord(out)) {
    if (typeof out.output === "string") shell.output = out.output;
    const meta = out.metadata;
    if (isRecord(meta)) {
      if (typeof meta.exit === "number") shell.exitCode = meta.exit;
      if (shell.output === undefined && typeof meta.output === "string") shell.output = meta.output;
    }
  }
  return shell;
}

const PLAN_STATUS = new Set(["pending", "in_progress", "completed"]);
const PLAN_PRIORITY = new Set(["high", "medium", "low"]);

// opencode keeps its todo list in the `todowrite` tool ({todos: [{content, status, priority}]}) and sends no ACP
// `plan`; the list is the same shape, so it becomes the plan. Cancelled todos count as completed.
export function todosToPlan(rawInput: unknown): acp.PlanEntry[] | undefined {
  if (!isRecord(rawInput) || !Array.isArray(rawInput.todos)) return undefined;
  return rawInput.todos.filter(isRecord).map((t) => {
    const status = t.status === "cancelled" ? "completed" : String(t.status);
    const priority = String(t.priority);
    return {
      content: typeof t.content === "string" ? t.content : "",
      status: (PLAN_STATUS.has(status) ? status : "pending") as acp.PlanEntryStatus,
      priority: (PLAN_PRIORITY.has(priority) ? priority : "medium") as acp.PlanEntryPriority,
    };
  });
}

export function makeOpencodeDriver(mcpServerNames: readonly string[] = ["cadence"]): Driver {
  const mcpPrefixes = [...mcpServerNames].sort((a, b) => b.length - a.length);
  const parseMcp = (name: string): { server: string; tool: string } | undefined => {
    if (BUILTIN.has(name)) return undefined;
    for (const server of mcpPrefixes) {
      if (name.startsWith(`${server}_`) && name.length > server.length + 1) {
        return { server, tool: name.slice(server.length + 1) };
      }
    }
    return undefined;
  };

  return {
    name: "opencode",

    launch(opts: LaunchOptions): LaunchSpec {
      const env: NodeJS.ProcessEnv = { ...(opts.baseEnv ?? process.env), ...opts.env };
      if (opts.model) env.OPENCODE_CONFIG_CONTENT = inlineConfig(env.OPENCODE_CONFIG_CONTENT, opts.model);
      const cmd = opts.command ?? { command: binary(), args: ["acp", "--cwd", opts.cwd] };
      return { command: cmd.command, args: cmd.args, cwd: opts.cwd, env };
    },

    // The agent-credentials volume holds `opencode/auth.json` (what `opencode auth login` wrote: the MiniMax Token
    // Plan key, R6) and optionally `opencode/opencode.json` (provider settings); they go to the session's XDG data
    // and config directories, where opencode looks for them.
    async prepareHome(home: string, credentials: string | undefined): Promise<NodeJS.ProcessEnv> {
      if (!credentials) return {};
      const src = join(credentials, "opencode");
      await copyIfPresent(join(src, "auth.json"), join(home, ".local", "share", "opencode", "auth.json"));
      await copyIfPresent(join(src, "opencode.json"), join(home, ".config", "opencode", "opencode.json"));
      return {};
    },

    // `opencode auth login` (choose the provider — MiniMax for the Token Plan, R6 — and paste its key) writes
    // auth.json under XDG_DATA_HOME; it is stored as opencode/auth.json.
    async login(credentials: string): Promise<void> {
      const dir = join(credentials, "opencode");
      await mkdir(dir, { recursive: true, mode: 0o700 });
      const scratch = await mkdtemp(join(tmpdir(), "opencode-login-"));
      try {
        console.log("Running `opencode auth login`: choose the provider (MiniMax for the Token Plan) and paste its key.");
        runInteractive(binary(), ["auth", "login"], { ...process.env, HOME: scratch, XDG_DATA_HOME: scratch });
        const auth = join(scratch, "opencode", "auth.json");
        if (!(await copyIfPresent(auth, join(dir, "auth.json")))) throw new Error("opencode wrote no auth.json; nothing was saved");
        await chmod(join(dir, "auth.json"), 0o600);
        console.log(`Saved ${join(dir, "auth.json")}; opencode sessions use it from their next start.`);
      } finally {
        await rm(scratch, { recursive: true, force: true });
      }
    },

    readTool(call: RawToolCall): ToolReading {
      const base = defaultReading(call);
      if (call.title === "todowrite") {
        const plan = todosToPlan(call.rawInput);
        return plan ? { class: "think", plan } : { class: "think" };
      }
      const mcp = parseMcp(call.title);
      if (mcp) return { class: "mcp", mcp, title: `${mcp.server}.${mcp.tool}` };
      if (base.class === "shell" || call.title === "bash") {
        const command = isRecord(call.rawInput) && typeof call.rawInput.command === "string" ? call.rawInput.command : "";
        return { class: "shell", shell: shellResult(call, command) };
      }
      return base;
    },
  };
}

export const opencodeDriver: Driver = makeOpencodeDriver();
