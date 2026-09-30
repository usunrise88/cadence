import type { Actor, Approval } from "@/api/gen/types.gen";
import { knownSession, sessionLabel } from "@/shell/agents/labels";

// Presentation helpers for approval cards and notices.

export function actorLabel(a: Actor | undefined): string {
  if (!a) return "—";
  const name = a.name ?? a.id;
  if (a.kind === "agent") {
    const s = knownSession(a.sessionId);
    if (s) return sessionLabel(s); // "claude-code · session 3", as the Chat and the badges name it
    return a.sessionId ? `Agent · session ${shortId(a.sessionId)}` : `Agent · ${name}`;
  }
  if (a.kind === "automation") return `Automation · ${name}`;
  return name;
}

export function shortId(id: string): string {
  const i = id.indexOf("_");
  const tail = i >= 0 ? id.slice(i + 1) : id;
  return tail.length > 8 ? `${id.slice(0, i + 1)}…${tail.slice(-6)}` : id;
}

/** "PUT /projects/p/aliases/baseline" — what the stored request will do when approved. */
export function requestLine(a: Approval): string {
  return `${a.request.method} ${a.request.path}${a.request.query ? `?${a.request.query}` : ""}`;
}

/** "23 h 12 min", "4 min 10 s", "12 s", or "expired". */
export function countdown(expiresAt: string, now: number): { label: string; urgent: boolean; expired: boolean } {
  const ms = new Date(expiresAt).getTime() - now;
  if (ms <= 0) return { label: "expired", urgent: true, expired: true };
  const s = Math.floor(ms / 1000);
  const h = Math.floor(s / 3600);
  const m = Math.floor((s % 3600) / 60);
  const sec = s % 60;
  const label = h > 0 ? `${h} h ${m} min` : m > 0 ? `${m} min ${sec} s` : `${sec} s`;
  return { label, urgent: ms < 60 * 60 * 1000, expired: false };
}

export function formatGpuHours(v: number | undefined): string {
  if (v === undefined) return "—";
  return `${v >= 10 ? v.toFixed(0) : v.toFixed(1)} GPU-h`;
}

/** The estimate line: "3.0 GPU-h · 5.0 GPU-h left today", or undefined when the command spends nothing. */
export function estimateLine(a: Approval): string | undefined {
  const e = a.estimate;
  if (!e || (e.gpuHours === undefined && e.remainingGpuHours === undefined)) return undefined;
  const parts = [formatGpuHours(e.gpuHours)];
  if (e.remainingGpuHours !== undefined) parts.push(`${formatGpuHours(e.remainingGpuHours)} left today`);
  return parts.join(" · ");
}

export function decisionLine(a: Approval): string {
  if (a.state === "pending") return "Pending";
  const how = a.decision?.expired ? "Expired" : a.state === "approved" ? (a.decision?.grant === "session" ? "Approved for the session" : "Approved once") : "Denied";
  const by = a.decision?.expired ? "" : a.decidedBy ? ` by ${actorLabel(a.decidedBy)}` : "";
  return `${how}${by}`;
}

export function time(iso: string | undefined): string {
  return iso ? new Date(iso).toLocaleString() : "—";
}
