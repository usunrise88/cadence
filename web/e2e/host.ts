import { execFileSync } from "node:child_process";
import { mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import os from "node:os";
import path from "node:path";
import { expect, request as playwrightRequest, type APIRequestContext } from "@playwright/test";
import type { AgentSession, AgentToolCall, HostDecision, HostEntry, HostReport, HostStart, HostWork } from "../src/api/gen/types.gen";
import { API_URL, McpAgent } from "./agent";

// A scripted agent host for the specs: it speaks the host protocol (hostSessions.claim|report|ask|decision) with
// the host credential the e2e stack writes to CADENCE_HOST_TOKEN_FILE (e2e/stack.sh), so a spec can drive a session
// turn by turn — streamed text, a permission request, a Cadence tool call through MCP with the session token, a
// commit on the session branch — without a real agent.

const TOKEN_FILE = path.resolve(import.meta.dirname, "../.e2e/host-token");

export class ScriptedHost {
  readonly hostId = `e2e-host-${process.pid}`;
  private readonly ctx: APIRequestContext;
  private readonly started = new Map<string, HostStart>();
  private readonly pending: HostWork = { start: [], messages: [], controls: [], decisions: [] };

  private constructor(ctx: APIRequestContext) {
    this.ctx = ctx;
  }

  static async connect(): Promise<ScriptedHost> {
    const token = readFileSync(TOKEN_FILE, "utf8").trim();
    const ctx = await playwrightRequest.newContext({ baseURL: API_URL, extraHTTPHeaders: { Authorization: `Bearer ${token}` } });
    return new ScriptedHost(ctx);
  }

  private async claim(wait = 1): Promise<void> {
    const res = await this.ctx.post("/api/host-sessions:claim", { data: { hostId: this.hostId, version: "e2e", wait, capacity: 4 } });
    expect(res.status(), await res.text()).toBe(200);
    const w = (await res.json()) as HostWork;
    for (const s of w.start) this.started.set(s.session.id, s);
    this.pending.start.push(...w.start);
    this.pending.messages.push(...w.messages);
    this.pending.controls.push(...w.controls);
    this.pending.decisions.push(...w.decisions);
  }

  /** Claims until the session is handed to this host; reports it running. */
  async start(sessionId: string): Promise<HostStart> {
    await expect.poll(async () => (await this.claim(), this.started.has(sessionId)), { timeout: 20_000, intervals: [100] }).toBe(true);
    const st = this.started.get(sessionId)!;
    await this.report(sessionId, { state: { state: "running", busy: false } });
    return st;
  }

  /** Claims until a user message of the session arrives; returns its text. */
  async message(sessionId: string): Promise<string> {
    let text: string | undefined;
    await expect
      .poll(
        async () => {
          await this.claim();
          const i = this.pending.messages.findIndex((m) => m.sessionId === sessionId);
          if (i >= 0) text = this.pending.messages.splice(i, 1)[0]!.text;
          return text !== undefined;
        },
        { timeout: 20_000, intervals: [100] },
      )
      .toBe(true);
    return text!;
  }

  /** Claims until a control of the session arrives (cancel, pause, resume, end). */
  async control(sessionId: string, action: string): Promise<void> {
    await expect
      .poll(
        async () => {
          await this.claim();
          const i = this.pending.controls.findIndex((c) => c.sessionId === sessionId && c.action === action);
          if (i >= 0) this.pending.controls.splice(i, 1);
          return i >= 0;
        },
        { timeout: 20_000, intervals: [100] },
      )
      .toBe(true);
  }

  async report(sessionId: string, body: Omit<HostReport, "hostId">): Promise<AgentSession> {
    const res = await this.ctx.post(`/api/host-sessions/${sessionId}:report`, { data: { hostId: this.hostId, ...body } });
    expect(res.status(), await res.text()).toBe(200);
    return (await res.json()) as AgentSession;
  }

  async entries(sessionId: string, turn: number, entries: Omit<HostEntry, "turn">[]): Promise<void> {
    await this.report(sessionId, { entries: entries.map((e) => ({ ...e, turn })) });
  }

  /** Raises an ACP permission request; the preset answers it or it becomes an approval (outcome pending). */
  async ask(sessionId: string, turn: number, toolCall: AgentToolCall): Promise<HostDecision> {
    const res = await this.ctx.post(`/api/host-sessions/${sessionId}:ask`, {
      data: {
        hostId: this.hostId,
        turn,
        toolCall,
        options: [
          { optionId: "allow", name: "Allow once", kind: "allow_once" },
          { optionId: "always", name: "Allow always", kind: "allow_always" },
          { optionId: "reject", name: "Reject", kind: "reject_once" },
        ],
      },
    });
    expect(res.status(), await res.text()).toBe(200);
    return (await res.json()) as HostDecision;
  }

  /** Waits for a person's decision on an agent-permission approval. */
  async decision(sessionId: string, approvalId: string): Promise<HostDecision> {
    let d: HostDecision | undefined;
    await expect
      .poll(
        async () => {
          const res = await this.ctx.get(`/api/host-sessions/${sessionId}:decision`, { params: { approvalId, hostId: this.hostId, wait: 2 } });
          expect(res.status(), await res.text()).toBe(200);
          d = (await res.json()) as HostDecision;
          return d.outcome;
        },
        { timeout: 20_000, intervals: [100] },
      )
      .not.toBe("pending");
    return d!;
  }

  /** The agent's MCP connection with the session token the claim handed over. */
  async mcp(sessionId: string, project: string): Promise<McpAgent> {
    return McpAgent.connect(this.started.get(sessionId)!.token, project);
  }

  /** Commits a file on the session branch and pushes it, as the host does after a turn; returns the commit. */
  commit(sessionId: string, project: string, file: string, content: string): string {
    const st = this.started.get(sessionId)!;
    const dir = mkdtempSync(path.join(os.tmpdir(), "cadence-e2e-host-"));
    try {
      const url = new URL(st.cloneUrl, API_URL);
      url.username = "x-token";
      url.password = st.token;
      const branch = st.session.branch;
      const git = (...args: string[]) => execFileSync("git", args, { cwd: dir, encoding: "utf8", env: { ...process.env, GIT_TERMINAL_PROMPT: "0" } }).trim();
      git("clone", "--quiet", "--branch", branch, url.toString(), ".");
      writeFileSync(path.join(dir, file), content);
      git("add", file);
      git("-c", "user.name=agent", "-c", "user.email=agent@cadence.local", "commit", "--quiet", "-m", `agent: turn 1 (${project})`);
      git("push", "--quiet", "origin", `HEAD:${branch}`);
      return git("rev-parse", "HEAD");
    } finally {
      rmSync(dir, { recursive: true, force: true });
    }
  }

  async close(): Promise<void> {
    await this.ctx.dispose();
  }
}
