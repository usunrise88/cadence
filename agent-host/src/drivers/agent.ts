// A running agent behind a driver: launch → initialize → sessions → turns, emitting HostUpdates.
// No agent-specific code here: differences live in the Driver; capabilities come from `initialize`.

import type * as acp from "@agentclientprotocol/sdk";
import { AcpClient } from "../acp/client.ts";
import { type AgentProcess, launch, type MessageTap } from "../acp/transport.ts";
import { UpdateNormalizer } from "./normalize.ts";
import type {
  Driver,
  HostUpdate,
  LaunchOptions,
  PermissionDecision,
  PermissionHandler,
  PermissionOptionKind,
  TurnUsage,
} from "./types.ts";

export interface StartOptions extends LaunchOptions {
  onPermission: PermissionHandler;
  tap?: MessageTap;
}

export interface SessionOptions {
  cwd: string;
  mcpServers: acp.McpServer[];
  additionalDirectories?: string[];
}

export interface TurnResult {
  stopReason: acp.StopReason;
  usage?: TurnUsage;
}

export interface AgentCapabilities {
  load: boolean;
  resume: boolean;
  close: boolean;
  mcpHttp: boolean;
}

// How a session was restored: `resume` keeps context silently, `load` replays history as updates first.
export type RestoreMode = "resume" | "load";

export class RestoreUnsupportedError extends Error {
  constructor(driver: string) {
    super(`${driver} supports neither session/resume nor session/load; start a new session with a summary`);
    this.name = "RestoreUnsupportedError";
  }
}

const FALLBACK: Record<PermissionOptionKind, PermissionOptionKind> = {
  allow_once: "allow_once",
  allow_always: "allow_once",
  reject_once: "reject_once",
  reject_always: "reject_once",
};

export function pickOption(options: readonly acp.PermissionOption[], d: PermissionDecision): acp.PermissionOption | undefined {
  if (d === "cancelled") return undefined;
  if ("optionId" in d) return options.find((o) => o.optionId === d.optionId);
  return options.find((o) => o.kind === d.select) ?? options.find((o) => o.kind === FALLBACK[d.select]);
}

function turnUsage(u: acp.Usage): TurnUsage {
  const out: TurnUsage = { inputTokens: u.inputTokens, outputTokens: u.outputTokens, totalTokens: u.totalTokens };
  if (u.thoughtTokens != null) out.thoughtTokens = u.thoughtTokens;
  if (u.cachedReadTokens != null) out.cachedReadTokens = u.cachedReadTokens;
  if (u.cachedWriteTokens != null) out.cachedWriteTokens = u.cachedWriteTokens;
  return out;
}

export class Agent {
  private readonly listeners = new Set<(u: HostUpdate) => void>();
  private readonly normalizer: UpdateNormalizer;
  private info?: acp.InitializeResponse;

  private constructor(
    readonly driver: Driver,
    private readonly opts: StartOptions,
    private readonly proc: AgentProcess,
    private readonly client: AcpClient,
    normalizer: UpdateNormalizer,
  ) {
    this.normalizer = normalizer;
  }

  static async start(driver: Driver, opts: StartOptions): Promise<Agent> {
    const spec = driver.launch(opts);
    if (opts.user) Object.assign(spec, { uid: opts.user.uid, gid: opts.user.gid });
    const proc = launch(spec, opts.tap);
    const normalizer = new UpdateNormalizer(driver);
    let self: Agent | undefined;
    const client = new AcpClient(proc.stream, {
      onUpdate: (n) => {
        for (const u of normalizer.apply(n)) self?.emit(u);
      },
      requestPermission: (req, signal) => {
        if (!self) return Promise.resolve({ outcome: { outcome: "cancelled" } });
        return self.permission(req, signal);
      },
      ...(opts.user ? { fileOwner: opts.user } : {}),
    });
    self = new Agent(driver, opts, proc, client, normalizer);
    try {
      self.info = await Promise.race([
        client.initialize(),
        proc.exited.then(({ code, signal }) => {
          throw new Error(`${driver.name} exited during initialize (code ${code}, signal ${signal}): ${proc.stderr().slice(-2000)}`);
        }),
      ]);
    } catch (err) {
      proc.kill();
      throw err;
    }
    return self;
  }

  get initializeResponse(): acp.InitializeResponse {
    if (!this.info) throw new Error("agent not initialized");
    return this.info;
  }

  get capabilities(): AgentCapabilities {
    const c = this.initializeResponse.agentCapabilities;
    return {
      load: c?.loadSession === true,
      resume: c?.sessionCapabilities?.resume != null,
      close: c?.sessionCapabilities?.close != null,
      mcpHttp: c?.mcpCapabilities?.http === true,
    };
  }

  get exited(): AgentProcess["exited"] {
    return this.proc.exited;
  }

  stderr(): string {
    return this.proc.stderr();
  }

  onUpdate(listener: (u: HostUpdate) => void): () => void {
    this.listeners.add(listener);
    return () => this.listeners.delete(listener);
  }

  async newSession(s: SessionOptions): Promise<string> {
    const req: acp.NewSessionRequest = { cwd: s.cwd, mcpServers: s.mcpServers };
    if (s.additionalDirectories) req.additionalDirectories = s.additionalDirectories;
    const meta = this.driver.sessionMeta?.(this.opts);
    if (meta) req._meta = meta;
    return (await this.client.newSession(req)).sessionId;
  }

  // Restores a session created by an earlier process of the same agent: resume when offered, else load.
  async restoreSession(sessionId: string, s: SessionOptions): Promise<RestoreMode> {
    const meta = this.driver.sessionMeta?.(this.opts);
    const base = { sessionId, cwd: s.cwd, mcpServers: s.mcpServers, ...(meta ? { _meta: meta } : {}) };
    if (this.capabilities.resume) {
      await this.client.resumeSession(base);
      return "resume";
    }
    if (this.capabilities.load) {
      await this.client.loadSession(base);
      return "load";
    }
    throw new RestoreUnsupportedError(this.driver.name);
  }

  async prompt(sessionId: string, prompt: string | acp.ContentBlock[]): Promise<TurnResult> {
    const blocks: acp.ContentBlock[] = typeof prompt === "string" ? [{ type: "text", text: prompt }] : prompt;
    this.emit({ kind: "state", sessionId, state: "turn_started" });
    const res = await this.client.prompt(sessionId, blocks);
    const out: TurnResult = { stopReason: res.stopReason };
    if (res.usage) {
      out.usage = turnUsage(res.usage);
      this.emit({ kind: "usage", sessionId, turn: out.usage });
    }
    this.emit({ kind: "state", sessionId, state: "turn_ended", stopReason: res.stopReason });
    return out;
  }

  cancel(sessionId: string): Promise<void> {
    return this.client.cancel(sessionId);
  }

  async close(): Promise<void> {
    this.client.close();
    this.proc.kill();
    const timer = setTimeout(() => this.proc.kill("SIGKILL"), 5000);
    await this.proc.exited;
    clearTimeout(timer);
  }

  private emit(u: HostUpdate): void {
    for (const l of this.listeners) l(u);
  }

  private async permission(req: acp.RequestPermissionRequest, signal: AbortSignal): Promise<acp.RequestPermissionResponse> {
    const sessionId = req.sessionId;
    const call = this.normalizer.foldTool(req.toolCall);
    this.emit({ kind: "permission", sessionId, call, options: req.options });
    const decision = await this.opts.onPermission({ sessionId, call, options: req.options }, signal);
    const option = signal.aborted ? undefined : pickOption(req.options, decision);
    if (!option) {
      this.emit({ kind: "permission_decided", sessionId, toolCallId: call.id, outcome: "cancelled" });
      return { outcome: { outcome: "cancelled" } };
    }
    this.emit({
      kind: "permission_decided",
      sessionId,
      toolCallId: call.id,
      outcome: { optionId: option.optionId, optionKind: option.kind },
    });
    return { outcome: { outcome: "selected", optionId: option.optionId } };
  }
}
