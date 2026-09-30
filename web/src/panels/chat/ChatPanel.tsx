import { useEffect, useLayoutEffect, useMemo, useRef, useState, type KeyboardEvent, type UIEvent } from "react";
import { useQuery } from "@tanstack/react-query";
import { Attachment, Eject, Pause, Play, SendDiagonal, Square } from "iconoir-react";
import { branchesGetOptions } from "@/api/gen/@tanstack/react-query.gen";
import type { AgentMessage, AgentSession } from "@/api/gen/types.gen";
import { Button } from "@/components/ui/button";
import { Kbd } from "@/components/ui/kbd";
import { NativeSelect } from "@/components/ui/native-select";
import { Textarea } from "@/components/ui/textarea";
import { cn } from "@/lib/utils";
import { EmptyState } from "@/shell/entity/primitives";
import {
  currentSelectionReferences,
  errorMessage,
  isLive,
  openBranch,
  openChat,
  pinChat,
  runCommand,
  sessionIdOfDoc,
  sessionLabel,
  sessionTopic,
  useAgentPatcher,
  useAgentSession,
  useAgentSessions,
  useChatBridge,
  useChatDraft,
  useProject,
  useSelection,
  useTopic,
  usePanel,
  useTranscript,
  viewSession,
  type PanelProps,
} from "@/shell/panel";
import { Entry, RefChips } from "./entries";
import { budgetUse, compact, entryMatchesToolCall, rowOffsets, sessionStatus, tabLabel, VIRTUALIZE_AFTER, visibleEntries, visibleRange, type Meter, type Tone } from "./model";

// Chat (docs/spec/11-ui-panels.md "Panel catalogue"; docs/spec/05-agents.md "What the Chat panel shows"): one agent
// session's streaming transcript, its header (kind, state, budget; stop, pause or resume, end), the merge of its
// session changes once it ends, and the composer with the selection attached as references. The workspace's Chat
// is pinned to a session (the workspace remembers it); "Open its Chat" elsewhere opens one per session.

export function ChatEmpty() {
  return <EmptyState step="prepare" title="No agent session" hint="Write a message to start a Claude Code or opencode session, or pick one from Agent sessions." />;
}

export function ChatPanel({ instanceId, doc }: PanelProps) {
  const pinned = useSelection((s) => s.pins[instanceId]);
  const sessionId = sessionIdOfDoc(doc) ?? sessionIdOfDoc(pinned);
  const session = useAgentSession(sessionId);
  const patch = useAgentPatcher();
  useTopic(sessionId ? [sessionTopic(sessionId)] : null, patch);
  const { visible } = usePanel();
  // While this Chat is on screen its session's news counts as read (the tab's dot).
  useEffect(() => {
    if (visible && sessionId) return viewSession(sessionId);
  }, [visible, sessionId]);
  const setLast = useChatBridge((s) => s.setLastChat);
  useEffect(() => {
    if (!useChatBridge.getState().lastChat) setLast(instanceId);
  }, [instanceId, setLast]);

  return (
    <div
      className="cadence-chat flex h-full min-h-0 flex-col"
      onFocusCapture={() => setLast(instanceId)}
      onPointerDownCapture={() => setLast(instanceId)}
      onKeyDown={typeIntoComposer}
      data-chat-session={sessionId}
    >
      {sessionId && session.data ? (
        <>
          <Header session={session.data} instanceId={instanceId} switchable={!doc} />
          <Transcript session={session.data} />
          <MergeArea session={session.data} />
        </>
      ) : sessionId && session.isLoading ? (
        <p className="flex-1 p-3 text-xs text-muted-foreground">Loading the session…</p>
      ) : (
        <NoSession instanceId={instanceId} switchable={!doc} missing={sessionId && session.error ? sessionId : undefined} />
      )}
      <Composer instanceId={instanceId} session={session.data} bound={!!doc} />
    </div>
  );
}

/** Typing anywhere in the Chat outside a field goes to the composer: the key lands in it once it has focus. Space
 * still presses the button or link it is on. */
function typeIntoComposer(e: KeyboardEvent<HTMLDivElement>) {
  if (e.defaultPrevented || e.nativeEvent.isComposing || e.ctrlKey || e.metaKey || e.altKey || e.key.length !== 1) return;
  const t = e.target as HTMLElement;
  if (t.isContentEditable || t.closest("input, textarea, select")) return;
  if (e.key === " " && t.closest("button, a, summary, [role=button]")) return;
  const input = e.currentTarget.querySelector<HTMLTextAreaElement>('[data-slot="composer"] textarea');
  if (!input || input.disabled) return;
  const end = input.value.length;
  input.focus({ preventScroll: true });
  input.setSelectionRange(end, end);
}

// ---------------------------------------------------------------- no session yet

function NoSession({ instanceId, switchable, missing }: { instanceId: string; switchable: boolean; missing?: string }) {
  const project = useProject();
  const list = useAgentSessions(project);
  const live = (list.data?.items ?? []).filter(isLive).slice(0, 6);
  return (
    <div className="flex min-h-0 flex-1 flex-col gap-3 overflow-auto p-4 text-xs">
      {missing ? <p role="alert" className="text-destructive">Session {missing} could not be loaded.</p> : null}
      <div className="h-40">
        <ChatEmpty />
      </div>
      {switchable && live.length ? (
        <section aria-labelledby={`${instanceId}-live`} className="flex flex-col gap-1">
          <h3 id={`${instanceId}-live`} className="text-[11px] font-medium tracking-wide text-muted-foreground uppercase">
            Live sessions
          </h3>
          {live.map((s) => (
            <Button key={s.id} size="xs" variant="outline" className="justify-start" onClick={() => pinChat(instanceId, s.id)}>
              {sessionLabel(s)} · {s.state.replace("_", " ")}
            </Button>
          ))}
        </section>
      ) : null}
    </div>
  );
}

// ---------------------------------------------------------------- header

const TONE: Record<Tone, string> = {
  neutral: "text-muted-foreground",
  running: "text-status-running-foreground",
  done: "text-status-done-foreground",
  warning: "text-status-warning-foreground",
  failed: "text-status-failed-foreground",
};
const DOT: Record<Tone, string> = {
  neutral: "bg-muted-foreground",
  running: "bg-status-running",
  done: "bg-status-done",
  warning: "bg-status-warning",
  failed: "bg-status-failed",
};

function BudgetMeter({ label, m, format }: { label: string; m: Meter; format: (n: number) => string }) {
  const high = m.ratio >= 0.9;
  return (
    <div className="flex min-w-24 flex-1 flex-col gap-0.5" title={`${label}: ${m.used.toLocaleString()} of ${m.limit.toLocaleString()}`}>
      <div className="flex justify-between text-[11px] text-muted-foreground tabular-nums">
        <span>{label}</span>
        <span className={cn(high && "text-status-warning-foreground")}>
          {format(m.used)} / {format(m.limit)}
        </span>
      </div>
      <div role="meter" aria-label={`${label} used`} aria-valuemin={0} aria-valuemax={m.limit} aria-valuenow={m.used} className="h-1.5 overflow-hidden rounded-full bg-muted">
        <div className={cn("h-full rounded-full", high ? "bg-status-warning" : "bg-accent-line")} style={{ width: `${Math.round(m.ratio * 100)}%` }} />
      </div>
    </div>
  );
}

function Header({ session: s, instanceId, switchable }: { session: AgentSession; instanceId: string; switchable: boolean }) {
  const project = useProject();
  const list = useAgentSessions(switchable ? project : undefined);
  const status = sessionStatus(s);
  const budget = budgetUse(s);
  const live = isLive(s);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [confirmEnd, setConfirmEnd] = useState(false);
  const act = async (fn: () => Promise<unknown>) => {
    setBusy(true);
    setError(null);
    try {
      await fn();
    } catch (err) {
      setError(errorMessage(err));
    } finally {
      setBusy(false);
      setConfirmEnd(false);
    }
  };
  const options = useMemo(() => {
    const items = list.data?.items ?? [];
    return items.some((x) => x.id === s.id) ? items : [s, ...items];
  }, [list.data, s]);
  const iconButton = "size-6 [&_svg]:size-3.5";
  return (
    <div className="@container flex shrink-0 flex-col gap-1.5 border-b px-3 py-2" data-slot="chat-header">
      <div className="flex min-w-0 items-center gap-2">
        {switchable ? (
          <NativeSelect
            aria-label="Session shown in this Chat"
            className="h-6 w-auto max-w-56 min-w-0 shrink text-xs font-medium"
            value={s.id}
            onChange={(e) => pinChat(instanceId, e.target.value || null)}
          >
            {options.map((x) => (
              <option key={x.id} value={x.id}>
                {tabLabel(x)} · {x.model}
                {x.id !== s.id ? ` (${x.state.replace("_", " ")})` : ""}
              </option>
            ))}
            <option value="">New session…</option>
          </NativeSelect>
        ) : (
          <h3 className="min-w-0 truncate text-[13px] font-semibold">{sessionLabel(s)}</h3>
        )}
        <span
          className={cn("inline-flex min-w-0 items-center gap-1.5 truncate text-xs font-medium", TONE[status.tone])}
          data-slot="session-state"
          data-state={s.state}
          title={[status.detail, `${s.driver} · ${s.model} · ${s.kind} · preset ${s.preset}`].filter(Boolean).join("\n")}
        >
          <span aria-hidden className={cn("size-2 rounded-full", DOT[status.tone], s.busy && "animate-pulse motion-reduce:animate-none")} />
          {status.label}
        </span>
        {s.kind !== "interactive" ? (
          <span className="shrink-0 rounded-full border px-1.5 text-[11px] text-muted-foreground" data-slot="session-kind">
            {s.kind}
          </span>
        ) : null}
        <span className="sr-only">{sessionLabel(s)}</span>
        {live ? (
          <div className="ml-auto flex shrink-0 items-center gap-0.5" role="toolbar" aria-label="Session">
            {confirmEnd ? (
              <>
                <Button size="xs" variant="destructive" disabled={busy} onClick={() => void act(() => runCommand("agentSessions.cancel", { session: s, end: true }))} autoFocus>
                  End the session
                </Button>
                <Button size="xs" variant="ghost" onClick={() => setConfirmEnd(false)}>
                  Keep it
                </Button>
              </>
            ) : (
              <>
                <Button
                  size="icon-xs"
                  variant="ghost"
                  className={iconButton}
                  disabled={busy || !s.busy}
                  onClick={() => void act(() => runCommand("agentSessions.cancel", { session: s }))}
                  data-command="agentSessions.cancel"
                  aria-label="Stop turn"
                  title="Stop the agent's turn (Ctrl/Cmd+.)"
                >
                  <Square aria-hidden />
                </Button>
                {s.state === "paused" ? (
                  <Button
                    size="icon-xs"
                    variant="ghost"
                    className={iconButton}
                    disabled={busy || s.pendingControl === "resume"}
                    onClick={() => void act(() => runCommand("agentSessions.resume", { session: s }))}
                    data-command="agentSessions.resume"
                    aria-label="Resume"
                    title="Resume the session"
                  >
                    <Play aria-hidden />
                  </Button>
                ) : (
                  <Button
                    size="icon-xs"
                    variant="ghost"
                    className={iconButton}
                    disabled={busy || s.state === "created" || !!s.pendingControl}
                    onClick={() => void act(() => runCommand("agentSessions.pause", { session: s }))}
                    data-command="agentSessions.pause"
                    aria-label="Pause"
                    title="Pause the session"
                  >
                    <Pause aria-hidden />
                  </Button>
                )}
                <Button
                  size="icon-xs"
                  variant="ghost"
                  className={iconButton}
                  disabled={busy || s.pendingControl === "end"}
                  onClick={() => setConfirmEnd(true)}
                  aria-label="End session…"
                  title="End the session"
                >
                  <Eject aria-hidden />
                </Button>
              </>
            )}
          </div>
        ) : null}
      </div>
      {s.state === "failed" && status.detail ? (
        <p className={cn("text-xs", TONE[status.tone])} data-slot="session-error">
          {status.detail}
        </p>
      ) : null}
      <div className="flex flex-wrap items-center gap-3">
        <BudgetMeter label="Turns" m={budget.turns} format={String} />
        <BudgetMeter label="Tokens" m={budget.tokens} format={compact} />
      </div>
      {error ? (
        <p role="alert" className="text-xs text-destructive">
          {error}
        </p>
      ) : null}
    </div>
  );
}

// ---------------------------------------------------------------- transcript (windowed when long)

const ESTIMATE = 72;

function Transcript({ session }: { session: AgentSession }) {
  const q = useTranscript(session.id);
  const items = useMemo(() => visibleEntries(q.data?.items ?? []), [q.data]);
  const scroller = useRef<HTMLDivElement>(null);
  const [heights, setHeights] = useState(() => new Map<string, number>());
  const [scroll, setScroll] = useState({ top: 0, height: 600 });
  const atBottom = useRef(true);
  const [highlighted, setHighlighted] = useState<string | null>(null);
  const highlight = useChatBridge((s) => (s.highlight?.sessionId === session.id ? s.highlight : null));

  const virtual = items.length > VIRTUALIZE_AFTER;
  const ids = useMemo(() => items.map((m) => m.id), [items]);
  const offsets = useMemo(() => rowOffsets(ids, heights, ESTIMATE), [ids, heights]);
  const range = virtual ? visibleRange(offsets, scroll.top, scroll.height) : { first: 0, last: items.length };

  // Row heights (windowed mode): measured rows are collected and applied once per animation frame.
  const [observer] = useState(() => {
    if (typeof ResizeObserver === "undefined") return null;
    const pending = new Map<string, number>();
    let frame = 0;
    const flush = () => {
      frame = 0;
      const batch = new Map(pending);
      pending.clear();
      setHeights((prev) => {
        let next: Map<string, number> | null = null;
        for (const [id, h] of batch) {
          if (prev.get(id) === h) continue;
          next ??= new Map(prev);
          next.set(id, h);
        }
        return next ?? prev;
      });
    };
    return new ResizeObserver((entries) => {
      for (const e of entries) {
        const id = (e.target as HTMLElement).dataset.row;
        if (id) pending.set(id, Math.round(e.contentRect.height) + 8);
      }
      if (!frame) frame = requestAnimationFrame(flush);
    });
  });
  useEffect(() => () => observer?.disconnect(), [observer]);
  const measure = (el: HTMLDivElement | null) => {
    if (el && observer && virtual) observer.observe(el);
  };

  // Follow the stream while the reader is at the bottom.
  useLayoutEffect(() => {
    const el = scroller.current;
    if (el && atBottom.current && !highlighted) el.scrollTop = el.scrollHeight;
  }, [items, heights, highlighted]);

  // The attribution badge's jump: scroll to the tool call, highlight and focus it.
  useEffect(() => {
    if (!highlight) return;
    const i = highlight.toolCallId ? items.findIndex((m) => entryMatchesToolCall(m, highlight.toolCallId!)) : -1;
    if (highlight.toolCallId && i < 0) return; // not in the transcript yet: wait for it
    useChatBridge.getState().setHighlight(null);
    if (i < 0) return;
    const id = items[i]!.id;
    atBottom.current = false;
    setHighlighted(id);
    const el = scroller.current;
    if (el && virtual) el.scrollTop = Math.max(0, offsets[i]! - el.clientHeight / 3);
    requestAnimationFrame(() => {
      const row = scroller.current?.querySelector<HTMLElement>(`[data-entry="${CSS.escape(id)}"]`);
      row?.scrollIntoView({ block: "center" });
      row?.focus({ preventScroll: true });
    });
  }, [highlight, items, offsets, virtual]);
  useEffect(() => {
    if (!highlighted) return;
    const t = setTimeout(() => setHighlighted(null), 4000);
    return () => clearTimeout(t);
  }, [highlighted]);

  const onScroll = (e: UIEvent<HTMLDivElement>) => {
    const el = e.currentTarget;
    atBottom.current = el.scrollHeight - el.scrollTop - el.clientHeight < 24;
    if (virtual) setScroll({ top: el.scrollTop, height: el.clientHeight });
  };
  useLayoutEffect(() => {
    const el = scroller.current;
    if (el) setScroll({ top: el.scrollTop, height: el.clientHeight || 600 });
  }, [virtual]);

  if (q.isLoading) return <p className="flex-1 p-3 text-xs text-muted-foreground">Loading the transcript…</p>;
  if (q.error) return <p role="alert" className="flex-1 p-3 text-xs text-destructive">The transcript could not be read: {errorMessage(q.error)}</p>;
  const top = offsets[range.first] ?? 0;
  const bottom = (offsets[items.length] ?? 0) - (offsets[range.last] ?? 0);
  return (
    // Not role="log": a log is a live region, and streamed tokens must not be announced (WCAG 4.1.3; the finished
    // turn is announced by the shell). Focusable on click so the keys typed after it reach the composer.
    <div
      ref={scroller}
      onScroll={onScroll}
      tabIndex={-1}
      className="@container min-h-0 flex-1 overflow-auto px-2 py-2 outline-none"
      role="region"
      aria-label="Transcript"
      data-testid="chat-transcript"
    >
      {items.length === 0 ? <p className="p-2 text-xs text-muted-foreground">{session.prompt ? "Waiting for the agent…" : "No messages yet."}</p> : null}
      <div style={virtual ? { paddingTop: top, paddingBottom: bottom } : undefined} className="flex flex-col gap-2">
        {items.slice(range.first, range.last).map((m: AgentMessage) => (
          <div key={m.id} data-row={m.id} ref={measure}>
            <Entry m={m} highlighted={highlighted === m.id} />
          </div>
        ))}
      </div>
    </div>
  );
}

// ---------------------------------------------------------------- merge (session changes)

function MergeArea({ session: s }: { session: AgentSession }) {
  const project = useProject();
  const ended = !isLive(s);
  const show = s.kind === "interactive" && !!s.branch && (ended || s.state === "paused");
  const decidable = show && s.merge.state !== "merged" && s.merge.state !== "discarded";
  const diff = useQuery({ ...branchesGetOptions({ path: { p: project ?? s.project, name: s.branch } }), enabled: decidable, retry: false });
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [confirmDiscard, setConfirmDiscard] = useState(false);
  if (!show) return null;
  const act = async (id: "agentSessions.accept" | "agentSessions.revert") => {
    setBusy(true);
    setError(null);
    try {
      await runCommand(id, { session: s });
    } catch (err) {
      setError(errorMessage(err));
    } finally {
      setBusy(false);
      setConfirmDiscard(false);
    }
  };
  const d = diff.data;
  const changes = s.merge.state === "pending" || s.merge.state === "conflict" || (!ended && !!d && d.ahead > 0);
  const conflicts = s.merge.conflicts?.length ? s.merge.conflicts : (d?.conflicts ?? []);
  let summary: string;
  switch (s.merge.state) {
    case "merged":
      summary = `Merged into main at ${s.merge.commit?.slice(0, 7) ?? "?"}${s.merge.fastForward ? " (fast-forward)" : ""}${s.merge.by?.name ? ` by ${s.merge.by.name}` : ""}.`;
      break;
    case "discarded":
      summary = "The session changes were discarded.";
      break;
    case "conflict":
      summary = `Merging would conflict on ${conflicts.join(", ") || "some files"}; discard, or resolve on the branch.`;
      break;
    case "pending":
      summary = "Session changes wait for you: accept merges the branch into main, discard deletes it.";
      break;
    default:
      summary = changes ? "Paused with changes on the session branch; accepting or discarding ends the session." : "No file changes to merge.";
  }
  return (
    <section aria-label="Session changes" className="shrink-0 border-t bg-chrome px-3 py-2 text-xs" data-slot="merge-area" data-merge={s.merge.state}>
      <div className="flex flex-wrap items-center gap-2">
        <span className="font-medium">Session changes</span>
        <span className="rounded-full border px-2 py-0.5 text-[11px] text-muted-foreground" data-slot="merge-state">
          {s.merge.state === "none" && changes ? "open" : s.merge.state}
        </span>
        <code className="text-[11px] text-muted-foreground">{s.branch}</code>
        <Button size="xs" variant="ghost" className="ml-auto" onClick={() => openBranch(s.branch)}>
          Diff in Recipe
        </Button>
      </div>
      <p className="mt-1 text-muted-foreground">{summary}</p>
      {decidable && d && d.files.length ? (
        <ul className="mt-1 max-h-28 overflow-auto font-mono text-[11px]" aria-label="Changed files">
          {d.files.map((f) => (
            <li key={f.path} className="flex gap-2">
              <span className="w-14 shrink-0 text-muted-foreground">{f.status}</span>
              <span className="min-w-0 truncate">{f.path}</span>
              <span className="ml-auto shrink-0 tabular-nums">
                <span className="text-diff-added-foreground">+{f.additions}</span> <span className="text-diff-removed-foreground">−{f.deletions}</span>
              </span>
            </li>
          ))}
        </ul>
      ) : null}
      {decidable && changes ? (
        <div className="mt-2 flex flex-wrap items-center gap-1">
          <Button size="xs" disabled={busy || conflicts.length > 0} onClick={() => void act("agentSessions.accept")} data-command="agentSessions.accept">
            Accept into main
          </Button>
          {confirmDiscard ? (
            <>
              <Button size="xs" variant="destructive" disabled={busy} onClick={() => void act("agentSessions.revert")} autoFocus>
                Discard the changes
              </Button>
              <Button size="xs" variant="ghost" onClick={() => setConfirmDiscard(false)}>
                Keep them
              </Button>
            </>
          ) : (
            <Button size="xs" variant="outline" disabled={busy} onClick={() => setConfirmDiscard(true)} data-command="agentSessions.revert">
              Discard…
            </Button>
          )}
        </div>
      ) : null}
      {error ? (
        <p role="alert" className="mt-1 text-destructive">
          {error}
        </p>
      ) : null}
    </section>
  );
}

// ---------------------------------------------------------------- composer

function Composer({ instanceId, session, bound }: { instanceId: string; session: AgentSession | undefined; bound: boolean }) {
  const project = useProject();
  const draft = useChatDraft(instanceId);
  const focusNonce = useChatBridge((s) => s.focus[instanceId] ?? 0);
  const ref = useRef<HTMLTextAreaElement>(null);
  const [sending, setSending] = useState(false);
  const [error, setError] = useState<string | null>(null);
  useEffect(() => {
    if (focusNonce) ref.current?.focus();
  }, [focusNonce]);
  const b = useChatBridge.getState;
  const continues = !!session && isLive(session) && session.kind === "interactive";
  const placeholder = continues
    ? `Message ${sessionLabel(session)}`
    : session && !isLive(session)
      ? "This session ended; a message starts a new session"
      : session?.kind === "read-only"
        ? "A read-only session takes one message; a message starts a new session"
        : "Message starts a new agent session (profile's driver and model)";

  const send = async () => {
    const text = draft.text.trim();
    if (!text || sending || !project) return;
    setSending(true);
    setError(null);
    try {
      if (continues) {
        await runCommand("agentMessages.new", { sessionId: session.id, text, references: draft.refs });
      } else {
        const s = await runCommand("agentSessions.new", { project, body: { kind: "interactive", prompt: text, references: draft.refs } });
        if (s) {
          if (bound) openChat(s.id);
          else pinChat(instanceId, s.id);
        }
      }
      b().clearDraft(instanceId);
    } catch (err) {
      setError(errorMessage(err));
    } finally {
      setSending(false);
    }
  };
  const onKey = (e: KeyboardEvent<HTMLTextAreaElement>) => {
    if (e.key === "Enter" && !e.shiftKey && !e.nativeEvent.isComposing) {
      e.preventDefault();
      void send();
    }
  };
  const attach = () => {
    const refs = currentSelectionReferences();
    if (refs.length) b().addRefs(instanceId, refs);
    ref.current?.focus();
  };
  return (
    <div className="@container flex shrink-0 flex-col gap-1.5 border-t bg-background p-2" data-slot="composer">
      <RefChips refs={draft.refs} onRemove={(r) => b().removeRef(instanceId, r)} />
      <Textarea
        ref={ref}
        value={draft.text}
        onChange={(e) => b().setDraft(instanceId, { text: e.target.value })}
        onKeyDown={onKey}
        placeholder={placeholder}
        aria-label="Message to the agent"
        rows={2}
        className="max-h-40 min-h-12 resize-none text-[13px]"
        disabled={!project}
      />
      <div className="flex min-w-0 items-center gap-1">
        <Button size="xs" variant="ghost" className="shrink-0" onClick={attach} title="Attach the current selection (Ctrl/Cmd+I)" disabled={!project}>
          <Attachment aria-hidden />
          Attach selection
        </Button>
        <span className="ml-auto hidden items-center gap-1 text-[11px] whitespace-nowrap text-muted-foreground @lg:flex">
          <Kbd>Enter</Kbd> send · <Kbd>Shift+Enter</Kbd> new line
        </span>
        <Button
          size="xs"
          className="ml-auto shrink-0 @lg:ml-0"
          disabled={!draft.text.trim() || sending || !project}
          onClick={() => void send()}
          data-command={continues ? "agentMessages.new" : "agentSessions.new"}
          title="Enter sends · Shift+Enter starts a new line"
        >
          <SendDiagonal aria-hidden />
          {continues ? "Send" : "Start session"}
        </Button>
      </div>
      {error ? (
        <p role="alert" className="text-xs text-destructive">
          {error}
        </p>
      ) : null}
    </div>
  );
}
