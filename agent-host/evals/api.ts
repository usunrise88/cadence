// The harness's side of the Cadence API: signed in as the admin (session cookie + the CSRF header the SPA sends),
// it creates the fixture project, starts and ends sessions, and reads what the graders look at. Types come from the
// contract (src/api/gen).

import { randomUUID } from "node:crypto";
import type {
  AgentMessage,
  AgentMessageList,
  AgentSession,
  AgentSessionNew,
  Alias,
  Approval,
  ApprovalList,
  AuditEntry,
  AuditList,
  Draft,
  DraftList,
  Job,
  Mix,
  MixList,
  Project,
} from "../src/api/gen/types.gen.ts";

export class CadenceError extends Error {
  constructor(
    readonly status: number,
    readonly body: string,
    what: string,
  ) {
    super(`${what} answered ${status}: ${body.slice(0, 400)}`);
    this.name = "CadenceError";
  }
}

export class CadenceApi {
  private cookie = "";

  constructor(readonly baseUrl: string) {}

  // req throws on every error status except 404 when missingOk (a read of something that may not exist).
  private async req<T>(method: string, path: string, body?: unknown, headers: Record<string, string> = {}, missingOk = false): Promise<{ status: number; data: T }> {
    const h: Record<string, string> = { Accept: "application/json", "Cadence-Client": "web", ...headers };
    if (this.cookie) h.Cookie = this.cookie;
    if (body !== undefined) h["Content-Type"] = "application/json";
    if (method !== "GET" && !h["Idempotency-Key"]) h["Idempotency-Key"] = `evals-${randomUUID()}`;
    const res = await fetch(new URL(`/api${path}`, this.baseUrl), { method, headers: h, body: body === undefined ? null : JSON.stringify(body) });
    const text = await res.text();
    const cookie = res.headers.getSetCookie().find((c) => c.startsWith("cadence_session="));
    if (cookie) this.cookie = cookie.split(";")[0]!;
    if (!res.ok && !(missingOk && res.status === 404)) throw new CadenceError(res.status, text, `${method} ${path}`);
    return { status: res.status, data: (text ? JSON.parse(text) : undefined) as T };
  }

  private async get<T>(path: string): Promise<T> {
    return (await this.req<T>("GET", path)).data;
  }

  /** First start (auth.setup) on a fresh database, else auth.login. */
  async signIn(username: string, password: string): Promise<void> {
    const status = await this.get<{ setupRequired?: boolean }>("/auth");
    if (status.setupRequired) await this.req("POST", "/auth:setup", { username, password });
    else await this.req("POST", "/auth:login", { username, password });
  }

  /** projects.new, then waits for its bootstrap job (the internal repository, the agent profile). */
  async newProject(slug: string, name: string): Promise<Project> {
    const r = await this.req<{ jobId: string }>("POST", "/projects", { slug, name, locales: ["he-IL"], domain: "telephony" });
    const end = Date.now() + 120_000;
    for (;;) {
      const job = await this.get<Job>(`/jobs/${r.data.jobId}:wait?timeout=10`);
      if (job.state === "done") break;
      if (job.state === "failed" || job.state === "cancelled") throw new Error(`bootstrap of ${slug} ${job.state}: ${job.error ?? ""}`);
      if (Date.now() > end) throw new Error(`bootstrap of ${slug} did not finish in 120 s`);
    }
    return this.get<Project>(`/projects/${slug}`);
  }

  async newMix(project: string, mix: { name: string; datasets: string[]; temperature?: number }): Promise<Mix> {
    const body: Record<string, unknown> = { name: mix.name, groups: [{ name: "target", datasets: mix.datasets }] };
    if (mix.temperature !== undefined) body.temperature = mix.temperature;
    return (await this.req<Mix>("POST", `/projects/${project}/mixes`, body)).data;
  }

  async mixes(project: string): Promise<Mix[]> {
    return (await this.get<MixList>(`/projects/${project}/mixes`)).items;
  }

  async drafts(mixId: string): Promise<Draft[]> {
    return (await this.get<DraftList>(`/drafts?entityKind=mix&entityId=${encodeURIComponent(mixId)}&state=all`)).items;
  }

  /** null when the alias does not exist. */
  async alias(project: string, name: string): Promise<Alias | null> {
    const r = await this.req<Alias>("GET", `/projects/${project}/aliases/${name}`, undefined, {}, true);
    return r.status === 404 ? null : r.data;
  }

  async newSession(project: string, body: AgentSessionNew): Promise<AgentSession> {
    return (await this.req<AgentSession>("POST", `/projects/${project}/agent-sessions`, body)).data;
  }

  async session(id: string): Promise<AgentSession> {
    return this.get<AgentSession>(`/agent-sessions/${id}`);
  }

  /** agentSessions.cancel {end: true}: the host ends the agent, the branch merges per the auto-merge policy. */
  async endSession(s: AgentSession): Promise<void> {
    await this.req("POST", `/agent-sessions/${s.id}:cancel`, { end: true }, { "If-Match": `"${s.rev}"` });
  }

  async transcript(id: string): Promise<AgentMessage[]> {
    const out: AgentMessage[] = [];
    let after = 0;
    for (;;) {
      const page = await this.get<AgentMessageList>(`/agent-sessions/${id}/agent-messages?after=${after}&limit=500`);
      out.push(...page.items);
      if (page.next === undefined || page.items.length === 0) return out;
      after = page.next;
    }
  }

  /** Every audit row of the project, oldest first. */
  async audit(project: string): Promise<AuditEntry[]> {
    const out: AuditEntry[] = [];
    let before = "";
    for (;;) {
      const page = await this.get<AuditList>(`/audit?project=${project}&limit=500${before ? `&before=${before}` : ""}`);
      out.push(...page.items);
      if (!page.next) return out.reverse();
      before = page.next;
    }
  }

  async approvals(project: string): Promise<Approval[]> {
    return (await this.get<ApprovalList>(`/approvals?project=${project}&limit=500`)).items;
  }
}
