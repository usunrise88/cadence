// The one ACP client of the agent host: JSON-RPC over any `acp.Stream` using @agentclientprotocol/sdk.
//
// Client-side methods it serves: `session/update` (to the host's listener), `session/request_permission` (to the
// host's permission callback; answered `cancelled` when the turn is cancelled, as ACP requires) and
// `fs/read_text_file` / `fs/write_text_file` confined to the session's cwd and additional directories. It does not
// offer terminals: agents run shell commands themselves, inside their own sandbox.

import * as acp from "@agentclientprotocol/sdk";
import { type FileOwner, OutsideWorkspaceError, WorkspaceFs } from "./fs.ts";

export interface ClientHandlers {
  onUpdate: (notification: acp.SessionNotification) => void;
  requestPermission: (
    request: acp.RequestPermissionRequest,
    signal: AbortSignal,
  ) => Promise<acp.RequestPermissionResponse>;
  // Files the client writes for the agent belong to this user (the agent's session user).
  fileOwner?: FileOwner;
}

export const CLIENT_INFO = { name: "cadence-agent-host", title: "Cadence agent host", version: "0.1.0" } as const;

const CANCELLED: acp.RequestPermissionResponse = { outcome: { outcome: "cancelled" } };

export class AcpClient {
  private readonly fsBySession = new Map<string, WorkspaceFs>();
  private readonly turnAborts = new Map<string, AbortController>();
  private readonly conn: acp.ClientConnection;

  constructor(stream: acp.Stream, private readonly handlers: ClientHandlers) {
    this.conn = acp
      .client({ name: CLIENT_INFO.name })
      .onNotification(acp.methods.client.session.update, (ctx) => {
        this.handlers.onUpdate(ctx.params);
      })
      .onRequest(acp.methods.client.session.requestPermission, (ctx) => this.permission(ctx.params, ctx.signal))
      .onRequest(acp.methods.client.fs.readTextFile, async (ctx) => {
        const fs = this.fsFor(ctx.params.sessionId);
        return { content: await guard(() => fs.read(ctx.params.path, ctx.params.line, ctx.params.limit)) };
      })
      .onRequest(acp.methods.client.fs.writeTextFile, async (ctx) => {
        const fs = this.fsFor(ctx.params.sessionId);
        await guard(() => fs.write(ctx.params.path, ctx.params.content));
        return {};
      })
      .connect(stream);
  }

  get closed(): Promise<void> {
    return this.conn.closed;
  }

  initialize(): Promise<acp.InitializeResponse> {
    return this.conn.agent.request(acp.methods.agent.initialize, {
      protocolVersion: acp.PROTOCOL_VERSION,
      clientCapabilities: { fs: { readTextFile: true, writeTextFile: true }, terminal: false },
      clientInfo: CLIENT_INFO,
    });
  }

  async newSession(req: acp.NewSessionRequest): Promise<acp.NewSessionResponse> {
    const fs = await WorkspaceFs.create([req.cwd, ...(req.additionalDirectories ?? [])], this.handlers.fileOwner);
    const res = await this.conn.agent.request(acp.methods.agent.session.new, req);
    this.fsBySession.set(res.sessionId, fs);
    return res;
  }

  // session/load replays the history as session/update notifications before it returns.
  async loadSession(req: acp.LoadSessionRequest): Promise<acp.LoadSessionResponse> {
    this.fsBySession.set(req.sessionId, await WorkspaceFs.create([req.cwd, ...(req.additionalDirectories ?? [])], this.handlers.fileOwner));
    return this.conn.agent.request(acp.methods.agent.session.load, req);
  }

  // session/resume restores the context without replaying it.
  async resumeSession(req: acp.ResumeSessionRequest): Promise<acp.ResumeSessionResponse> {
    this.fsBySession.set(req.sessionId, await WorkspaceFs.create([req.cwd, ...(req.additionalDirectories ?? [])], this.handlers.fileOwner));
    return this.conn.agent.request(acp.methods.agent.session.resume, req);
  }

  async prompt(sessionId: string, prompt: acp.ContentBlock[]): Promise<acp.PromptResponse> {
    const abort = new AbortController();
    this.turnAborts.set(sessionId, abort);
    try {
      return await this.conn.agent.request(acp.methods.agent.session.prompt, { sessionId, prompt });
    } finally {
      if (this.turnAborts.get(sessionId) === abort) this.turnAborts.delete(sessionId);
    }
  }

  // Notification; the pending session/prompt then resolves with stopReason "cancelled".
  async cancel(sessionId: string): Promise<void> {
    this.turnAborts.get(sessionId)?.abort();
    await this.conn.agent.notify(acp.methods.agent.session.cancel, { sessionId });
  }

  async closeSession(sessionId: string): Promise<void> {
    await this.conn.agent.request(acp.methods.agent.session.close, { sessionId });
    this.fsBySession.delete(sessionId);
  }

  close(): void {
    for (const a of this.turnAborts.values()) a.abort();
    this.conn.close();
  }

  private fsFor(sessionId: string): WorkspaceFs {
    const fs = this.fsBySession.get(sessionId);
    if (!fs) throw acp.RequestError.invalidParams({ sessionId }, "unknown session");
    return fs;
  }

  private async permission(
    req: acp.RequestPermissionRequest,
    requestSignal: AbortSignal,
  ): Promise<acp.RequestPermissionResponse> {
    const turn = this.turnAborts.get(req.sessionId)?.signal;
    const signal = turn ? AbortSignal.any([turn, requestSignal]) : requestSignal;
    if (signal.aborted) return CANCELLED;
    return new Promise((resolve, reject) => {
      const onAbort = (): void => resolve(CANCELLED);
      signal.addEventListener("abort", onAbort, { once: true });
      this.handlers.requestPermission(req, signal).then(
        (res) => {
          signal.removeEventListener("abort", onAbort);
          resolve(signal.aborted ? CANCELLED : res);
        },
        (err: unknown) => {
          signal.removeEventListener("abort", onAbort);
          reject(err instanceof Error ? err : new Error(String(err)));
        },
      );
    });
  }
}

async function guard<T>(op: () => Promise<T>): Promise<T> {
  try {
    return await op();
  } catch (err) {
    if (err instanceof OutsideWorkspaceError) throw acp.RequestError.invalidParams({ path: err.path }, err.message);
    const e = err as NodeJS.ErrnoException;
    if (e.code === "ENOENT") throw acp.RequestError.resourceNotFound(e.path);
    throw err;
  }
}
