// The control plane as the agent host sees it: the host protocol (docs/spec/05-agents.md; api/openapi.yaml tag
// host), authenticated by the agent host token (cah_…). Types come from the contract (src/api/gen, make gen).

import type {
  AgentSession,
  HostAsk,
  HostCredentialAck,
  HostCredentialClaim,
  HostCredentialReport,
  HostCredentialTask,
  HostCredentialWork,
  HostClaim,
  HostDecision,
  HostRelease,
  HostReleased,
  HostReport,
  HostWork,
  Problem,
} from "../api/gen/types.gen.ts";

export type { AgentSession, HostAsk, HostClaim, HostDecision, HostRelease, HostReleased, HostReport, HostWork };
export type { HostCredentialAck, HostCredentialClaim, HostCredentialReport, HostCredentialTask, HostCredentialWork };
export type {
  AgentPauseReason,
  AgentToolCall,
  AgentUse,
  HostControl,
  HostEntry,
  HostMessage,
  HostStart,
} from "../api/gen/types.gen.ts";

export interface ControlPlane {
  claim(req: HostClaim, signal?: AbortSignal): Promise<HostWork>;
  report(sessionId: string, body: HostReport): Promise<AgentSession>;
  ask(sessionId: string, body: HostAsk): Promise<HostDecision>;
  // release gives every session of a host that shuts down back to the control plane (the next host takes them).
  release(body: HostRelease): Promise<HostReleased>;
}

// The agent-credential side of the host protocol (hostCredentials.claim|report): values to write into the
// agent-credentials volume, removals and verifications.
export interface CredentialPlane {
  claimCredentials(req: HostCredentialClaim, signal?: AbortSignal): Promise<HostCredentialWork>;
  reportCredential(taskId: string, body: HostCredentialReport): Promise<HostCredentialAck>;
}

export class ApiError extends Error {
  constructor(
    readonly status: number,
    readonly problem: Partial<Problem>,
  ) {
    super(`control plane answered ${status}: ${problem.title ?? ""} ${problem.detail ?? ""}`.trim());
    this.name = "ApiError";
  }

  // A 4xx other than 408/429 will not get better by retrying.
  get permanent(): boolean {
    return this.status >= 400 && this.status < 500 && this.status !== 408 && this.status !== 429;
  }
}

export class HttpControlPlane implements ControlPlane, CredentialPlane {
  constructor(
    private readonly baseUrl: string, // e.g. http://control-plane:8080
    private readonly token: string,
  ) {}

  private async call<T>(method: string, path: string, body: unknown, signal?: AbortSignal): Promise<T> {
    const init: RequestInit = {
      method,
      headers: { Authorization: `Bearer ${this.token}`, "Content-Type": "application/json", Accept: "application/json" },
      body: body === undefined ? null : JSON.stringify(body),
    };
    if (signal) init.signal = signal;
    const res = await fetch(new URL(`/api${path}`, this.baseUrl), init);
    const text = await res.text();
    if (!res.ok) {
      let problem: Partial<Problem> = { detail: text.slice(0, 500) };
      try {
        problem = JSON.parse(text) as Problem;
      } catch {
        // not problem+json
      }
      throw new ApiError(res.status, problem);
    }
    return JSON.parse(text) as T;
  }

  claim(req: HostClaim, signal?: AbortSignal): Promise<HostWork> {
    return this.call("POST", "/host-sessions:claim", req, signal);
  }

  report(sessionId: string, body: HostReport): Promise<AgentSession> {
    return this.call("POST", `/host-sessions/${encodeURIComponent(sessionId)}:report`, body);
  }

  ask(sessionId: string, body: HostAsk): Promise<HostDecision> {
    return this.call("POST", `/host-sessions/${encodeURIComponent(sessionId)}:ask`, body);
  }

  claimCredentials(req: HostCredentialClaim, signal?: AbortSignal): Promise<HostCredentialWork> {
    return this.call("POST", "/host-credentials:claim", req, signal);
  }

  reportCredential(taskId: string, body: HostCredentialReport): Promise<HostCredentialAck> {
    return this.call("POST", `/host-credentials/${encodeURIComponent(taskId)}:report`, body);
  }

  release(body: HostRelease): Promise<HostReleased> {
    return this.call("POST", "/host-sessions:release", body);
  }
}
