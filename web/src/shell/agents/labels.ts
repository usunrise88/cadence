import { useSyncExternalStore } from "react";
import type { AgentSession } from "@/api/gen/types.gen";

// Sessions the UI has seen, so an agent's attribution badge can name its session the way people know it
// ("claude-code · session 3") instead of by id. Fed by every session read and event (shell/agents/sessions.ts).

const known = new Map<string, AgentSession>();
const listeners = new Set<() => void>();
let version = 0;

/** Badges re-render when labels change (a session becomes known, the resolver is installed). */
export function notifyLabels(): void {
  version++;
  for (const fn of listeners) fn();
}

export function rememberSessions(list: AgentSession[]): void {
  let changed = false;
  for (const s of list) {
    const old = known.get(s.id);
    if (old && old.rev >= s.rev) continue;
    known.set(s.id, s);
    changed = true;
  }
  if (changed) notifyLabels();
}

export function knownSession(id: string | undefined): AgentSession | undefined {
  return id ? known.get(id) : undefined;
}

export function useLabelsVersion(): number {
  return useSyncExternalStore(
    (fn) => {
      listeners.add(fn);
      return () => listeners.delete(fn);
    },
    () => version,
  );
}

/** "claude-code · session 3". */
export function sessionLabel(s: Pick<AgentSession, "driver" | "number">): string {
  return `${s.driver} · session ${s.number}`;
}
