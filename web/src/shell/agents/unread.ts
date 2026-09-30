import { create } from "zustand";
import type { AgentSession } from "@/api/gen/types.gen";
import { isAsleep } from "./labels";

// Chats with news the reader has not seen: the agent finished a turn or the session wants attention (an approval,
// a pause, a failure, its end) while no visible Chat showed it. The Chat's tab carries a dot until one does.

type UnreadState = { unread: Readonly<Record<string, true>> };

export const useUnread = create<UnreadState>(() => ({ unread: {} }));

// Visible Chats per session (a session may be open in more than one Chat).
const viewing = new Map<string, number>();

const ATTENTION = new Set<AgentSession["state"]>(["waiting_approval", "paused", "failed", "done"]);

/** Whether a session change is news for its Chat. Pure: the subscription and the tests share it. */
export function isNews(prev: AgentSession | undefined, next: AgentSession): boolean {
  if (!prev || prev.rev >= next.rev) return false;
  if (prev.busy && !next.busy) return true;
  return prev.state !== next.state && ATTENTION.has(next.state) && !isAsleep(next); // falling asleep is not news
}

function seen(id: string): void {
  if (!useUnread.getState().unread[id]) return;
  useUnread.setState((s) => {
    const unread = { ...s.unread };
    delete unread[id];
    return { unread };
  });
}

/** Marks a session unread unless a Chat shows it to someone looking at the page. */
export function noteNews(id: string): void {
  if ((viewing.get(id) ?? 0) > 0 && document.visibilityState === "visible") return;
  if (!useUnread.getState().unread[id]) useUnread.setState((s) => ({ unread: { ...s.unread, [id]: true } }));
}

/** A Chat shows the session while it is visible: its news is read now and when the page is looked at again. */
export function viewSession(id: string): () => void {
  viewing.set(id, (viewing.get(id) ?? 0) + 1);
  if (document.visibilityState === "visible") seen(id);
  const onVisible = () => document.visibilityState === "visible" && seen(id);
  document.addEventListener("visibilitychange", onVisible);
  return () => {
    document.removeEventListener("visibilitychange", onVisible);
    const n = (viewing.get(id) ?? 1) - 1;
    if (n > 0) viewing.set(id, n);
    else viewing.delete(id);
  };
}
