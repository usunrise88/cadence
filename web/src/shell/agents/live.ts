import { useEffect } from "react";
import { useQueryClient, type QueryClient } from "@tanstack/react-query";
import type { AgentSession } from "@/api/gen/types.gen";
import { announce, notify, type Notice } from "@/shell/notifications/store";
import { events } from "@/shell/registries";
import { isAsleep, sessionLabel } from "./labels";
import { isNews, noteNews } from "./unread";
import { patchAgentBatch, splitBatch, SESSIONS_TOPIC, transcriptKey, useAgentSessions, type Transcript } from "./sessions";

// The chrome's own agent subscription (not a panel's): keeps every session list and header live, names sessions
// for attribution badges, and tells assistive technology what agents did (docs/spec/10-ui-shell.md, WCAG 4.1.3):
// the finished turn is announced in the polite live region; streamed tokens never are. Pauses and failures also
// land in the notification history. A turn finished or a session wanting attention marks its Chat unread.

type NoticeInput = Omit<Notice, "id" | "at" | "read">;
export type Transition = { announce?: string; notice?: NoticeInput };

const SESSIONS = { label: "Open Agent sessions", panel: "agent-sessions" };

/** What a session change is worth saying. Pure: the subscription and the tests share it. */
export function sessionTransition(prev: AgentSession | undefined, next: AgentSession, lastReply?: string): Transition {
  const who = sessionLabel(next);
  if (!prev) return next.state === "created" ? { announce: `${who} started` } : {};
  if (prev.rev >= next.rev) return {};
  if (prev.state !== next.state) {
    switch (next.state) {
      case "paused":
        // Idleness is routine, not news: said once, quietly; the next message wakes the session.
        if (isAsleep(next)) return { announce: `${who} is asleep (${next.pauseReason?.message ?? "idle"}); your next message wakes it` };
        return {
          announce: `${who} paused: ${next.pauseReason?.message ?? "paused"}`,
          notice: { level: "warning", title: `${who} paused`, detail: next.pauseReason?.message, open: SESSIONS },
        };
      case "failed":
        return {
          announce: `${who} failed${next.error ? `: ${next.error}` : ""}`,
          notice: { level: "error", title: `${who} failed`, detail: next.error, open: SESSIONS },
        };
      case "done":
        return {
          announce: `${who} ended${next.merge.state === "pending" ? "; its changes wait for you to accept or discard" : ""}`,
          ...(next.merge.state === "pending" || next.merge.state === "conflict"
            ? { notice: { level: "info" as const, title: `${who} ended with session changes`, detail: next.merge.state === "conflict" ? "Merging would conflict; review in Chat." : "Accept or discard them in Chat.", open: SESSIONS } }
            : {}),
        };
      case "cancelled":
        return { announce: `${who} was cancelled` };
      case "waiting_approval":
        return { announce: `${who} waits for your approval` };
    }
  }
  const live = next.state === "running" || next.state === "waiting_approval";
  if (live && prev.hostState !== next.hostState && (next.hostState === "released" || next.hostState === "lost")) {
    // The turn did not finish: its host restarted or went silent.
    return {
      announce: next.hostState === "released" ? `${who}: the agent host is restarting — reconnecting` : `${who}: the agent host stopped answering`,
    };
  }
  if (prev.busy && !next.busy && live) {
    const reply = lastReply ? `: ${lastReply.length > 200 ? `${lastReply.slice(0, 200)}…` : lastReply}` : "";
    return { announce: `${who} finished its turn${reply}` };
  }
  return {};
}

function lastReplyOf(qc: QueryClient, sessionId: string): string | undefined {
  const t = qc.getQueryData<Transcript>(transcriptKey(sessionId));
  if (!t) return undefined;
  for (let i = t.items.length - 1; i >= 0; i--) {
    const m = t.items[i]!;
    if (m.kind === "agent_message" && m.text) return m.text.replace(/\s+/g, " ").trim();
    if (m.kind === "user_message") return undefined;
  }
  return undefined;
}

// Transitions are judged against what this subscription saw last (seeded from the project's session list), not
// against the caches a Chat may already have patched with the same event.
const lastSeen = new Map<string, AgentSession>();

export function useAgentLive(project: string | undefined): void {
  const qc = useQueryClient();
  const list = useAgentSessions(project);
  useEffect(() => {
    for (const s of list.data?.items ?? []) {
      const prev = lastSeen.get(s.id);
      if (!prev || prev.rev < s.rev) lastSeen.set(s.id, s);
    }
  }, [list.data]);
  useEffect(
    () =>
      events.subscribe(
        [SESSIONS_TOPIC],
        (batch) => {
          patchAgentBatch(qc, batch);
          for (const s of splitBatch(batch).sessions) {
            const prev = lastSeen.get(s.id);
            if (prev && prev.rev >= s.rev) continue;
            lastSeen.set(s.id, s);
            if (isNews(prev, s)) noteNews(s.id);
            const t = sessionTransition(prev, s, lastReplyOf(qc, s.id));
            if (t.notice) notify(t.notice);
            if (t.announce) announce(t.announce);
          }
        },
        "shell",
      ),
    [qc],
  );
}
