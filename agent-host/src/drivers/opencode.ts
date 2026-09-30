// opencode through its native ACP server, `opencode acp --cwd <worktree>`.
//
// Binary: OPENCODE_BIN, else the pinned `opencode-ai` package in node_modules, else `opencode` on PATH.
// Model and providers come from the project's opencode.json; a session model override is merged into
// OPENCODE_CONFIG_CONTENT (opencode's inline config, highest precedence). Tool names are opencode's: MCP tools are
// `<server>_<tool>`, shell is `bash`, edits are `edit`/`write`/`patch`.

import type * as acp from "@agentclientprotocol/sdk";
import { existsSync } from "node:fs";
import { mkdir, mkdtemp, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { fileURLToPath } from "node:url";
import type { LaunchSpec } from "../acp/transport.ts";
import { copyIfPresent, exists, readJsonObject, runInteractive, writeJsonSecret } from "./home.ts";
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

// The catalogue entry of a custom provider: an OpenAI-compatible base URL (e.g. a self-hosted vLLM).
const CUSTOM = "openai-compatible";
const VERIFY_PROMPT = "Reply OK";
const PROVIDER_ID = /^[a-z][a-z0-9-]{0,39}$/;

const isCustom = (t: CredentialTask): boolean => t.catalogueId === CUSTOM;

function checkProviderId(id: string): void {
  if (!PROVIDER_ID.test(id)) throw new Error(`${JSON.stringify(id)} is not a provider id`);
}

// writeProviderBlock merges a custom provider into opencode.json: `@ai-sdk/openai-compatible` at baseURL with its
// models (given as provider/model; the block keys them without the prefix). models undefined keeps the block's own.
async function writeProviderBlock(dir: string, id: string, name: string, baseURL: string, models: string[] | undefined): Promise<void> {
  const path = join(dir, "opencode.json");
  const cfg = await readJsonObject(path);
  const providers = isRecord(cfg.provider) ? { ...cfg.provider } : {};
  const old = providers[id];
  let keep: unknown = isRecord(old) && isRecord(old.models) ? old.models : {};
  if (models) {
    keep = Object.fromEntries(
      models.map((m) => (m.startsWith(`${id}/`) ? m.slice(id.length + 1) : m)).filter((m) => m !== "").map((m) => [m, { name: m }]),
    );
  }
  providers[id] = { npm: "@ai-sdk/openai-compatible", name, options: { baseURL }, models: keep };
  await writeJsonSecret(path, { $schema: "https://opencode.ai/config.json", ...cfg, provider: providers });
}

// PROBE lists an OpenAI-compatible server's models (GET <base>/models) from inside the sandbox; Node's fetch goes
// through the egress proxy with NODE_USE_ENV_PROXY=1. It prints {"ids": [...]} or {"error": "..."}.
const PROBE = `
const base = (process.env.CADENCE_PROBE_URL || "").replace(/\\/+$/, "");
const key = process.env.CADENCE_PROBE_KEY || "";
try {
  const res = await fetch(base + "/models", { headers: key ? { Authorization: "Bearer " + key } : {}, signal: AbortSignal.timeout(30000) });
  const text = await res.text();
  if (!res.ok) {
    console.log(JSON.stringify({ error: "HTTP " + res.status + ": " + text.slice(0, 300) }));
  } else {
    const body = JSON.parse(text);
    const list = Array.isArray(body) ? body : (body.data || body.models || []);
    const ids = list.map((m) => (typeof m === "string" ? m : m && m.id)).filter((m) => typeof m === "string");
    console.log(JSON.stringify({ ids }));
  }
} catch (e) {
  console.log(JSON.stringify({ error: String((e && e.cause && (e.cause.code || e.cause.message)) || (e && e.message) || e) }));
}
`;

export function readProbe(r: RunResult): { ids: string[] } | { error: string } {
  if (r.timedOut) return { error: "no answer in time" };
  for (const line of r.stdout.split("\n").reverse()) {
    try {
      const v: unknown = JSON.parse(line);
      if (isRecord(v) && typeof v.error === "string") return { error: v.error };
      if (isRecord(v) && Array.isArray(v.ids)) return { ids: v.ids.filter((x): x is string => typeof x === "string") };
    } catch {
      // not JSON
    }
  }
  return { error: (r.stderr.trim() || `exit ${r.code ?? r.signal}`).slice(0, 300) };
}

const ANSI = /\u001b\[[0-9;]*[A-Za-z]/g;
const stripAnsi = (s: string): string => s.replace(ANSI, "");

// readModels reads `opencode models <provider>`: one provider/model per line (openrouter's models have slashes).
export function readModels(stdout: string, provider: string): string[] {
  const out: string[] = [];
  for (const raw of stripAnsi(stdout).split("\n")) {
    const line = raw.trim();
    if (line.startsWith(`${provider}/`) && !/\s/.test(line) && !out.includes(line)) out.push(line);
  }
  return out;
}

// readRun reads `opencode run --format json`: JSON events, one per line; an `error` event (error.data.message) or a
// non-zero exit is a failure, the text parts are the answer.
export function readRun(r: RunResult): { ok: boolean; detail: string } {
  if (r.timedOut) return { ok: false, detail: "opencode did not answer in time" };
  let text = "";
  let error: string | undefined;
  for (const line of r.stdout.split("\n")) {
    const s = line.trim();
    if (!s.startsWith("{")) continue;
    let ev: unknown;
    try {
      ev = JSON.parse(s);
    } catch {
      continue;
    }
    if (!isRecord(ev)) continue;
    if (ev.type === "error") {
      const e = ev.error;
      const data = isRecord(e) ? e.data : undefined;
      const msg = isRecord(data) && typeof data.message === "string" ? data.message : isRecord(e) && typeof e.name === "string" ? e.name : "error";
      const status = isRecord(data) && typeof data.statusCode === "number" ? ` (HTTP ${data.statusCode})` : "";
      error = `${msg}${status}`;
    } else if (ev.type === "text") {
      const part = ev.part;
      if (isRecord(part) && typeof part.text === "string") text += part.text;
    }
  }
  if (error) return { ok: false, detail: error };
  if (r.code !== 0) return { ok: false, detail: stripAnsi(r.stderr).trim() || `opencode exited with ${r.code ?? r.signal}` };
  return { ok: true, detail: text.trim() ? `the model answered: ${text.trim()}` : "the model answered" };
}

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
    // auth.json under XDG_DATA_HOME; its entries are merged into opencode/auth.json (other providers stay).
    async login(credentials: string): Promise<void> {
      const dir = join(credentials, "opencode");
      await mkdir(dir, { recursive: true, mode: 0o700 });
      const scratch = await mkdtemp(join(tmpdir(), "opencode-login-"));
      try {
        console.log("Running `opencode auth login`: choose the provider (MiniMax for the Token Plan) and paste its key.");
        runInteractive(binary(), ["auth", "login"], { ...process.env, HOME: scratch, XDG_DATA_HOME: scratch });
        const fresh = await readJsonObject(join(scratch, "opencode", "auth.json"));
        if (Object.keys(fresh).length === 0) throw new Error("opencode wrote no auth.json; nothing was saved");
        const path = join(dir, "auth.json");
        await writeJsonSecret(path, { ...(await readJsonObject(path)), ...fresh });
        console.log(`Saved ${path}; opencode sessions use it from their next start.`);
      } finally {
        await rm(scratch, { recursive: true, force: true });
      }
    },

    // Settings → Agents: the provider's key into auth.json (merged, as `opencode auth login` stores it) and, for a
    // custom OpenAI-compatible provider, its block in opencode.json (base URL and models).
    async writeCredential(root: string, task: CredentialTask): Promise<void> {
      const dir = join(root, "opencode");
      checkProviderId(task.provider);
      if (task.value !== undefined && task.value !== "") {
        const key = task.value.trim();
        if (!key || /\s/.test(key)) throw new Error("an API key is one word without spaces or line breaks");
        const path = join(dir, "auth.json");
        await writeJsonSecret(path, { ...(await readJsonObject(path)), [task.provider]: { type: "api", key } });
      }
      if (isCustom(task)) {
        if (!task.baseUrl) throw new Error("a custom provider needs its base URL");
        const known = task.models?.length ? task.models : undefined;
        await writeProviderBlock(dir, task.provider, task.name || task.provider, task.baseUrl, known);
      }
    },

    async removeCredential(root: string, task: CredentialTask): Promise<void> {
      const dir = join(root, "opencode");
      checkProviderId(task.provider);
      for (const [file, drop] of [
        ["auth.json", (o: Record<string, unknown>) => delete o[task.provider]],
        ["opencode.json", (o: Record<string, unknown>) => isRecord(o.provider) && delete o.provider[task.provider]],
      ] as const) {
        const path = join(dir, file);
        if (!(await exists(path))) continue;
        const obj = await readJsonObject(path);
        drop(obj);
        await writeJsonSecret(path, obj);
      }
    },

    // A custom provider's models come from its /models endpoint (opencode knows only models its config lists); then
    // `opencode models <provider>` lists what sessions can pick and `opencode run` sends one tiny request.
    async verify(ctx: VerifyContext): Promise<VerifyResult> {
      const t = ctx.task;
      checkProviderId(t.provider);
      if (isCustom(t)) {
        const dir = join(ctx.credentials, "opencode");
        const cfg = await readJsonObject(join(dir, "opencode.json"));
        const block = isRecord(cfg.provider) ? cfg.provider[t.provider] : undefined;
        const configured = isRecord(block) && isRecord(block.options) && typeof block.options.baseURL === "string" ? block.options.baseURL : undefined;
        const baseUrl = t.baseUrl || configured;
        if (!baseUrl) return { ok: false, detail: `the custom provider ${t.provider} has no base URL` };
        const auth = await readJsonObject(join(dir, "auth.json"));
        const entry = auth[t.provider];
        const key = isRecord(entry) && typeof entry.key === "string" ? entry.key : "";
        const probe = await ctx.run(process.execPath, ["--input-type=module", "-e", PROBE], {
          env: { NODE_USE_ENV_PROXY: "1", CADENCE_PROBE_URL: baseUrl, CADENCE_PROBE_KEY: key },
          timeoutMs: 45_000,
        });
        const found = readProbe(probe);
        if ("error" in found) return { ok: false, detail: `${baseUrl}/models: ${found.error}` };
        if (found.ids.length === 0) return { ok: false, detail: `${baseUrl}/models lists no models` };
        const name = t.name || (isRecord(block) && typeof block.name === "string" ? block.name : t.provider);
        await writeProviderBlock(dir, t.provider, name, baseUrl, found.ids.map((m) => `${t.provider}/${m}`));
        await this.prepareHome?.(ctx.home, ctx.credentials);
      }
      const listed = await ctx.run(binary(), ["models", t.provider], { timeoutMs: 60_000 });
      const models = readModels(listed.stdout, t.provider);
      if (listed.code !== 0 || models.length === 0) {
        const why = stripAnsi(listed.stderr || listed.stdout).trim() || `exit ${listed.code ?? listed.signal}`;
        return { ok: false, detail: `opencode lists no models for ${t.provider}: ${why}`, models };
      }
      // The catalogue's cheap model when opencode lists it, else the provider's first model.
      const model = t.verifyModel && models.includes(t.verifyModel) ? t.verifyModel : models[0]!;
      const ran = await ctx.run(binary(), ["run", "--format", "json", "-m", model, VERIFY_PROMPT], { timeoutMs: 90_000 });
      return { ...readRun(ran), model, models };
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
