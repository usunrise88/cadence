import { useEffect } from "react";
import { useQuery, useQueryClient, type QueryClient } from "@tanstack/react-query";
import { agentMessagesList } from "@/api/gen/sdk.gen";
import { agentMessagesListQueryKey, agentSessionsGetOptions, agentSessionsGetQueryKey, agentSessionsListOptions } from "@/api/gen/@tanstack/react-query.gen";
import type { AgentMessage, AgentSession, AgentSessionList, CadenceEvent } from "@/api/gen/types.gen";
import { rememberSessions } from "./labels";

// Agent sessions and transcripts in the query cache (docs/spec/05-agents.md "Entities and contract"). Events carry
// the whole session or transcript entry, so caches are patched in place: `agent.sessions` keeps the lists live,
// `agent.session.{id}` one session's header and transcript. The event stream already delivers one batch per
// animation frame; a batch is merged in one cache write, so a burst of streamed updates re-renders once.

export const SESSIONS_TOPIC = "agent.sessions";
export const sessionTopic = (id: string) => `agent.session.${id}`;

/** Live states: the session may still take messages or be resumed. */
export const LIVE_STATES = new Set(["created", "running", "waiting_approval", "paused"]);
export const isLive = (s: Pick<AgentSession, "state">) => LIVE_STATES.has(s.state);

// ---------------------------------------------------------------- transcript merge (pure)

/**
 * Merges changed transcript entries into a transcript ordered by seq: a newer revision replaces the entry, an older
 * one is ignored, a new entry is inserted in order. Several updates of one entry in the same batch collapse to the
 * newest. Returns the same array when nothing changed.
 */
export function mergeMessages(list: AgentMessage[], changed: AgentMessage[]): AgentMessage[] {
  if (changed.length === 0) return list;
  const latest = new Map<string, AgentMessage>();
  for (const m of changed) {
    const prev = latest.get(m.id);
    if (!prev || prev.rev <= m.rev) latest.set(m.id, m);
  }
  const index = new Map(list.map((m, i) => [m.id, i]));
  let out: AgentMessage[] | null = null;
  const appended: AgentMessage[] = [];
  for (const m of latest.values()) {
    const i = index.get(m.id);
    if (i === undefined) {
      appended.push(m);
      continue;
    }
    if (list[i]!.rev >= m.rev) continue;
    out ??= list.slice();
    out[i] = m;
  }
  if (appended.length === 0) return out ?? list;
  out ??= list.slice();
  const lastSeq = out.length ? out[out.length - 1]!.seq : -Infinity;
  appended.sort((a, b) => a.seq - b.seq);
  if (appended[0]!.seq > lastSeq) return out.concat(appended);
  return out.concat(appended).sort((a, b) => a.seq - b.seq);
}

/** A session list with changed sessions merged in (newer revisions win; new sessions first). */
export function mergeSessions(list: AgentSessionList, changed: AgentSession[]): AgentSessionList {
  const byId = new Map(list.items.map((s) => [s.id, s]));
  const added: AgentSession[] = [];
  let touched = false;
  for (const s of changed) {
    const old = byId.get(s.id);
    if (old && old.rev >= s.rev) continue;
    if (!old) added.push(s);
    byId.set(s.id, s);
    touched = true;
  }
  if (!touched) return list;
  const items = list.items.map((s) => byId.get(s.id)!);
  return { ...list, items: [...added.reverse(), ...items] };
}

export type Transcript = { items: AgentMessage[] };

export function transcriptKey(sessionId: string) {
  return agentMessagesListQueryKey({ path: { id: sessionId } });
}

type EventPayload = { message?: AgentMessage; session?: AgentSession };

/** Splits a batch into the sessions and transcript entries it carries. */
export function splitBatch(batch: CadenceEvent[]): { sessions: AgentSession[]; messages: AgentMessage[] } {
  const sessions: AgentSession[] = [];
  const messages: AgentMessage[] = [];
  for (const e of batch) {
    const p = (e.payload ?? {}) as EventPayload;
    if (e.type.startsWith("agent_message.") && p.message) messages.push(p.message);
    else if (e.type.startsWith("agent_session.") && p.session) sessions.push(p.session);
  }
  return { sessions, messages };
}

/** Patches the session caches (get and every list of its project) with changed sessions. */
export function patchSessions(qc: QueryClient, sessions: AgentSession[]): void {
  if (sessions.length === 0) return;
  rememberSessions(sessions);
  for (const s of sessions) {
    const key = agentSessionsGetQueryKey({ path: { id: s.id } });
    const old = qc.getQueryData<AgentSession>(key);
    if (!old || old.rev < s.rev) qc.setQueryData(key, s);
  }
  const lists = qc.getQueryCache().findAll({ predicate: (q) => (q.queryKey[0] as { _id?: string } | undefined)?._id === "agentSessionsList" });
  for (const q of lists) {
    const k = q.queryKey[0] as { path?: { p?: string }; query?: { state?: string } };
    const old = q.state.data as AgentSessionList | undefined;
    if (!old) continue;
    const mine = sessions.filter((s) => s.project === k.path?.p);
    if (mine.length === 0) continue;
    if (k.query?.state) {
      void qc.invalidateQueries({ queryKey: q.queryKey, exact: true });
      continue;
    }
    qc.setQueryData(q.queryKey, mergeSessions(old, mine));
  }
}

/** Applies one batch of `agent.session.{id}` / `agent.sessions` events: one cache write per transcript. */
export function patchAgentBatch(qc: QueryClient, batch: CadenceEvent[]): void {
  const { sessions, messages } = splitBatch(batch);
  patchSessions(qc, sessions);
  const bySession = new Map<string, AgentMessage[]>();
  for (const m of messages) {
    const list = bySession.get(m.sessionId) ?? [];
    list.push(m);
    bySession.set(m.sessionId, list);
  }
  for (const [id, changed] of bySession) {
    const key = transcriptKey(id);
    const old = qc.getQueryData<Transcript>(key);
    if (!old) continue; // not loaded: the first read brings everything
    const items = mergeMessages(old.items, changed);
    if (items !== old.items) qc.setQueryData<Transcript>(key, { items });
  }
}

/** Reads a whole transcript, page after page (`?after=<seq>`). */
export async function loadTranscript(sessionId: string, signal?: AbortSignal): Promise<Transcript> {
  let items: AgentMessage[] = [];
  let after: number | undefined;
  for (let page = 0; page < 200; page++) {
    const { data } = await agentMessagesList({ path: { id: sessionId }, query: { ...(after !== undefined ? { after } : {}), limit: 500 }, signal, throwOnError: true });
    items = mergeMessages(items, data.items);
    if (data.next === undefined || data.items.length === 0) break;
    after = data.next;
  }
  return { items };
}

/** One session's transcript, kept live by the Chat panel's subscription. */
export function useTranscript(sessionId: string | undefined) {
  return useQuery({
    queryKey: transcriptKey(sessionId ?? ""),
    queryFn: ({ signal }) => loadTranscript(sessionId!, signal),
    enabled: !!sessionId,
    staleTime: Infinity,
  });
}

/** One session (header, budget, merge state). */
export function useAgentSession(sessionId: string | undefined) {
  const q = useQuery({ ...agentSessionsGetOptions({ path: { id: sessionId ?? "" } }), enabled: !!sessionId });
  useEffect(() => {
    if (q.data) rememberSessions([q.data]);
  }, [q.data]);
  return q;
}

/** The project's sessions, live ones first (agentSessions.list). */
export function useAgentSessions(project: string | undefined) {
  const q = useQuery({ ...agentSessionsListOptions({ path: { p: project ?? "" }, query: { limit: 200 } }), enabled: !!project });
  useEffect(() => {
    if (q.data) rememberSessions(q.data.items);
  }, [q.data]);
  return q;
}

/** Hook form of the patcher for a panel's useTopic handler. */
export function useAgentPatcher(): (batch: CadenceEvent[]) => void {
  const qc = useQueryClient();
  return (batch) => patchAgentBatch(qc, batch);
}
