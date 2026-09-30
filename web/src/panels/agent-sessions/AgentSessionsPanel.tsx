import { useEffect, useId, useMemo, useRef, useState, type FormEvent } from "react";
import { useQuery } from "@tanstack/react-query";
import { ChatBubble, Plus } from "iconoir-react";
import { agentModelsListOptions, agentProfileGetOptions } from "@/api/gen/@tanstack/react-query.gen";
import type { AgentDriver, AgentSession } from "@/api/gen/types.gen";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { NativeSelect } from "@/components/ui/native-select";
import { Textarea } from "@/components/ui/textarea";
import { cn } from "@/lib/utils";
import { EmptyState, PanelToolbar } from "@/shell/entity/primitives";
import {
  errorMessage,
  isAsleep,
  isLive,
  openChat,
  PlaybookLauncher,
  runCommand,
  sessionLabel,
  sessionStateLabel,
  SESSIONS_TOPIC,
  useAgentPatcher,
  useAgentSessions,
  useChatBridge,
  usePendingApprovals,
  useProject,
  useTopic,
  type PanelProps,
} from "@/shell/panel";

// Agent sessions (docs/spec/11-ui-panels.md "Panel catalogue"): every session of the project by kind and state,
// with driver, model, budget use, pending approvals and the merge state of its branch; New session (Claude Code or
// opencode, the model from the agent profile), open its Chat, pause or resume, accept or discard session changes.
// Opens floating from the status bar.

export function AgentSessionsEmpty() {
  return <EmptyState step="prepare" title="No agent sessions yet" hint="Start one here or from Chat; Ask agent on any entity starts one with the entity attached." />;
}

type Filter = "live" | "ended" | "all";

const DRIVERS: { id: AgentDriver; label: string }[] = [
  { id: "claude-code", label: "Claude Code" },
  { id: "opencode", label: "opencode" },
];

const STATE_ORDER = ["waiting_approval", "running", "created", "paused", "failed", "done", "cancelled"];

export function AgentSessionsPanel(_props: PanelProps) {
  const project = useProject();
  const list = useAgentSessions(project);
  const patch = useAgentPatcher();
  useTopic(project ? [SESSIONS_TOPIC] : null, patch);
  const pending = usePendingApprovals();
  const [filter, setFilter] = useState<Filter>("all");
  const formNonce = useChatBridge((s) => s.newSessionForm);
  const [formOpen, setFormOpen] = useState(formNonce > 0);
  useEffect(() => {
    if (formNonce > 0) setFormOpen(true);
  }, [formNonce]);

  const waiting = useMemo(() => {
    const m = new Map<string, number>();
    for (const a of pending.data?.items ?? []) if (a.actor.sessionId) m.set(a.actor.sessionId, (m.get(a.actor.sessionId) ?? 0) + 1);
    return m;
  }, [pending.data]);
  const items = (list.data?.items ?? [])
    .filter((s) => filter === "all" || (filter === "live" ? isLive(s) : !isLive(s)))
    .sort((a, b) => STATE_ORDER.indexOf(a.state) - STATE_ORDER.indexOf(b.state) || b.number - a.number);

  if (!project) return <AgentSessionsEmpty />;
  return (
    <div className="flex h-full min-h-0 flex-col">
      <PanelToolbar>
        <div role="radiogroup" aria-label="Sessions shown" className="inline-flex rounded-md border bg-background p-0.5 text-xs">
          {(["all", "live", "ended"] as const).map((f) => (
            <button
              key={f}
              type="button"
              role="radio"
              aria-checked={filter === f}
              onClick={() => setFilter(f)}
              className={cn("h-6 rounded-[4px] px-2.5 capitalize", filter === f ? "bg-selected font-medium text-foreground" : "text-muted-foreground hover:text-foreground")}
            >
              {f}
            </button>
          ))}
        </div>
        <Button size="xs" className="ml-auto" onClick={() => setFormOpen((o) => !o)} aria-expanded={formOpen} data-command="agentSessions.new">
          <Plus aria-hidden />
          New session
        </Button>
      </PanelToolbar>
      {formOpen ? <NewSessionForm key={formNonce} project={project} onDone={() => setFormOpen(false)} /> : null}
      <div className="min-h-0 flex-1 overflow-auto">
        {list.isLoading ? <p className="p-3 text-xs text-muted-foreground">Loading…</p> : null}
        {!list.isLoading && items.length === 0 ? (
          <div className="h-56">
            <AgentSessionsEmpty />
          </div>
        ) : null}
        <ul className="flex flex-col gap-1.5 p-2" aria-label="Agent sessions">
          {items.map((s) => (
            <SessionRow key={s.id} s={s} approvals={waiting.get(s.id) ?? 0} />
          ))}
        </ul>
      </div>
    </div>
  );
}

const STATE_CLASS: Record<string, string> = {
  running: "text-status-running-foreground",
  created: "text-muted-foreground",
  waiting_approval: "text-status-warning-foreground",
  paused: "text-status-warning-foreground",
  done: "text-status-done-foreground",
  failed: "text-status-failed-foreground",
  cancelled: "text-muted-foreground",
};

const capitalize = (t: string) => t.charAt(0).toUpperCase() + t.slice(1);

function SessionRow({ s, approvals }: { s: AgentSession; approvals: number }) {
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const act = async (fn: () => Promise<unknown>) => {
    setBusy(true);
    setError(null);
    try {
      await fn();
    } catch (err) {
      setError(errorMessage(err));
    } finally {
      setBusy(false);
    }
  };
  const tokens = s.use.inputTokens + s.use.outputTokens;
  const decidable = s.kind !== "read-only" && (s.merge.state === "pending" || (s.state === "paused" && s.merge.state === "none"));
  const asleep = isAsleep(s);
  return (
    <li className="flex flex-col gap-1.5 rounded-md border bg-background p-2 text-xs" data-session={s.id} data-state={s.state}>
      <div className="flex min-w-0 flex-wrap items-center gap-x-2 gap-y-1">
        <span className="font-medium">{sessionLabel(s)}</span>
        <span className="rounded-full border px-1.5 text-[11px] text-muted-foreground">{s.kind}</span>
        <span className={cn("font-medium", asleep ? "text-muted-foreground" : STATE_CLASS[s.state])} data-slot="session-state">
          {sessionStateLabel(s)}
          {s.busy ? " · working" : ""}
        </span>
        {approvals ? <span className="rounded-full border border-status-warning px-1.5 text-[11px] text-status-warning-foreground">{approvals} pending approval{approvals === 1 ? "" : "s"}</span> : null}
        <span className="ml-auto text-muted-foreground tabular-nums">{new Date(s.updatedAt).toLocaleString()}</span>
      </div>
      {s.pauseReason && s.state === "paused" ? (
        asleep ? (
          <p className="text-muted-foreground">{`${capitalize(s.pauseReason.message)}; a message in its Chat wakes it.`}</p>
        ) : (
          <p className="text-status-warning-foreground">{s.pauseReason.message}</p>
        )
      ) : null}
      {s.error && s.state === "failed" ? <p className="text-status-failed-foreground">{s.error}</p> : null}
      <dl className="grid grid-cols-[5.5rem_1fr] gap-x-2 gap-y-0.5 text-[11px]">
        <dt className="text-muted-foreground">Model</dt>
        <dd className="truncate">{s.model}</dd>
        <dt className="text-muted-foreground">Budget use</dt>
        <dd className="tabular-nums">
          {s.use.turns}/{s.budget.turns} turns · {tokens.toLocaleString()}/{s.budget.tokens.toLocaleString()} tokens
        </dd>
        <dt className="text-muted-foreground">Merge</dt>
        <dd>{s.branch ? `${s.merge.state}${s.merge.conflicts?.length ? ` (${s.merge.conflicts.length} conflicts)` : ""} · ${s.branch}` : "read-only, no branch"}</dd>
        {s.prompt ? (
          <>
            <dt className="text-muted-foreground">Prompt</dt>
            <dd className="truncate" title={s.prompt}>
              {s.prompt}
            </dd>
          </>
        ) : null}
      </dl>
      <div className="flex flex-wrap items-center gap-1">
        <Button size="xs" variant="outline" onClick={() => openChat(s.id)}>
          <ChatBubble aria-hidden />
          Open Chat
        </Button>
        {s.state === "running" || s.state === "waiting_approval" ? (
          <Button size="xs" variant="ghost" disabled={busy || !!s.pendingControl} onClick={() => void act(() => runCommand("agentSessions.pause", { session: s }))}>
            Pause
          </Button>
        ) : null}
        {s.state === "paused" ? (
          <Button size="xs" variant="ghost" disabled={busy || s.pendingControl === "resume"} onClick={() => void act(() => runCommand("agentSessions.resume", { session: s }))}>
            Resume
          </Button>
        ) : null}
        {decidable ? (
          <>
            <Button size="xs" variant="ghost" disabled={busy} onClick={() => void act(() => runCommand("agentSessions.accept", { session: s }))}>
              Accept changes
            </Button>
            <Button size="xs" variant="ghost" disabled={busy} onClick={() => void act(() => runCommand("agentSessions.revert", { session: s }))}>
              Discard changes
            </Button>
          </>
        ) : null}
      </div>
      {error ? (
        <p role="alert" className="text-destructive">
          {error}
        </p>
      ) : null}
    </li>
  );
}

function NewSessionForm({ project, onDone }: { project: string; onDone: () => void }) {
  const id = useId();
  const profile = useQuery(agentProfileGetOptions({ path: { p: project } }));
  const catalogue = useQuery(agentModelsListOptions());
  const [driver, setDriver] = useState<AgentDriver | null>(null);
  const [model, setModel] = useState<string | null>(null);
  const [prompt, setPrompt] = useState("");
  const [mode, setMode] = useState<"interactive" | "playbook">(() => useChatBridge.getState().newSessionMode);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const first = useRef<HTMLSelectElement>(null);
  useEffect(() => first.current?.focus(), []);

  const d: AgentDriver = driver ?? profile.data?.driver ?? "claude-code";
  const entry = catalogue.data?.items.find((c) => c.driver === d);
  // The model defaults to the agent profile's (for the profile's driver), else the driver's catalogue default.
  const defaultModel = profile.data && profile.data.driver === d ? profile.data.model : (entry?.default ?? "");
  const m = model ?? defaultModel;

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    setBusy(true);
    setError(null);
    try {
      const s = await runCommand("agentSessions.new", {
        project,
        body: { kind: "interactive", driver: d, ...(m ? { model: m } : {}), ...(prompt.trim() ? { prompt: prompt.trim() } : {}) },
      });
      if (s) openChat(s.id);
      onDone();
    } catch (err) {
      setError(errorMessage(err));
    } finally {
      setBusy(false);
    }
  };
  return (
    <div className="flex flex-col gap-2 border-b bg-chrome p-3 text-xs">
      <div role="radiogroup" aria-label="Session kind" className="inline-flex self-start rounded-md border bg-background p-0.5">
        {(
          [
            ["interactive", "Interactive"],
            ["playbook", "From a playbook"],
          ] as const
        ).map(([k, label]) => (
          <button
            key={k}
            type="button"
            role="radio"
            aria-checked={mode === k}
            onClick={() => setMode(k)}
            className={cn("h-6 rounded-[4px] px-2.5", mode === k ? "bg-selected font-medium text-foreground" : "text-muted-foreground hover:text-foreground")}
          >
            {label}
          </button>
        ))}
      </div>
    <form onSubmit={(e) => void submit(e)} className="flex flex-col gap-2" aria-label="New agent session">
      <div className="grid grid-cols-[4.5rem_1fr] items-center gap-2">
        <label htmlFor={`${id}-driver`} className="text-muted-foreground">
          Agent
        </label>
        <NativeSelect
          id={`${id}-driver`}
          ref={first}
          value={d}
          onChange={(e) => {
            setDriver(e.target.value as AgentDriver);
            setModel(null);
          }}
        >
          {DRIVERS.map((x) => (
            <option key={x.id} value={x.id}>
              {x.label}
            </option>
          ))}
        </NativeSelect>
        <label htmlFor={`${id}-model`} className="text-muted-foreground">
          Model
        </label>
        {entry && !entry.freeForm ? (
          <NativeSelect id={`${id}-model`} value={m} onChange={(e) => setModel(e.target.value)}>
            {!entry.models.some((x) => x.id === m) && m ? <option value={m}>{m}</option> : null}
            {entry.models.map((x) => (
              <option key={x.id} value={x.id}>
                {x.name}
                {x.id === defaultModel ? " (default)" : ""}
              </option>
            ))}
          </NativeSelect>
        ) : (
          <Input id={`${id}-model`} value={m} onChange={(e) => setModel(e.target.value)} placeholder={entry?.default ?? "provider/model"} list={`${id}-models`} />
        )}
        {entry?.freeForm ? (
          <datalist id={`${id}-models`}>
            {entry.models.map((x) => (
              <option key={x.id} value={x.id}>
                {x.name}
              </option>
            ))}
          </datalist>
        ) : null}
      </div>
      {mode === "playbook" ? null : (
      <label className="flex flex-col gap-1">
        <span className="text-muted-foreground">First message (optional)</span>
        <Textarea value={prompt} onChange={(e) => setPrompt(e.target.value)} rows={2} className="min-h-12 text-xs" placeholder="What should the agent do?" />
      </label>
      )}
      {mode === "playbook" ? null : (
        <>
          <p className="text-[11px] text-muted-foreground">Interactive, on its own branch; budget and permissions from the project's agent profile.</p>
          <div className="flex items-center gap-1">
            <Button type="submit" size="xs" disabled={busy}>
              Start session
            </Button>
            <Button type="button" size="xs" variant="ghost" onClick={onDone}>
              Cancel
            </Button>
          </div>
        </>
      )}
      {error ? (
        <p role="alert" className="text-destructive">
          {error}
        </p>
      ) : null}
    </form>
      {mode === "playbook" ? <PlaybookLauncher project={project} driver={d} {...(m ? { model: m } : {})} onStarted={onDone} onCancel={onDone} /> : null}
    </div>
  );
}
