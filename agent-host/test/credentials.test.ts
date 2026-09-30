// Agent credentials on the host (Settings → Agents): the files the drivers write into the agent-credentials volume,
// and verification through fake `claude` and `opencode` binaries (CLAUDE_BIN, OPENCODE_BIN) run by the credential
// worker in its sandbox, with an in-memory control plane.

import assert from "node:assert/strict";
import { chmod, mkdir, mkdtemp, readFile, rm, stat, writeFile } from "node:fs/promises";
import { createServer, type Server } from "node:http";
import type { AddressInfo } from "node:net";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { after, before, beforeEach, describe, test } from "node:test";
import { claudeDriver, driverFor, opencodeDriver } from "../src/drivers/index.ts";
import { readClaudeVerify } from "../src/drivers/claude.ts";
import { readModels, readRun } from "../src/drivers/opencode.ts";
import type { CredentialTask } from "../src/drivers/types.ts";
import type { CredentialPlane, HostCredentialAck, HostCredentialReport, HostCredentialWork } from "../src/host/api.ts";
import { CredentialWorker, redactDetail } from "../src/host/credentials.ts";
import { silentLogger } from "../src/host/log.ts";

const GOOD_CLAUDE = `sk-ant-oat01-${"G".repeat(40)}`;
const BAD_CLAUDE = `sk-ant-oat01-${"B".repeat(40)}`;

let base: string;
let seq = 0;

// A fake `claude`: -p with --output-format json; accepts GOOD_CLAUDE only (CLAUDE_CODE_OAUTH_TOKEN, as the driver's
// prepareHome sets it) and names the model it was asked for.
const FAKE_CLAUDE = `
const args = process.argv.slice(2);
const model = args[args.indexOf("--model") + 1];
const ok = process.env.CLAUDE_CODE_OAUTH_TOKEN === ${JSON.stringify(GOOD_CLAUDE)} && args.includes("-p") && args.includes("json");
if (ok) {
  console.log(JSON.stringify({ type: "result", subtype: "success", is_error: false, result: "OK from " + model }));
} else {
  console.log(JSON.stringify({ type: "result", subtype: "success", is_error: true, api_error_status: 401,
    result: "Failed to authenticate. API Error: 401 OAuth access token is invalid. " + (process.env.CLAUDE_CODE_OAUTH_TOKEN || "") }));
  process.exit(1);
}
`;

// A fake `opencode`: models <provider> lists the provider's models when auth.json (copied into the sandbox's XDG data
// dir) has a key for it, or the custom provider's block in opencode.json; run -m answers, or fails for key "bad".
const FAKE_OPENCODE = `
const fs = require("node:fs");
const path = require("node:path");
const read = (p) => { try { return JSON.parse(fs.readFileSync(p, "utf8")); } catch { return {}; } };
const auth = read(path.join(process.env.XDG_DATA_HOME, "opencode", "auth.json"));
const cfg = read(path.join(process.env.XDG_CONFIG_HOME, "opencode", "opencode.json"));
const [cmd, ...rest] = process.argv.slice(2);
const catalogue = { minimax: ["MiniMax-M2.5", "MiniMax-M3"], openrouter: ["anthropic/claude-haiku-4.5"] };
if (cmd === "models") {
  const p = rest[0];
  const block = cfg.provider && cfg.provider[p];
  const list = block ? Object.keys(block.models || {}) : auth[p] ? catalogue[p] || [] : null;
  if (!list) { process.stderr.write("\\u001b[91mError: \\u001b[0mProvider not found: " + p + "\\n"); process.exit(1); }
  for (const m of list) console.log(p + "/" + m);
} else if (cmd === "run") {
  const model = rest[rest.indexOf("-m") + 1];
  const p = model.split("/")[0];
  if (auth[p] && auth[p].key === "bad") {
    console.log(JSON.stringify({ type: "error", error: { name: "APIError", data: { message: "login fail: invalid key " + auth[p].key, statusCode: 401 } } }));
    process.exit(1);
  }
  console.log(JSON.stringify({ type: "step_start" }));
  console.log(JSON.stringify({ type: "text", part: { text: "OK" } }));
} else { process.exit(2); }
`;

async function fakeBin(name: string, body: string): Promise<string> {
  const p = join(base, "bin", name);
  await mkdir(join(base, "bin"), { recursive: true });
  await writeFile(p, `#!${process.execPath}\n${body}`);
  await chmod(p, 0o755);
  return p;
}

const mode = async (p: string): Promise<number> => (await stat(p)).mode & 0o777;
const json = async (p: string): Promise<Record<string, unknown>> => JSON.parse(await readFile(p, "utf8")) as Record<string, unknown>;

function task(over: Partial<CredentialTask>): CredentialTask {
  return { id: `act_${++seq}`, action: "write", credentialId: "claude-code", agent: "claude-code", provider: "anthropic", catalogueId: "claude-subscription", ...over };
}

class FakePlane implements CredentialPlane {
  queue: CredentialTask[][] = [];
  reports: Array<{ id: string; body: HostCredentialReport }> = [];
  onEmpty?: () => void;
  claimCredentials(): Promise<HostCredentialWork> {
    const next = this.queue.shift();
    if (!next) this.onEmpty?.();
    return Promise.resolve({ tasks: next ?? [] });
  }
  reportCredential(id: string, body: HostCredentialReport): Promise<HostCredentialAck> {
    this.reports.push({ id, body: structuredClone(body) });
    return Promise.resolve({ id, state: "done" });
  }
}

function worker(cp: CredentialPlane, root: string | undefined): CredentialWorker {
  return new CredentialWorker({
    cp, hostId: "host-1", dataDir: join(base, `data-${++seq}`), ...(root ? { credentials: root } : {}),
    hostEnv: { PATH: process.env.PATH }, log: silentLogger, driverFor, waitSeconds: 0, timeoutMs: 20_000,
  });
}

before(async () => {
  base = await mkdtemp(join(tmpdir(), "host-creds-"));
  process.env.CLAUDE_BIN = await fakeBin("claude", FAKE_CLAUDE);
  process.env.OPENCODE_BIN = await fakeBin("opencode", FAKE_OPENCODE);
});

after(async () => {
  delete process.env.CLAUDE_BIN;
  delete process.env.OPENCODE_BIN;
  await rm(base, { recursive: true, force: true });
});

let root: string;
beforeEach(async () => {
  root = join(base, `volume-${++seq}`);
  await mkdir(root, { mode: 0o700 });
});

describe("credential files", () => {
  test("claude: claude/oauth-token, 0600 in a 0700 directory; removal drops the login", async () => {
    await claudeDriver.writeCredential!(root, task({ value: `${GOOD_CLAUDE}\n` }));
    const file = join(root, "claude", "oauth-token");
    assert.equal(await readFile(file, "utf8"), `${GOOD_CLAUDE}\n`);
    assert.equal(await mode(file), 0o600);
    assert.equal(await mode(join(root, "claude")), 0o700);
    const env = await claudeDriver.prepareHome!(join(root, "home"), root);
    assert.equal(env.CLAUDE_CODE_OAUTH_TOKEN, GOOD_CLAUDE, "the session reads what was written");
    assert.equal(env.ENABLE_CLAUDEAI_MCP_SERVERS, "false", "the account's claude.ai connectors never reach a session");
    await claudeDriver.removeCredential!(root, task({ action: "remove" }));
    await assert.rejects(stat(join(root, "claude")));
    await assert.rejects(claudeDriver.writeCredential!(root, task({ value: "two words" })), /one word/);
  });

  test("opencode: auth.json merges providers; a custom provider gets its block in opencode.json", async () => {
    const dir = join(root, "opencode");
    await mkdir(dir, { mode: 0o700 });
    await writeFile(join(dir, "auth.json"), JSON.stringify({ deepseek: { type: "api", key: "dk" } }), { mode: 0o600 });
    const oc = { agent: "opencode" as const, credentialId: "opencode.minimax" };
    await opencodeDriver.writeCredential!(root, task({ ...oc, provider: "minimax", catalogueId: "minimax", value: "mm-key" }));
    assert.deepEqual(await json(join(dir, "auth.json")), { deepseek: { type: "api", key: "dk" }, minimax: { type: "api", key: "mm-key" } });
    assert.equal(await mode(join(dir, "auth.json")), 0o600);
    await assert.rejects(stat(join(dir, "opencode.json")), "a catalogue provider needs no config block");

    const vllm = { agent: "opencode" as const, credentialId: "opencode.vllm", provider: "vllm", catalogueId: "openai-compatible", name: "Lab vLLM", baseUrl: "http://vllm.lan:8000/v1" };
    await opencodeDriver.writeCredential!(root, task({ ...vllm, models: ["vllm/qwen3-coder"] }));
    const cfg = await json(join(dir, "opencode.json"));
    assert.equal(cfg.$schema, "https://opencode.ai/config.json");
    assert.deepEqual((cfg.provider as Record<string, unknown>).vllm, {
      npm: "@ai-sdk/openai-compatible", name: "Lab vLLM", options: { baseURL: "http://vllm.lan:8000/v1" }, models: { "qwen3-coder": { name: "qwen3-coder" } },
    });
    assert.equal(await mode(join(dir, "opencode.json")), 0o600);
    assert.equal((await json(join(dir, "auth.json"))).vllm, undefined, "no key given: none stored");
    // A later write without models keeps the known ones (e.g. a new base URL).
    await opencodeDriver.writeCredential!(root, task({ ...vllm, baseUrl: "http://vllm.lan:9000/v1" }));
    const block = ((await json(join(dir, "opencode.json"))).provider as Record<string, Record<string, unknown>>).vllm!;
    assert.deepEqual(block.options, { baseURL: "http://vllm.lan:9000/v1" });
    assert.deepEqual(block.models, { "qwen3-coder": { name: "qwen3-coder" } });

    await opencodeDriver.removeCredential!(root, task({ ...vllm, action: "remove" }));
    await opencodeDriver.removeCredential!(root, task({ ...oc, provider: "minimax", catalogueId: "minimax", action: "remove" }));
    assert.deepEqual(await json(join(dir, "auth.json")), { deepseek: { type: "api", key: "dk" } });
    assert.deepEqual((await json(join(dir, "opencode.json"))).provider, {});
    await assert.rejects(opencodeDriver.writeCredential!(root, task({ ...oc, provider: "../x", catalogueId: "minimax", value: "k" })), /not a provider id/);
  });
});

describe("output parsing", () => {
  const r = (stdout: string, code = 0, stderr = "") => ({ code, signal: null, stdout, stderr, timedOut: false });
  test("claude -p json", () => {
    assert.deepEqual(readClaudeVerify(r('{"type":"result","is_error":false,"result":"OK"}')), { ok: true, detail: "Claude answered: OK" });
    const bad = readClaudeVerify(r('{"type":"result","is_error":true,"api_error_status":401,"result":"Failed to authenticate."}', 1));
    assert.deepEqual(bad, { ok: false, detail: "Failed to authenticate. (HTTP 401)" });
    assert.equal(readClaudeVerify(r("", 1, "boom")).ok, false);
  });
  test("opencode models and run", () => {
    assert.deepEqual(readModels("\u001b[0mminimax/MiniMax-M3\nminimax/MiniMax-M2.5\nnoise line\nother/x\n", "minimax"), ["minimax/MiniMax-M3", "minimax/MiniMax-M2.5"]);
    assert.deepEqual(readModels("openrouter/anthropic/claude-haiku-4.5\n", "openrouter"), ["openrouter/anthropic/claude-haiku-4.5"]);
    assert.deepEqual(readRun(r('{"type":"text","part":{"text":"OK"}}\n')), { ok: true, detail: "the model answered: OK" });
    const err = readRun(r('{"type":"error","error":{"name":"APIError","data":{"message":"login fail","statusCode":401}}}', 1));
    assert.deepEqual(err, { ok: false, detail: "login fail (HTTP 401)" });
  });
  test("details are redacted and shortened", () => {
    assert.equal(redactDetail(`bad key sk-ant-oat01-${"x".repeat(30)} and mine`, ["mine"]), "bad key [redacted] and [redacted]");
    assert.equal(redactDetail(`Authorization: Bearer abc.def`), "Authorization: Bearer [redacted]");
    assert.equal(redactDetail("x".repeat(20) + " " + "y".repeat(2000)).length <= 500, true);
  });
});

describe("credential worker", () => {
  test("claim → write → report; verify runs the fake claude in the sandbox", async () => {
    const cp = new FakePlane();
    const stop = new AbortController();
    cp.queue.push([task({ value: GOOD_CLAUDE })], [task({ action: "verify", verifyModel: "haiku" })]);
    cp.onEmpty = () => stop.abort();
    await worker(cp, root).run(stop.signal);
    assert.equal(cp.reports.length, 2);
    assert.deepEqual(cp.reports[0]!.body, { hostId: "host-1", ok: true, detail: "written to the agent-credentials volume" });
    assert.deepEqual(cp.reports[1]!.body, { hostId: "host-1", ok: true, detail: "Claude answered: OK from haiku", model: "haiku" });
  });

  test("a rejected Claude token fails verification without the token in the detail", async () => {
    const cp = new FakePlane();
    const w = worker(cp, root);
    await w.process(task({ value: BAD_CLAUDE }));
    const rep = await w.process(task({ action: "verify" }));
    assert.equal(rep.ok, false);
    assert.equal(rep.model, "haiku");
    assert.match(rep.detail ?? "", /401/);
    assert.doesNotMatch(rep.detail ?? "", /BBBB/);
  });

  test("opencode verify reports the provider's models; a bad key fails", async () => {
    const cp = new FakePlane();
    const w = worker(cp, root);
    const mm = { agent: "opencode" as const, credentialId: "opencode.minimax", provider: "minimax", catalogueId: "minimax" };
    await w.process(task({ ...mm, value: "good" }));
    const ok = await w.process(task({ ...mm, action: "verify", verifyModel: "minimax/MiniMax-M3" }));
    assert.deepEqual(ok, { hostId: "host-1", ok: true, detail: "the model answered: OK", model: "minimax/MiniMax-M3", models: ["minimax/MiniMax-M2.5", "minimax/MiniMax-M3"] });
    await w.process(task({ ...mm, value: "bad" }));
    const bad = await w.process(task({ ...mm, action: "verify" }));
    assert.equal(bad.ok, false);
    assert.equal(bad.model, "minimax/MiniMax-M2.5", "without a verify model the first listed one");
    assert.match(bad.detail ?? "", /login fail/);
    const none = await w.process(task({ ...mm, provider: "openrouter", credentialId: "opencode.openrouter", catalogueId: "openrouter", action: "verify" }));
    assert.equal(none.ok, false);
    assert.match(none.detail ?? "", /no models for openrouter/);
  });

  describe("custom provider", () => {
    let server: Server;
    let url: string;
    let auth: string | undefined;
    before(async () => {
      server = createServer((req, res) => {
        auth = req.headers.authorization;
        if (req.url !== "/v1/models") {
          res.writeHead(404).end("no");
          return;
        }
        res.writeHead(200, { "Content-Type": "application/json" }).end(JSON.stringify({ object: "list", data: [{ id: "qwen3-coder" }, { id: "llama-4" }] }));
      });
      await new Promise<void>((r) => server.listen(0, "127.0.0.1", r));
      url = `http://127.0.0.1:${(server.address() as AddressInfo).port}/v1`;
    });
    after(() => new Promise<void>((r) => server.close(() => r())));

    test("the probe lists /models into the provider block, then opencode lists and runs them", async () => {
      const cp = new FakePlane();
      const w = worker(cp, root);
      const vllm = { agent: "opencode" as const, credentialId: "opencode.vllm", provider: "vllm", catalogueId: "openai-compatible", name: "vLLM", baseUrl: url };
      assert.equal((await w.process(task({ ...vllm, value: "vk" }))).ok, true);
      const rep = await w.process(task({ ...vllm, action: "verify" }));
      assert.deepEqual(rep, { hostId: "host-1", ok: true, detail: "the model answered: OK", model: "vllm/qwen3-coder", models: ["vllm/qwen3-coder", "vllm/llama-4"] });
      assert.equal(auth, "Bearer vk");
      const cfg = await json(join(root, "opencode", "opencode.json"));
      assert.deepEqual(Object.keys(((cfg.provider as Record<string, Record<string, unknown>>).vllm!.models as object)), ["qwen3-coder", "llama-4"]);
      const wrong = await w.process(task({ ...vllm, baseUrl: `${url}/nope`, action: "verify" }));
      assert.equal(wrong.ok, false);
      assert.match(wrong.detail ?? "", /HTTP 404/);
    });
  });

  test("without the agent-credentials volume every task fails with a reason", async () => {
    const cp = new FakePlane();
    const rep = await worker(cp, undefined).process(task({ value: GOOD_CLAUDE }));
    assert.equal(rep.ok, false);
    assert.match(rep.detail ?? "", /CADENCE_AGENT_CREDENTIALS/);
    assert.equal(cp.reports.length, 1);
  });
});
