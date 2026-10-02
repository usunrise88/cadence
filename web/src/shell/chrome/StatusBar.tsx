import { createContext, useContext, useEffect, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Bell, ChatBubble, CheckCircle, Cpu, GitFork, HalfMoon, OpenInWindow, SunLight } from "iconoir-react";
import { branchesListOptions, computeListOptions, queueEntriesListOptions } from "@/api/gen/@tanstack/react-query.gen";
import { Button } from "@/components/ui/button";
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover";
import type { AgentSession, Approval, Branch, CardTelemetry, QueueEntry } from "@/api/gen/types.gen";
import { cn } from "@/lib/utils";
import { useSnap } from "@/shell/floating-snap/dockview-adapter";
import { useHelp } from "@/shell/help/store";
import type { ConnectionState } from "@/shell/live/events";
import { useNotices } from "@/shell/notifications/store";
import { usePendingApprovals } from "@/shell/approvals/cache";
import { openPanel } from "@/shell/dock/layout";
import { events } from "@/shell/registries";
import { useTheme } from "@/shell/theme/store";
import { useWorkspaceSync } from "@/shell/workspaces/persistence";
import { openBranch, openChat } from "@/shell/agents/bridge";
import { isAsleep, sessionLabel } from "@/shell/agents/labels";
import { useAgentSessions } from "@/shell/agents/sessions";
import { useShell } from "@/shell/state";
import { focusPipelineRun } from "@/shell/training/focus";
import { sortEntries } from "@/shell/training/queue";
import { waitingBranches, waitingReason } from "./branches";
import { applyTelemetry, formatReading, isStale, mergeReadings, seedReadings, sortedReadings, type Readings } from "./gpu";

// Status bar: live connection, workspace save state, GPU telemetry per card, the step queue, agent sessions and
// approvals, snapping, theme and the notification history. The polite live region lives here too (WCAG 4.1.3).

function useConnection(): ConnectionState {
  const [s, setS] = useState(events.connectionState);
  useEffect(() => events.onState(setS), []);
  return s;
}

const CONN_LABEL: Record<ConnectionState, string> = { idle: "Live: idle", connecting: "Live: connecting", open: "Live", error: "Live: offline" };

export function StatusBar() {
  const conn = useConnection();
  const sync = useWorkspaceSync();
  const snap = useSnap((s) => s.enabled);
  const { dark, setMode } = useTheme();
  return (
    <footer className="flex h-6 shrink-0 items-center gap-3 border-t bg-chrome px-2.5 text-[11px] text-muted-foreground [&>span+span]:border-l [&>span+span]:pl-3">
      <span data-testid="connection" className="inline-flex items-center gap-1">
        <span aria-hidden className={cn("size-2 rounded-full", conn === "open" ? "bg-status-done" : conn === "error" ? "bg-status-failed" : "bg-muted-foreground")} />
        {CONN_LABEL[conn]}
      </span>
      <span data-testid="workspace-sync">{sync.restoring ? "Restoring…" : sync.saving ? "Saving…" : sync.rev && !sync.placeholder ? `Saved · rev ${sync.rev}` : "Not saved yet"}</span>
      <div className="ml-auto flex items-center gap-1">
        <GpuBadge />
        <QueueBadge />
        <AgentSessionsBadge />
        <BranchesBadge />
        <ApprovalsBadge />
        <Button variant="ghost" size="xs" className="h-5 px-1.5 text-[11px] font-normal text-muted-foreground" onClick={() => useSnap.getState().setEnabled(!snap)} aria-pressed={snap}>
          Snap {snap ? "on" : "off"}
        </Button>
        <Button variant="ghost" size="icon-xs" className="size-6 text-muted-foreground [&_svg]:size-3.5" aria-label={dark ? "Light mode" : "Dark mode"} onClick={() => setMode(dark ? "light" : "dark")}>
          {dark ? <SunLight aria-hidden /> : <HalfMoon aria-hidden />}
        </Button>
        <NotificationHistory />
      </div>
      <LiveRegion />
    </footer>
  );
}

// The status bar's popups share one shape (docs/spec/10-ui-shell.md "Status bar"): a small popover with a short
// list, and — where a panel holds the full view — a button that opens that panel as a floating window.

// A row that acts closes the popup it sits in.
const ClosePopover = createContext<() => void>(() => {});

const trigger = "h-5 gap-1 px-1.5 text-[11px] font-normal [&_svg]:size-3.5";

function StatusPopover({
  button,
  title,
  actions,
  expand,
  children,
  onOpenChange,
}: {
  button: React.ReactElement;
  title: string;
  actions?: React.ReactNode;
  /** Opens the full panel as a floating window. */
  expand?: { label: string; panel: string };
  children: React.ReactNode;
  onOpenChange?: (open: boolean) => void;
}) {
  const [open, setOpen] = useState(false);
  return (
    <Popover
      open={open}
      onOpenChange={(o) => {
        setOpen(o);
        onOpenChange?.(o);
      }}
    >
      <PopoverTrigger render={button} />
      <PopoverContent align="end" className="w-96 gap-0 p-0" data-slot="status-popover" aria-label={title}>
        <div className="flex h-9 items-center gap-1 border-b px-3 text-xs font-medium">
          <span className="mr-auto">{title}</span>
          {actions}
          {expand ? (
            <Button
              variant="ghost"
              size="icon-xs"
              className="size-6 text-muted-foreground [&_svg]:size-3.5"
              aria-label={expand.label}
              title={expand.label}
              data-slot="status-popover-expand"
              onClick={() => {
                setOpen(false);
                openPanel(expand.panel, { location: "floating" });
              }}
            >
              <OpenInWindow aria-hidden />
            </Button>
          ) : null}
        </div>
        <ClosePopover.Provider value={() => setOpen(false)}>
          <div className="max-h-80 overflow-auto text-xs">{children}</div>
        </ClosePopover.Provider>
      </PopoverContent>
    </Popover>
  );
}

const SESSION_TONE: Record<string, string> = {
  running: "bg-status-running",
  created: "bg-muted-foreground",
  waiting_approval: "bg-status-warning",
  paused: "bg-status-warning",
  asleep: "bg-muted-foreground",
};

/** Live agent sessions of the project: a popup listing them (click opens the session's Chat); expands into Agent sessions. */
function AgentSessionsBadge() {
  const project = useShell((s) => s.project);
  const { data } = useAgentSessions(project);
  const items = data?.items ?? [];
  const liveItems = items.filter((s) => ["created", "running", "waiting_approval", "paused"].includes(s.state));
  const running = items.filter((s) => s.state === "running" || s.state === "created").length;
  const waiting = items.filter((s) => s.state === "waiting_approval").length;
  const asleep = items.filter(isAsleep).length;
  const paused = items.filter((s) => s.state === "paused").length - asleep;
  const live = running + waiting + paused + asleep;
  const parts = [running && `${running} running`, waiting && `${waiting} waiting for approval`, paused && `${paused} paused`, asleep && `${asleep} asleep`].filter(Boolean).join(", ");
  return (
    <StatusPopover
      title="Agent sessions"
      expand={{ label: "Open Agent sessions as a window", panel: "agent-sessions" }}
      button={
        <Button
          variant="ghost"
          size="xs"
          data-testid="agent-sessions-badge"
          disabled={!project}
          className={cn(trigger, waiting ? "text-status-warning-foreground" : live ? "text-foreground" : "text-muted-foreground")}
          aria-label={live ? `Agent sessions: ${parts}` : "No live agent sessions"}
        >
          <ChatBubble aria-hidden />
          Agents
          {live ? <span className={cn("min-w-4 rounded-full border px-1 text-center font-medium tabular-nums", waiting ? "border-status-warning" : "border-border")}>{live}</span> : null}
        </Button>
      }
    >
      {liveItems.length === 0 ? <p className="p-3 text-muted-foreground">No live sessions. Write in Chat to start one.</p> : null}
      <ul>
        {liveItems.map((s) => (
          <SessionRow key={s.id} s={s} />
        ))}
      </ul>
    </StatusPopover>
  );
}

/**
 * Pending approvals: a popup listing them (click opens the requesting Chat or Approvals); expands into Approvals.
 * While anything waits the badge blinks (still under prefers-reduced-motion).
 */
function ApprovalsBadge() {
  const { data } = usePendingApprovals();
  const items = data?.items ?? [];
  const n = items.length;
  return (
    <StatusPopover
      title="Pending approvals"
      expand={{ label: "Open Approvals as a window", panel: "approvals" }}
      button={
        <Button
          variant="ghost"
          size="xs"
          data-testid="approvals-badge"
          data-waiting={n ? "" : undefined}
          className={cn(trigger, n ? "animate-attention text-status-warning-foreground" : "text-muted-foreground")}
          aria-label={n ? `${n} pending approval${n === 1 ? "" : "s"}` : "No pending approvals"}
        >
          <CheckCircle aria-hidden />
          Approvals
          {n ? <span className="min-w-4 rounded-full border border-status-warning px-1 text-center font-medium tabular-nums">{n}</span> : null}
        </Button>
      }
    >
      {n === 0 ? <p className="p-3 text-muted-foreground">Nothing waits for you.</p> : null}
      <ul>
        {items.map((a) => (
          <ApprovalRow key={a.id} a={a} />
        ))}
      </ul>
    </StatusPopover>
  );
}

/**
 * Branches of the project repository that wait for a person — a template sync, another branch, an ended agent
 * session's unmerged changes: a popup listing them (click opens the branch in the Recipe document); blinks while any
 * wait, like Approvals.
 */
function BranchesBadge() {
  const project = useShell((s) => s.project);
  const qc = useQueryClient();
  const opts = branchesListOptions({ path: { p: project ?? "" } });
  const { data } = useQuery({ ...opts, enabled: !!project, staleTime: 30_000, refetchInterval: 60_000 });
  const sessions = useAgentSessions(project);
  const queryKey = opts.queryKey;
  useEffect(
    () =>
      events.subscribe(
        ["branches", "recipe.*", "agent.sessions", "entity.project.*"],
        () => void qc.invalidateQueries({ queryKey }),
        "shell",
      ),
    // The key is a fresh array each render; the project names it.
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [qc, project],
  );
  const items = waitingBranches(data?.items ?? [], sessions.data?.items ?? []);
  const n = items.length;
  if (!project) return null;
  return (
    <StatusPopover
      title="Branches waiting for review"
      button={
        <Button
          variant="ghost"
          size="xs"
          data-testid="branches-badge"
          data-waiting={n ? "" : undefined}
          className={cn(trigger, n ? "animate-attention text-status-warning-foreground" : "text-muted-foreground")}
          aria-label={n ? `${n} branch${n === 1 ? "" : "es"} waiting for review` : "No branch waits for review"}
        >
          <GitFork aria-hidden />
          Branches
          {n ? <span className="min-w-4 rounded-full border border-status-warning px-1 text-center font-medium tabular-nums">{n}</span> : null}
        </Button>
      }
    >
      {n === 0 ? <p className="p-3 text-muted-foreground">Nothing to merge: every branch is in main or still being worked on.</p> : null}
      <ul>
        {items.map((b) => (
          <BranchRow key={b.name} b={b} />
        ))}
      </ul>
    </StatusPopover>
  );
}

function BranchRow({ b }: { b: Branch }) {
  const close = useContext(ClosePopover);
  return (
    <li className="border-b last:border-0">
      <button
        type="button"
        className="flex w-full min-w-0 flex-col gap-0.5 px-3 py-2 text-left hover:bg-hover focus-visible:bg-hover focus-visible:outline-none"
        onClick={() => {
          close();
          openBranch(b.name);
        }}
        data-branch={b.name}
      >
        <span className="flex min-w-0 items-center gap-2">
          <span aria-hidden className="size-2 shrink-0 rounded-full bg-status-warning" />
          <span className="min-w-0 truncate font-mono font-medium">{b.name}</span>
          <time className="ml-auto shrink-0 text-muted-foreground" dateTime={b.updatedAt}>
            {new Date(b.updatedAt).toLocaleDateString()}
          </time>
        </span>
        <span className="truncate pl-4 text-muted-foreground">
          {waitingReason(b)} · {b.ahead} commit{b.ahead === 1 ? "" : "s"} ahead{b.behind ? `, ${b.behind} behind main` : ""}
          {b.subject ? ` · ${b.subject}` : ""}
        </span>
      </button>
    </li>
  );
}

/** GPU memory used/total and utilisation per card, live on `gpu`: a popup per card; expands into Queue & GPU. */
function GpuBadge() {
  const compute = useQuery(computeListOptions());
  const [live, setLive] = useState<Readings>({});
  useEffect(
    () =>
      events.subscribe(
        ["gpu"],
        (batch) => {
          for (const e of batch) {
            if (e.type !== "gpu.telemetry") continue;
            const p = e.payload as { host?: string; cards?: CardTelemetry[]; at?: string } | undefined;
            if (!p?.host || !p.cards) continue;
            const { host, cards } = p;
            const at = p.at ? Date.parse(p.at) / 1000 : Date.now() / 1000;
            setLive((cur) => applyTelemetry(cur, host, cards, at));
          }
        },
        "shell",
      ),
    [],
  );
  // Staleness is judged against a clock that ticks twice a minute.
  const [now, setNow] = useState(() => Date.now() / 1000);
  useEffect(() => {
    const t = setInterval(() => setNow(Date.now() / 1000), 30_000);
    return () => clearInterval(t);
  }, []);
  const cards = sortedReadings(mergeReadings(seedReadings(compute.data?.items ?? []), live));
  const fresh = cards.filter((c) => !isStale(c, now));
  const label = fresh.length === 0 ? "GPU —" : `GPU ${fresh.map(formatReading).join(" | ")}`;
  const aria = fresh.length === 0 ? "No recent GPU telemetry" : `GPU: ${fresh.map((c) => `${c.name} ${formatReading(c)}`).join("; ")}`;
  return (
    <StatusPopover
      title="GPU"
      expand={{ label: "Open Queue & GPU as a window", panel: "queue-gpu" }}
      button={
        <Button variant="ghost" size="xs" data-testid="gpu-badge" className={cn(trigger, "tabular-nums", fresh.length ? "text-foreground" : "text-muted-foreground")} aria-label={aria}>
          <Cpu aria-hidden />
          {label}
        </Button>
      }
    >
      {cards.length === 0 ? <p className="p-3 text-muted-foreground">No card has reported yet. A worker reports its cards when it claims a job and on every heartbeat.</p> : null}
      <ul>
        {cards.map((c) => (
          <li key={c.key} className="border-b px-3 py-2 last:border-0" data-card={c.key}>
            <div className="flex items-center gap-2">
              <span aria-hidden className={cn("size-2 shrink-0 rounded-full", isStale(c, now) ? "bg-muted-foreground" : "bg-status-done")} />
              <span className="min-w-0 truncate font-medium">{c.name}</span>
              <span className="ml-auto shrink-0 text-muted-foreground">
                {c.host} · card {c.index}
              </span>
            </div>
            <p className="pl-4 text-muted-foreground tabular-nums">
              {formatReading(c) || "no reading"}
              {c.capGb ? ` · Cadence cap ${c.capGb} GB` : ""}
              {isStale(c, now) ? ` · last seen ${new Date(c.at * 1000).toLocaleTimeString()}` : ""}
            </p>
          </li>
        ))}
      </ul>
    </StatusPopover>
  );
}

/** Step jobs running and waiting for the current project (every project without one): a popup listing them (click
 * shows the job's pipeline run, or Queue & GPU); expands into Queue & GPU. */
function QueueBadge() {
  const project = useShell((s) => s.project);
  const qc = useQueryClient();
  const opts = queueEntriesListOptions(project ? { query: { project } } : undefined);
  const { data } = useQuery(opts);
  const queryKey = opts.queryKey;
  useEffect(
    () =>
      events.subscribe(
        ["queue"],
        (batch) => {
          if (batch.some((e) => e.type === "queue.changed")) void qc.invalidateQueries({ queryKey });
        },
        "shell",
      ),
    // The key is a fresh array each render; the project names it.
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [qc, project],
  );
  const items = sortEntries(data?.items ?? []);
  const running = items.filter((e) => e.state === "running" || e.state === "stopping").length;
  return (
    <StatusPopover
      title="Queue"
      expand={{ label: "Open Queue & GPU as a window", panel: "queue-gpu" }}
      button={
        <Button
          variant="ghost"
          size="xs"
          data-testid="queue-badge"
          className={cn(trigger, "tabular-nums", items.length ? "text-foreground" : "text-muted-foreground")}
          aria-label={items.length ? `Queue: ${running} running, ${items.length - running} waiting` : "The queue is empty"}
        >
          Queue {items.length ? `${running}/${items.length}` : "—"}
        </Button>
      }
    >
      {items.length === 0 ? <p className="p-3 text-muted-foreground">Nothing runs or waits{project ? " in this project" : ""}.</p> : null}
      <ul>
        {items.map((e) => (
          <QueueRow key={e.jobId} e={e} />
        ))}
      </ul>
    </StatusPopover>
  );
}

const QUEUE_TONE: Record<QueueEntry["state"], string> = {
  running: "bg-status-running",
  stopping: "bg-status-warning",
  waiting: "bg-muted-foreground",
  paused: "bg-status-warning",
};

function QueueRow({ e }: { e: QueueEntry }) {
  const close = useContext(ClosePopover);
  const l = e.lease;
  const progress = e.state === "running" && l?.progress !== undefined ? `${Math.round(l.progress * 100)} %` : undefined;
  const where = l ? (l.card < 0 ? l.host : `${l.host} · card ${l.card}`) : undefined;
  return (
    <li className="border-b last:border-0">
      <button
        type="button"
        className="flex w-full min-w-0 flex-col gap-0.5 px-3 py-2 text-left hover:bg-hover focus-visible:bg-hover focus-visible:outline-none"
        onClick={() => {
          close();
          if (e.pipelineRunId) {
            focusPipelineRun(e.pipelineRunId);
            openPanel("pipeline-run");
          } else openPanel("queue-gpu", { location: "floating" });
        }}
        data-job={e.jobId}
        data-state={e.state}
      >
        <span className="flex min-w-0 items-center gap-2">
          <span aria-hidden className={cn("size-2 shrink-0 rounded-full", QUEUE_TONE[e.state], e.state === "running" && "animate-pulse motion-reduce:animate-none")} />
          <span className="min-w-0 truncate font-medium">
            {e.kind}@{e.kindVersion}
          </span>
          <span className="shrink-0 text-muted-foreground">{e.jobKind}</span>
          <span className="ml-auto shrink-0 text-muted-foreground tabular-nums">{progress ?? (e.state === "running" ? "running" : e.state)}</span>
        </span>
        <span className="truncate pl-4 text-muted-foreground">
          {[where, l?.message, e.attempt > 1 ? `attempt ${e.attempt}` : undefined, !l ? `priority ${e.priority}` : undefined].filter(Boolean).join(" · ") || "waiting for a card"}
        </span>
      </button>
    </li>
  );
}

function SessionRow({ s }: { s: AgentSession }) {
  const close = useContext(ClosePopover);
  const state = isAsleep(s) ? "asleep" : s.state;
  return (
    <li className="border-b last:border-0">
      <button
        type="button"
        className="flex w-full min-w-0 items-center gap-2 px-3 py-2 text-left hover:bg-hover focus-visible:bg-hover focus-visible:outline-none"
        onClick={() => {
          close();
          openChat(s.id);
        }}
        data-session={s.id}
      >
        <span aria-hidden className={cn("size-2 shrink-0 rounded-full", SESSION_TONE[state] ?? "bg-muted-foreground", s.busy && "animate-pulse motion-reduce:animate-none")} />
        <span className="shrink-0 font-medium">{sessionLabel(s)}</span>
        <span className="min-w-0 truncate text-muted-foreground">
          {state.replace("_", " ")}
          {s.busy ? " · working" : ""} · {s.model}
        </span>
      </button>
    </li>
  );
}

function ApprovalRow({ a }: { a: Approval }) {
  const close = useContext(ClosePopover);
  return (
    <li className="border-b last:border-0">
      <button
        type="button"
        className="flex w-full min-w-0 flex-col gap-0.5 px-3 py-2 text-left hover:bg-hover focus-visible:bg-hover focus-visible:outline-none"
        onClick={() => {
          close();
          if (a.permission?.sessionId) openChat(a.permission.sessionId);
          else openPanel("approvals", { location: "floating" });
        }}
        data-approval={a.id}
      >
        <span className="flex min-w-0 items-center gap-2">
          <span aria-hidden className="size-2 shrink-0 rounded-full bg-status-warning" />
          <span className="min-w-0 truncate font-medium">{a.permission?.title ?? a.operation}</span>
          <time className="ml-auto shrink-0 text-muted-foreground" dateTime={a.createdAt}>
            {new Date(a.createdAt).toLocaleTimeString()}
          </time>
        </span>
        <span className="truncate pl-4 text-muted-foreground">
          {a.actor.name ?? a.actor.kind} · {a.reason}
        </span>
      </button>
    </li>
  );
}

function NotificationHistory() {
  const { items, markAllRead, clear } = useNotices();
  const unread = items.filter((i) => !i.read).length;
  return (
    <StatusPopover
      title="Notifications"
      onOpenChange={(open) => open || markAllRead()}
      actions={
        <Button variant="ghost" size="xs" onClick={clear}>
          Clear
        </Button>
      }
      button={
        <Button variant="ghost" size="icon-xs" className="relative size-6 text-muted-foreground [&_svg]:size-3.5" aria-label={`Notifications${unread ? ` (${unread} unread)` : ""}`}>
          <Bell aria-hidden />
          {unread ? <span className="absolute top-0.5 right-0.5 size-1.5 rounded-full bg-status-failed" /> : null}
        </Button>
      }
    >
      <ul>
        {items.length === 0 ? <li className="p-3 text-muted-foreground">Nothing yet.</li> : null}
        {items.map((n) => (
          <li key={n.id} className="border-b px-3 py-2 last:border-0">
            <div className="flex items-center gap-2">
              <span className={cn("size-2 rounded-full", n.level === "error" ? "bg-status-failed" : n.level === "warning" ? "bg-status-warning" : n.level === "success" ? "bg-status-done" : "bg-muted-foreground")} />
              <span className="font-medium">{n.title}</span>
              <time className="ml-auto text-muted-foreground">{new Date(n.at).toLocaleTimeString()}</time>
            </div>
            {n.detail ? <p className="mt-1 text-muted-foreground">{n.detail}</p> : null}
            {n.open ? (
              <button type="button" className="mt-1 mr-3 text-primary underline-offset-2 hover:underline" onClick={() => openPanel(n.open!.panel)}>
                {n.open.label}
              </button>
            ) : null}
            {n.helpId ? (
              <button
                type="button"
                className="mt-1 text-primary underline-offset-2 hover:underline"
                onClick={() => {
                  useHelp.getState().show(n.helpId!);
                  openPanel("help");
                }}
              >
                What does this mean?
              </button>
            ) : null}
          </li>
        ))}
      </ul>
    </StatusPopover>
  );
}

function LiveRegion() {
  const message = useNotices((s) => s.announcement);
  return (
    <div role="status" aria-live="polite" className="sr-only" data-testid="live-region">
      {message}
    </div>
  );
}
