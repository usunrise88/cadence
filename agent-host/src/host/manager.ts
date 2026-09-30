// The agent host's loop: claim work from the control plane (a long-poll), start the sessions it hands over, and
// route messages, controls and permission decisions to them.

import type { ControlPlane, HostWork } from "./api.ts";
import { ApiError } from "./api.ts";
import { errText, type Logger } from "./log.ts";
import { HostSession, type SessionDeps } from "./session.ts";

export interface ManagerOptions extends Omit<SessionDeps, "onEnded"> {
  capacity: number;
  version: string;
  waitSeconds?: number;
  // How long shutdown waits for the agents to stop before it releases the sessions anyway (default 20 s).
  shutdownMs?: number;
}

export class SessionManager {
  readonly sessions = new Map<string, HostSession>();

  constructor(private readonly o: ManagerOptions) {}

  private get cp(): ControlPlane {
    return this.o.cp;
  }

  // run claims work until signal aborts.
  async run(signal: AbortSignal): Promise<void> {
    let backoff = 1000;
    while (!signal.aborted) {
      try {
        const work = await this.cp.claim(
          {
            hostId: this.o.hostId,
            version: this.o.version,
            wait: this.o.waitSeconds ?? 20,
            capacity: Math.max(0, this.o.capacity - this.live()),
          },
          signal,
        );
        backoff = 1000;
        this.dispatch(work);
      } catch (err) {
        if (signal.aborted) break;
        const level = err instanceof ApiError && err.status === 401 ? "error" : "warn";
        this.o.log.log(level, "claim failed", { err: errText(err), retryInMs: backoff });
        await new Promise((r) => setTimeout(r, backoff));
        backoff = Math.min(30_000, backoff * 2);
      }
    }
  }

  private live(): number {
    let n = 0;
    for (const s of this.sessions.values()) if (s.state !== "paused") n++;
    return n;
  }

  // dispatch hands one claim's work to the sessions; starts run in the background.
  dispatch(work: HostWork): void {
    for (const st of work.start ?? []) {
      if (this.sessions.has(st.session.id)) continue;
      const s = new HostSession({ ...this.o, onEnded: (id) => this.sessions.delete(id) }, st);
      this.sessions.set(s.id, s);
      this.o.log.log("info", "session start", { session: s.id, driver: st.session.driver, kind: st.session.kind });
      void s.begin();
    }
    for (const m of work.messages ?? []) this.session(m.sessionId)?.enqueue(m);
    for (const c of work.controls ?? []) void this.session(c.sessionId)?.control(c);
    for (const d of work.decisions ?? []) if (d.sessionId) this.session(d.sessionId)?.decision(d);
  }

  private session(id: string): HostSession | undefined {
    const s = this.sessions.get(id);
    if (!s) this.o.log.log("warn", "work for a session this host does not run", { session: id });
    return s;
  }

  // shutdown stops every agent — a turn in progress ends there, its changes committed and reported — and releases
  // the sessions (hostSessions.release): the next host takes them at once instead of after the host lapse, and the
  // messages no agent read yet go back to it. The release goes out even when an agent is slow to stop.
  async shutdown(): Promise<void> {
    const sessions = [...this.sessions.values()];
    let timer: NodeJS.Timeout | undefined;
    const deadline = new Promise<void>((r) => {
      timer = setTimeout(r, this.o.shutdownMs ?? 20_000);
      timer.unref?.();
    });
    await Promise.race([Promise.all(sessions.map((s) => s.detach())), deadline]);
    clearTimeout(timer);
    const messages = sessions.flatMap((s) => s.unread());
    try {
      const r = await this.cp.release({ hostId: this.o.hostId, ...(messages.length ? { messages } : {}) });
      this.o.log.log("info", "sessions released", { sessions: r.released.length, messages: messages.length });
    } catch (err) {
      this.o.log.log("warn", "release failed; the sessions move to the next host after the lapse", { err: errText(err) });
    }
    for (const s of sessions) this.sessions.delete(s.id);
  }
}
