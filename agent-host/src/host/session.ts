// One agent session on this host (docs/spec/05-agents.md "Session lifecycle"): a worktree cloned from the control
// plane on session/<id>, the agent spawned as the session's own user with the Cadence MCP server in session/new,
// turns from the messages the control plane hands over, a commit and push after each turn, the budget and runaway
// checks, the stuck-turn clock (R5), permission requests answered through the control plane, pause and resume
// (ACP resume, else a new ACP session primed with a summary), end and failure. Everything the Chat panel shows is
// reported to the control plane; the host keeps nothing the API does not have.

import type * as acp from "@agentclientprotocol/sdk";
import { Agent, type Driver, type HostUpdate, type PermissionDecision, type StartOptions } from "../drivers/index.ts";
import type {
  AgentPauseReason,
  AgentUse,
  ControlPlane,
  HostControl,
  HostDecision,
  HostEntry,
  HostMessage,
  HostReport,
  HostStart,
} from "./api.ts";
import { ApiError } from "./api.ts";
import type { Clock, Timer } from "./clock.ts";
import { Worktree } from "./git.ts";
import { baseEnv, own, prepareDirs, removeDirs, type SessionDirs, type SessionUser, type UidPool } from "./isolation.ts";
import { errText, type Logger } from "./log.ts";
import { RunawayDetector } from "./runaway.ts";
import { toolCall, Transcript } from "./transcript.ts";

export interface SessionDeps {
  cp: ControlPlane;
  clock: Clock;
  hostId: string;
  // The control plane as agents and git reach it (http://control-plane:8080).
  baseUrl: string;
  dataDir: string;
  // The agent-credentials volume; absent in development (agents use their default login).
  credentials?: string;
  // One Unix user per session when set (the host runs as root).
  uids?: UidPool;
  hostEnv: NodeJS.ProcessEnv;
  log: Logger;
  driverFor: (name: string) => Driver;
  // Tests: replace the agent command, and the clone URL (a local repository).
  launch?: (driver: Driver) => StartOptions["command"];
  repoUrl?: (start: HostStart) => string;
  // Stream the model's reasoning as thought chunks (default on).
  thoughts?: boolean;
  flushMs?: number;
  onEnded?: (id: string) => void;
}

type Phase = "starting" | "running" | "paused" | "ending" | "ended" | "failed";

// What to do when the cancelled turn has ended.
type After = { action: "pause"; reason: AgentPauseReason; note?: string } | { action: "end" } | { action: "none" };

const READ_ONLY = "read-only";

export class HostSession {
  readonly id: string;
  private phase: Phase = "starting";
  private readonly driver: Driver;
  private readonly transcript: Transcript;
  private readonly runaway: RunawayDetector;
  private readonly queue: HostMessage[] = [];
  private dirs?: SessionDirs;
  private user?: SessionUser;
  private worktree?: Worktree;
  private agentEnv: NodeJS.ProcessEnv = {};
  private homeEnv: NodeJS.ProcessEnv = {};
  private agent: Agent | undefined;
  private acpId: string | undefined;
  private stopping = false;
  private turn: number;
  private turnRunning = false;
  private after: After = { action: "none" };
  private use: AgentUse;
  private contextAtTurnStart: number | undefined;
  private stuck: Timer | undefined;
  private permissions = 0;
  private readonly waiting = new Map<string, (d: HostDecision) => void>();
  private readonly early = new Map<string, HostDecision>();
  private readonly withdrawn: string[] = [];
  private readonly nextPrompt: string[] = [];
  private summary: string | undefined;
  private chain: Promise<void> = Promise.resolve();
  private flushTimer: NodeJS.Timeout | undefined;
  private budget: HostStart["budget"];
  private noticeSeq = 0;

  constructor(
    private readonly deps: SessionDeps,
    private readonly start: HostStart,
  ) {
    this.id = start.session.id;
    this.driver = deps.driverFor(start.session.driver);
    this.transcript = new Transcript();
    this.runaway = new RunawayDetector(start.clocks.identicalCalls);
    this.turn = start.session.turn ?? start.session.use.turns;
    this.use = { ...start.session.use };
    this.budget = start.budget;
  }

  get state(): Phase {
    return this.phase;
  }

  get readOnly(): boolean {
    return this.start.session.kind === READ_ONLY;
  }

  private log(level: "info" | "warn" | "error", msg: string, attrs: Record<string, unknown> = {}): void {
    this.deps.log.log(level, msg, { session: this.id, ...attrs });
  }

  // ------------------------------------------------------------------ start

  async begin(): Promise<void> {
    try {
      this.user = this.deps.uids?.acquire(this.id);
      this.dirs = await prepareDirs(this.deps.dataDir, this.id, this.user);
      this.agentEnv = baseEnv(this.dirs, this.deps.hostEnv);
      const branch = this.start.session.branch || "main";
      this.worktree = new Worktree(this.dirs.worktree, branch, {
        token: this.start.token,
        baseEnv: this.agentEnv,
        ...(this.user ? { user: this.user } : {}),
      });
      this.transcript.setRoot(this.dirs.worktree);
      if (await this.worktree.isRepo()) {
        this.notice("The worktree from an earlier run of the session is reused");
        if (!this.readOnly) await this.worktree.pushPending();
      } else {
        const url = this.deps.repoUrl?.(this.start) ?? new URL(this.start.cloneUrl, this.deps.baseUrl).toString();
        await this.worktree.clone(url, {
          name: `${this.start.session.driver} (session ${this.start.session.number})`,
          email: `${this.id}@cadence.local`,
        });
      }
      this.homeEnv = (await this.driver.prepareHome?.(this.dirs.home, this.deps.credentials)) ?? {};
      await own(this.dirs.home, this.user);
      await this.spawn(this.start.resume);
      this.phase = "running";
      await this.report({ state: { state: "running", busy: false, turn: this.turn }, ...this.acpField() });
      this.pump();
    } catch (err) {
      await this.fail(err);
    }
  }

  private acpField(): Partial<HostReport> {
    return this.acpId ? { acpSessionId: this.acpId } : {};
  }

  private mcpServers(): acp.McpServer[] {
    return [
      {
        type: "http",
        name: "cadence",
        url: new URL(this.start.mcpUrl, this.deps.baseUrl).toString(),
        headers: [
          { name: "Authorization", value: `Bearer ${this.start.token}` },
          { name: "Cadence-Project", value: this.start.session.project },
        ],
      },
    ];
  }

  // spawn starts the agent process and its ACP session: a restore of the agent's own session when there is one,
  // else a new session (primed with the summary when a restore was wanted but failed).
  private async spawn(resume?: HostStart["resume"]): Promise<void> {
    const dirs = this.dirs;
    if (!dirs) throw new Error("session directories are not ready");
    this.stopping = false;
    const command = this.deps.launch?.(this.driver);
    const agent = await Agent.start(this.driver, {
      cwd: dirs.worktree,
      baseEnv: this.agentEnv,
      env: this.homeEnv,
      model: this.start.session.model,
      thoughts: this.deps.thoughts ?? true,
      onPermission: (req, signal) => this.permission(req, signal),
      ...(this.user ? { user: this.user } : {}),
      ...(command ? { command } : {}),
    });
    this.agent = agent;
    agent.onUpdate((u) => this.onUpdate(u));
    void agent.exited.then((r) => {
      if (this.agent === agent && !this.stopping) void this.crashed(r, agent.stderr());
    });
    const opts = { cwd: dirs.worktree, mcpServers: this.mcpServers() };
    if (resume?.acpSessionId) {
      try {
        const mode = await agent.restoreSession(resume.acpSessionId, opts);
        this.acpId = resume.acpSessionId;
        this.notice(`The agent's session was restored (ACP session/${mode})`);
        return;
      } catch (err) {
        this.log("warn", "ACP restore failed; a new session gets the summary", { err: errText(err) });
      }
    }
    this.acpId = await agent.newSession(opts);
    if (resume?.summary) {
      this.summary = resume.summary;
      this.notice("A new agent session continues this one; it gets a summary of the transcript");
    }
  }

  private async stopAgent(): Promise<void> {
    const agent = this.agent;
    if (!agent) return;
    this.stopping = true;
    this.agent = undefined;
    await agent.close().catch(() => undefined);
  }

  // ------------------------------------------------------------------ work from the control plane

  enqueue(m: HostMessage): void {
    this.queue.push(m);
    this.pump();
  }

  decision(d: HostDecision): void {
    if (!d.approvalId) return;
    const w = this.waiting.get(d.approvalId);
    if (w) w(d);
    else this.early.set(d.approvalId, d);
  }

  async control(c: HostControl): Promise<void> {
    this.log("info", "control", { action: c.action });
    switch (c.action) {
      case "cancel":
        if (this.turnRunning) await this.cancelTurn({ action: "none" });
        return;
      case "pause": {
        const reason = c.reason ?? { code: "user", message: "paused" };
        if (this.turnRunning) await this.cancelTurn({ action: "pause", reason, note: pauseNote(reason) });
        else if (this.phase === "running") await this.pause(reason, pauseNote(reason));
        return;
      }
      case "resume":
        if (c.budget) this.budget = c.budget;
        if (this.phase === "paused") await this.resume(c.resume);
        return;
      case "end":
        if (this.turnRunning) await this.cancelTurn({ action: "end" });
        else await this.end();
        return;
    }
  }

  private pump(): void {
    if (this.phase !== "running" || this.turnRunning || this.queue.length === 0) return;
    const m = this.queue.shift();
    if (!m) return;
    this.turnRunning = true;
    void this.runTurn(m)
      .catch((err: unknown) => this.fail(err))
      .finally(() => {
        this.turnRunning = false;
        void this.afterTurn().then(() => this.pump());
      });
  }

  // ------------------------------------------------------------------ turns

  private promptFor(m: HostMessage): string {
    const parts: string[] = [];
    if (this.summary) {
      parts.push(this.summary);
      this.summary = undefined;
    }
    parts.push(...this.nextPrompt.splice(0));
    if (m.context) parts.push(m.context);
    parts.push(m.kind === "notice" ? `[Cadence] ${m.text}` : m.text);
    return parts.join("\n\n");
  }

  private async runTurn(m: HostMessage): Promise<void> {
    const agent = this.agent;
    if (!agent || !this.acpId) throw new Error("the agent is not running");
    this.turn++;
    const turn = this.turn;
    if (m.kind === "user_message") this.runaway.reset();
    this.transcript.startTurn(turn);
    this.transcript.put({ key: `t${turn}:start`, kind: "turn", turn, turnInfo: { state: "started", messageId: m.id } });
    this.contextAtTurnStart = this.use.contextUsed;
    await this.report({ state: { state: "running", busy: true, turn } });
    this.armStuck();
    let stopReason = "error";
    let usage: { inputTokens: number; outputTokens: number; cachedReadTokens?: number } | undefined;
    try {
      const res = await agent.prompt(this.acpId, this.promptFor(m));
      stopReason = res.stopReason;
      if (res.usage) usage = res.usage;
    } catch (err) {
      if (this.agent === agent && !this.stopping) {
        this.notice(`The turn failed: ${errText(err)}`, "error");
      }
    } finally {
      this.disarmStuck();
    }
    if (this.phase === "failed" || this.phase === "ended") return;
    this.transcript.close();
    this.use.turns = turn;
    if (usage) {
      this.use.inputTokens += usage.inputTokens;
      this.use.outputTokens += usage.outputTokens;
      this.use.cachedReadTokens = (this.use.cachedReadTokens ?? 0) + (usage.cachedReadTokens ?? 0);
    }
    this.transcript.put({
      key: `t${turn}:end`,
      kind: "turn",
      turn,
      turnInfo: {
        state: "ended",
        stopReason,
        messageId: m.id,
        ...(usage ? { inputTokens: usage.inputTokens, outputTokens: usage.outputTokens } : {}),
        ...(usage?.cachedReadTokens ? { cachedReadTokens: usage.cachedReadTokens } : {}),
      },
    });
    await this.commit(turn);
    await this.report({ state: { state: "running", busy: false, turn }, use: this.use });
    if (this.after.action !== "none") return;
    const turnTokens = usage ? usage.inputTokens + usage.outputTokens : 0;
    if (this.readOnly) this.after = { action: "end" };
    else if (turnTokens > this.budget.tokensPerTurn) {
      const reason: AgentPauseReason = {
        code: "turn_tokens",
        message: `turn ${turn} used ${turnTokens} tokens, over the ${this.budget.tokensPerTurn} a turn may use`,
      };
      this.after = { action: "pause", reason, note: pauseNote(reason) };
    } else if (this.use.turns >= this.budget.turns) {
      const reason: AgentPauseReason = { code: "budget_turns", message: `the session used its ${this.budget.turns} turns` };
      this.after = { action: "pause", reason, note: pauseNote(reason) };
    } else if (this.use.inputTokens + this.use.outputTokens >= this.budget.tokens) {
      const reason: AgentPauseReason = { code: "budget_tokens", message: `the session used its ${this.budget.tokens} tokens` };
      this.after = { action: "pause", reason, note: pauseNote(reason) };
    }
  }

  private async afterTurn(): Promise<void> {
    const a = this.after;
    this.after = { action: "none" };
    if (a.action === "pause") await this.pause(a.reason, a.note);
    else if (a.action === "end") await this.end();
  }

  // cancelTurn stops the running turn (ACP session/cancel); `then` runs once it has ended.
  private async cancelTurn(then: After): Promise<void> {
    if (then.action !== "none" && this.after.action === "none") this.after = then;
    else if (then.action === "end") this.after = then;
    if (this.agent && this.acpId) await this.agent.cancel(this.acpId).catch(() => undefined);
  }

  private async commit(turn: number): Promise<void> {
    const wt = this.worktree;
    if (!wt || this.readOnly) return;
    try {
      const c = await wt.commitTurn(`cadence: turn ${turn} of session ${this.id}`);
      if (c.refused) {
        const found = c.refused.map((f) => `${f.path}${f.line ? `:${f.line}` : ""} (${f.kind})`).join(", ");
        this.transcript.put({ key: `t${turn}:commit`, kind: "commit", turn, commit: { refused: true, files: c.files, findings: c.refused } });
        this.notice(`The changes of turn ${turn} were not committed: the staged diff holds a credential in ${found}`, "error");
        this.nextPrompt.push(
          `[Cadence] Your changes in turn ${turn} were not committed because ${found} contains what looks like a credential. ` +
            "Remove it (never write tokens or keys into files); the next turn's changes are committed again.",
        );
      } else if (c.sha) {
        this.transcript.put({ key: `t${turn}:commit`, kind: "commit", turn, commit: { sha: c.sha, files: c.files } });
      }
    } catch (err) {
      this.notice(`The changes of turn ${turn} could not be committed or pushed: ${errText(err)}`, "error");
    }
  }

  // ------------------------------------------------------------------ updates, clocks, runaway

  private onUpdate(u: HostUpdate): void {
    if (this.turnRunning) this.armStuck();
    if (u.kind === "usage") {
      if (u.context) {
        this.use.contextUsed = u.context.used;
        this.use.contextSize = u.context.size;
        const grown = this.contextAtTurnStart === undefined ? 0 : u.context.used - this.contextAtTurnStart;
        if (this.turnRunning && grown > this.budget.tokensPerTurn && this.after.action === "none") {
          const reason: AgentPauseReason = {
            code: "turn_tokens",
            message: `turn ${this.turn} grew the context by ${grown} tokens, over the ${this.budget.tokensPerTurn} a turn may use`,
          };
          void this.cancelTurn({ action: "pause", reason, note: pauseNote(reason) });
        }
      }
      if (u.cost) this.use.costUsd = u.cost.amount;
    }
    if (u.kind === "tool_call") {
      const tool = u.call.mcp ? `${u.call.mcp.server}.${u.call.mcp.tool}` : u.call.title;
      const n = this.runaway.observe(u.call.id, tool, u.call.rawInput);
      if (n !== undefined && this.turnRunning && this.after.action === "none") {
        const reason: AgentPauseReason = {
          code: "runaway",
          message: `${tool} was called ${n} times in a row with the same arguments`,
        };
        void this.cancelTurn({ action: "pause", reason, note: pauseNote(reason) });
      }
    }
    this.transcript.apply(u);
    this.scheduleFlush();
  }

  private armStuck(): void {
    this.stuck?.cancel();
    this.stuck = undefined;
    if (!this.turnRunning || this.permissions > 0) return; // waiting for a person never pauses (R5)
    const secs = this.start.clocks.stuckTurnSeconds;
    this.stuck = this.deps.clock.after(secs * 1000, () => {
      if (!this.turnRunning || this.permissions > 0 || this.after.action !== "none") return;
      const reason: AgentPauseReason = {
        code: "stuck_turn",
        message: `no update from the agent for ${Math.round(secs / 60)} min during turn ${this.turn}`,
      };
      void this.cancelTurn({ action: "pause", reason, note: pauseNote(reason) });
    });
  }

  private disarmStuck(): void {
    this.stuck?.cancel();
    this.stuck = undefined;
  }

  // ------------------------------------------------------------------ permissions

  private async permission(
    req: { sessionId: string; call: Parameters<typeof toolCall>[0]; options: acp.PermissionOption[] },
    signal: AbortSignal,
  ): Promise<PermissionDecision> {
    this.permissions++;
    this.disarmStuck();
    let approvalId: string | undefined;
    try {
      const d = await this.deps.cp.ask(this.id, {
        hostId: this.deps.hostId,
        turn: this.turn,
        toolCall: toolCall(req.call, this.dirs?.worktree ?? ""),
        options: req.options.map((o) => ({ optionId: o.optionId, name: o.name, kind: o.kind })),
      });
      let outcome = d.outcome;
      if (outcome === "pending" && d.approvalId) {
        approvalId = d.approvalId;
        outcome = (await this.waitDecision(d.approvalId, signal)).outcome;
      }
      switch (outcome) {
        case "allow_once":
        case "allow_always":
        case "reject_once":
          return { select: outcome };
      }
      return "cancelled";
    } catch (err) {
      if (signal.aborted) {
        if (approvalId) this.withdrawn.push(approvalId);
        return "cancelled";
      }
      this.log("warn", "permission request failed; rejecting", { err: errText(err) });
      return { select: "reject_once" }; // fail closed
    } finally {
      this.permissions--;
      this.armStuck();
    }
  }

  private waitDecision(approvalId: string, signal: AbortSignal): Promise<HostDecision> {
    const early = this.early.get(approvalId);
    if (early) {
      this.early.delete(approvalId);
      return Promise.resolve(early);
    }
    return new Promise((resolve, reject) => {
      const onAbort = (): void => {
        this.waiting.delete(approvalId);
        reject(new Error("the turn was cancelled"));
      };
      signal.addEventListener("abort", onAbort, { once: true });
      this.waiting.set(approvalId, (d) => {
        signal.removeEventListener("abort", onAbort);
        this.waiting.delete(approvalId);
        resolve(d);
      });
    });
  }

  // ------------------------------------------------------------------ pause, resume, end, failure

  private async pause(reason: AgentPauseReason, note?: string): Promise<void> {
    if (this.phase !== "running") return;
    this.phase = "paused";
    this.notice(`Paused: ${reason.message}`, reason.code === "user" || reason.code === "idle" ? "info" : "warning");
    await this.stopAgent();
    await this.report({ state: { state: "paused", busy: false, turn: this.turn, reason }, ...(note ? { note } : {}) });
  }

  private async resume(info?: HostControl["resume"]): Promise<void> {
    try {
      await this.spawn(info ?? (this.acpId ? { acpSessionId: this.acpId } : undefined));
      this.phase = "running";
      await this.report({ state: { state: "running", busy: false, turn: this.turn }, ...this.acpField() });
      this.pump();
    } catch (err) {
      await this.fail(err);
    }
  }

  async end(): Promise<void> {
    if (this.phase === "ended" || this.phase === "ending") return;
    const wasFailed = this.phase === "failed";
    this.phase = "ending";
    await this.stopAgent();
    if (!wasFailed) {
      await this.report({ state: { state: "done", busy: false, turn: this.turn } });
    }
    await this.cleanup(true);
    this.phase = "ended";
    this.deps.onEnded?.(this.id);
  }

  private async crashed(r: { code: number | null; signal: NodeJS.Signals | null }, stderr: string): Promise<void> {
    const tail = stderr.trim().split("\n").slice(-5).join("\n");
    await this.fail(new Error(`the ${this.driver.name} process exited (code ${r.code}, signal ${r.signal})${tail ? `: ${tail}` : ""}`));
  }

  // fail marks the session failed; its worktree and branch stay (the branch is already pushed per turn).
  async fail(err: unknown): Promise<void> {
    if (this.phase === "failed" || this.phase === "ended") return;
    this.phase = "failed";
    this.log("error", "session failed", { err: errText(err) });
    await this.stopAgent();
    try {
      await this.worktree?.pushPending();
    } catch {
      // the branch keeps what earlier turns pushed
    }
    await this.report({ state: { state: "failed", busy: false, turn: this.turn, error: errText(err).slice(0, 20000) } });
    await this.cleanup(false);
    this.deps.onEnded?.(this.id);
  }

  // cleanup removes a finished session's directories, or keeps a failed one's worktree (handed back to the host
  // user so the uid can serve another session).
  private async cleanup(remove: boolean): Promise<void> {
    if (this.dirs) {
      if (remove) await removeDirs(this.dirs).catch(() => undefined);
      else if (this.user) await own(this.dirs.root, { uid: process.getuid?.() ?? 0, gid: process.getgid?.() ?? 0 }).catch(() => undefined);
    }
    this.deps.uids?.release(this.id);
  }

  // ------------------------------------------------------------------ reporting

  private notice(text: string, level: "info" | "warning" | "error" = "info"): void {
    this.noticeSeq++;
    this.transcript.notice(`n${this.deps.clock.now()}-${this.noticeSeq}`, text, level);
    this.scheduleFlush();
  }

  private scheduleFlush(): void {
    if (this.flushTimer) return;
    this.flushTimer = setTimeout(() => {
      this.flushTimer = undefined;
      void this.report({});
    }, this.deps.flushMs ?? 300);
    this.flushTimer.unref?.();
  }

  // report sends the pending entries with extra (state, use, note…) in order; a failed send is retried a few times
  // and its entries go back to the queue.
  report(extra: Partial<HostReport>): Promise<void> {
    const run = async (): Promise<void> => {
      if (this.flushTimer) {
        clearTimeout(this.flushTimer);
        this.flushTimer = undefined;
      }
      const entries = this.transcript.take();
      const withdraw = this.withdrawn.splice(0);
      const body: HostReport = { hostId: this.deps.hostId, ...extra };
      if (entries.length) body.entries = entries;
      if (withdraw.length) body.withdraw = withdraw;
      if (Object.keys(body).length === 1) return;
      for (let attempt = 0; ; attempt++) {
        try {
          await this.deps.cp.report(this.id, body);
          break;
        } catch (err) {
          const permanent = err instanceof ApiError && err.permanent;
          if (permanent || attempt >= 5) {
            this.log("error", "report failed", { err: errText(err), attempt });
            if (!permanent) this.transcript.restore(entries);
            return;
          }
          await new Promise((r) => setTimeout(r, Math.min(10_000, 250 * 2 ** attempt)));
        }
      }
      if (this.transcript.pendingCount > 0) this.scheduleFlush();
    };
    this.chain = this.chain.then(run, run);
    return this.chain;
  }

  // idle resolves once every report queued so far went out (tests).
  async idle(): Promise<void> {
    await this.report({});
  }

  // detach stops the agent without ending the session (host shutdown): the next host resumes it.
  async detach(): Promise<void> {
    await this.stopAgent();
  }
}

function pauseNote(r: AgentPauseReason): string {
  switch (r.code) {
    case "runaway":
      return `Paused as a runaway: ${r.message}. Next time, read the error's help article instead of retrying the same call.`;
    case "stuck_turn":
      return `Paused a stuck turn: ${r.message}.`;
    case "turn_tokens":
    case "budget_turns":
    case "budget_tokens":
    case "project_tokens":
      return `Paused on the budget: ${r.message}.`;
    default:
      return `Paused (${r.code}): ${r.message}.`;
  }
}

export type { HostEntry };
