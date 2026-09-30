import { memo, useEffect, useMemo, useState, type AnchorHTMLAttributes, type ReactNode } from "react";
import { useQuery } from "@tanstack/react-query";
import { Check, Circle, EditPencil, NavArrowRight, Page, Terminal, Tools, WarningTriangle, Xmark } from "iconoir-react";
import { Streamdown } from "streamdown";
import { approvalsGetOptions } from "@/api/gen/@tanstack/react-query.gen";
import type { AgentFileDiff, AgentMessage, AgentPlanEntry, AgentReference, AgentToolCall } from "@/api/gen/types.gen";
import { Button } from "@/components/ui/button";
import { cn } from "@/lib/utils";
import { ApprovalCard, linkifyReferences, openPanelById, openReference, parseReference, referenceChipLabel, referenceFromHref } from "@/shell/panel";
import { compact, diffStat, dryRunEstimate, lineDiff, toolDraft, toolEntityRef, toolOperation, toolResult } from "./model";

// One transcript entry per kind (docs/spec/05-agents.md "What the Chat panel shows"): streaming Markdown replies
// with references as links, collapsed thinking, the plan checklist, tool calls (Cadence MCP, file edit, shell) as one
// quiet line that opens into their card,
// permission and approval requests inline, commits and turn usage. Entries are memoised on their revision: a
// streamed update re-renders only its own entry.

/** A reference as a link that opens what it names (a document, a session's Chat, a help article). */
export function RefLink({ refText, label, className }: { refText: string; label?: string; className?: string }) {
  const ok = !!parseReference(refText);
  return (
    <button
      type="button"
      data-ref={refText}
      disabled={!ok}
      onClick={() => openReference(refText)}
      className={cn("inline cursor-pointer rounded-sm font-mono text-[0.92em] text-accent-text underline-offset-2 hover:underline focus-visible:ring-2 focus-visible:ring-ring focus-visible:outline-none", className)}
      title={`Open ${refText}`}
    >
      {label ?? refText}
    </button>
  );
}

export function RefChips({ refs, onRemove }: { refs: AgentReference[]; onRemove?: (ref: string) => void }) {
  if (refs.length === 0) return null;
  return (
    <ul className="flex flex-wrap gap-1" aria-label="References">
      {refs.map((r) => (
        <li key={r.ref} data-slot="reference-chip" data-ref={r.ref} className="inline-flex h-6 max-w-full items-center gap-0.5 rounded-full border bg-accent-soft pr-0.5 pl-2 text-[11px] text-accent-text">
          <button type="button" className="min-w-0 truncate hover:underline" title={r.ref} onClick={() => openReference(r.ref)}>
            {referenceChipLabel(r)}
          </button>
          {onRemove ? (
            <button
              type="button"
              aria-label={`Remove ${r.ref}`}
              onClick={() => onRemove(r.ref)}
              className="inline-flex size-6 shrink-0 items-center justify-center rounded-full hover:bg-hover [&_svg]:size-3"
            >
              <Xmark aria-hidden />
            </button>
          ) : (
            <span className="w-1.5" />
          )}
        </li>
      ))}
    </ul>
  );
}

/** Links in agent Markdown: our references open in the shell; anything else opens in a new tab. */
function MarkdownLink({ href, children, ...rest }: AnchorHTMLAttributes<HTMLAnchorElement> & { node?: unknown }) {
  const ref = referenceFromHref(href);
  if (ref) return <RefLink refText={ref} label={typeof children === "string" ? children : undefined} />;
  const { node: _node, ...props } = rest as typeof rest & { node?: unknown };
  return (
    <a href={href} target="_blank" rel="noopener noreferrer" {...props}>
      {children}
    </a>
  );
}

const MD_COMPONENTS = { a: MarkdownLink };

export function AgentMarkdown({ text, streaming }: { text: string; streaming: boolean }) {
  const md = useMemo(() => linkifyReferences(text), [text]);
  return (
    <div className="cadence-prose text-[13px]" aria-busy={streaming || undefined}>
      <Streamdown mode={streaming ? "streaming" : "static"} isAnimating={streaming} controls={false} components={MD_COMPONENTS}>
        {md}
      </Streamdown>
    </div>
  );
}

function Who({ children, className }: { children: ReactNode; className?: string }) {
  return <span className={cn("text-[11px] font-medium tracking-wide text-muted-foreground uppercase", className)}>{children}</span>;
}

// ---------------------------------------------------------------- kinds

function UserMessage({ m }: { m: AgentMessage }) {
  return (
    <div className="flex flex-col gap-1 rounded-md border bg-background px-3 py-2">
      <div className="flex items-center gap-2">
        <Who>{m.actor.kind === "user" ? (m.actor.name ?? "You") : m.actor.name ?? m.actor.kind}</Who>
        {m.delivery === "pending" ? <span className="text-[11px] text-muted-foreground">queued for the agent</span> : null}
      </div>
      {m.text ? <p className="text-[13px] leading-snug whitespace-pre-wrap">{m.text}</p> : null}
      {m.references?.length ? <RefChips refs={m.references} /> : null}
    </div>
  );
}

function AgentText({ m }: { m: AgentMessage }) {
  return <AgentMarkdown text={m.text ?? ""} streaming={!m.final} />;
}

function Thought({ m }: { m: AgentMessage }) {
  return (
    <details className="group rounded-md text-xs text-muted-foreground" data-slot="thought">
      <summary className="flex min-h-6 cursor-pointer items-center gap-1 select-none">
        <NavArrowRight aria-hidden className="size-3 transition-transform group-open:rotate-90" />
        {m.final ? "Thinking" : "Thinking…"}
      </summary>
      <p className="mt-1 border-l-2 pl-3 leading-relaxed whitespace-pre-wrap">{m.text}</p>
    </details>
  );
}

const PLAN_LABEL: Record<AgentPlanEntry["status"], string> = { pending: "to do", in_progress: "in progress", completed: "done" };

function Plan({ m }: { m: AgentMessage }) {
  const plan = m.plan ?? [];
  const done = plan.filter((p) => p.status === "completed").length;
  return (
    <section className="rounded-md border bg-background px-3 py-2" data-slot="plan" aria-label={`Plan, ${done} of ${plan.length} done`}>
      <div className="mb-1 flex items-center gap-2">
        <Who>Plan</Who>
        <span className="text-[11px] text-muted-foreground tabular-nums">
          {done}/{plan.length}
        </span>
      </div>
      <ul className="flex flex-col gap-0.5 text-[13px]">
        {plan.map((p, i) => (
          <li key={i} data-status={p.status} className="flex items-start gap-2">
            <span aria-label={PLAN_LABEL[p.status]} role="img" className="mt-0.5 inline-flex size-4 shrink-0 items-center justify-center">
              {p.status === "completed" ? (
                <Check aria-hidden className="size-4 text-status-done-foreground" />
              ) : p.status === "in_progress" ? (
                <span aria-hidden className="size-2.5 rounded-full bg-status-running" />
              ) : (
                <Circle aria-hidden className="size-3.5 text-muted-foreground" />
              )}
            </span>
            <span className={cn(p.status === "completed" && "text-muted-foreground", p.status === "in_progress" && "font-medium")}>{p.content}</span>
          </li>
        ))}
      </ul>
    </section>
  );
}

const STATUS_LABEL: Record<AgentToolCall["status"], string> = { pending: "pending", in_progress: "running", completed: "done", failed: "failed" };
const STATUS_CLASS: Record<AgentToolCall["status"], string> = {
  pending: "text-muted-foreground",
  in_progress: "text-status-running-foreground",
  completed: "text-status-done-foreground",
  failed: "text-status-failed-foreground",
};

/** The status on the collapsed line: quiet when done, coloured while running or on failure. */
function ToolStatus({ status }: { status: AgentToolCall["status"] }) {
  if (status === "completed") return null;
  return <span className={cn("shrink-0 font-medium", STATUS_CLASS[status])}>{STATUS_LABEL[status]}</span>;
}

function Json({ label, value }: { label: string; value: unknown }) {
  if (value === undefined) return null;
  return (
    <details className="rounded border bg-tool px-2 py-1">
      <summary className="min-h-6 cursor-pointer text-muted-foreground select-none">{label}</summary>
      <pre className="mt-1 max-h-60 overflow-auto font-mono text-[11px] whitespace-pre-wrap">{typeof value === "string" ? value : JSON.stringify(value, null, 2)}</pre>
    </details>
  );
}

/** A tool call as one collapsed line (icon, what it did, a short fact, its status) and the card behind it. */
type ToolView = { icon: typeof Tools; label: ReactNode; meta?: ReactNode; status: ReactNode; body: ReactNode };

function mcpView(tc: AgentToolCall): ToolView {
  const op = toolOperation(tc);
  const entity = toolEntityRef(tc);
  const estimate = dryRunEstimate(tc);
  const res = toolResult(tc);
  const draft = toolDraft(tc);
  const failed = !!res?.error || (res?.status ?? 0) >= 400;
  return {
    icon: Tools,
    label: (
      <code className="font-mono" data-slot="tool-operation">
        {op ?? tc.title}
      </code>
    ),
    meta: draft ? "draft" : estimate ? "dry run" : undefined,
    status: failed && tc.status === "completed" ? <span className="shrink-0 font-medium text-status-failed-foreground tabular-nums">{res?.status ?? "error"}</span> : <ToolStatus status={tc.status} />,
    body: (
      <>
        {op && !tc.title.endsWith(op) ? <p className="text-muted-foreground">{tc.title}</p> : null}
        {entity || draft || tc.approvalId || tc.jobId ? (
          <div className="flex flex-wrap items-center gap-x-3 gap-y-1">
            {entity ? <RefLink refText={entity} /> : null}
            {draft ? (
              <span className="rounded border border-dashed border-draft-outline px-1.5 text-[11px]" data-slot="tool-draft">
                Draft{draft.rev ? ` rev ${draft.rev}` : ""} — accept or revert on the document
              </span>
            ) : null}
            {tc.approvalId ? (
              <Button size="xs" variant="ghost" className="h-6 px-1.5" onClick={() => openPanelById("approvals")}>
                Approval {tc.approvalId.slice(0, 12)}…
                <NavArrowRight aria-hidden />
              </Button>
            ) : null}
            {tc.jobId ? <RefLink refText={`@job:${tc.jobId}`} label={`Job ${tc.jobId.slice(0, 12)}…`} /> : null}
          </div>
        ) : null}
        {estimate ? (
          <p className="rounded bg-accent-soft px-2 py-1 text-accent-text" data-slot="dry-run">
            {estimate}
          </p>
        ) : null}
        {res?.error ? (
          <p role="note" className="text-status-failed-foreground">
            {res.error.title ?? "Error"}
            {res.error.detail ? `: ${res.error.detail}` : ""}
          </p>
        ) : null}
        <Json label="Arguments" value={tc.input} />
        <Json label="Result" value={tc.output} />
      </>
    ),
  };
}

function FileDiff({ d }: { d: AgentFileDiff }) {
  const lines = useMemo(() => lineDiff(d.oldText, d.newText), [d.oldText, d.newText]);
  const stat = diffStat(lines);
  return (
    <div className="overflow-hidden rounded border">
      <div className="flex items-center gap-2 border-b bg-tool px-2 py-1">
        <button type="button" className="min-w-0 truncate font-mono text-[11px] hover:underline" onClick={() => openReference(`@recipe:${d.path}`)} title={`Open ${d.path}`}>
          {d.path}
        </button>
        {d.oldText === undefined ? <span className="text-[11px] text-muted-foreground">new file</span> : null}
        <span className="ml-auto shrink-0 text-[11px] tabular-nums">
          <span className="text-diff-added-foreground">+{stat.added}</span> <span className="text-diff-removed-foreground">−{stat.removed}</span>
        </span>
      </div>
      <pre className="max-h-72 overflow-auto py-1 font-mono text-[11px] leading-5" aria-label={`Diff of ${d.path}`}>
        {lines.map((l, i) =>
          l.op === "…" ? (
            <div key={i} className="px-2 text-muted-foreground select-none">
              … {l.count} unchanged line{l.count === 1 ? "" : "s"}
            </div>
          ) : (
            <div key={i} className={cn("px-2 whitespace-pre", l.op === "+" && "bg-diff-added text-diff-added-foreground", l.op === "-" && "bg-diff-removed text-diff-removed-foreground")}>
              <span aria-hidden className="mr-2 select-none">
                {l.op}
              </span>
              <span className="sr-only">{l.op === "+" ? "added: " : l.op === "-" ? "removed: " : ""}</span>
              {l.text || " "}
            </div>
          ),
        )}
      </pre>
    </div>
  );
}

function editView(tc: AgentToolCall): ToolView {
  const diffs = tc.diffs ?? [];
  const stat = diffs.reduce(
    (a, d) => {
      const s = diffStat(lineDiff(d.oldText, d.newText));
      return { added: a.added + s.added, removed: a.removed + s.removed };
    },
    { added: 0, removed: 0 },
  );
  return {
    icon: EditPencil,
    label: tc.title,
    meta: diffs.length ? (
      <span className="tabular-nums">
        <span className="text-diff-added-foreground">+{stat.added}</span> <span className="text-diff-removed-foreground">−{stat.removed}</span>
      </span>
    ) : undefined,
    status: <ToolStatus status={tc.status} />,
    body: diffs.length ? (
      diffs.map((d) => <FileDiff key={d.path} d={d} />)
    ) : tc.locations?.length ? (
      <p className="font-mono text-[11px] text-muted-foreground">{tc.locations.join(", ")}</p>
    ) : null,
  };
}

function shellView(tc: AgentToolCall): ToolView {
  const sh = tc.shell;
  const exit = sh?.exitCode;
  return {
    icon: Terminal,
    label: (
      <code className="font-mono" data-slot="shell-command">
        $ {sh?.command ?? tc.title}
      </code>
    ),
    status:
      exit !== undefined && exit !== 0 ? (
        <span className="shrink-0 font-medium text-status-failed-foreground tabular-nums">exit {exit}</span>
      ) : exit === undefined ? (
        <ToolStatus status={tc.status} />
      ) : null,
    body: sh?.output ? (
      <>
        <p className="text-muted-foreground">
          {exit !== undefined ? `exit ${exit} · ` : ""}
          {sh.output.split("\n").length} lines of output
        </p>
        <pre className="max-h-72 overflow-auto rounded border bg-tool px-2 py-1 font-mono text-[11px] whitespace-pre-wrap" data-slot="shell-output">
          {sh.output}
        </pre>
      </>
    ) : exit !== undefined ? (
      <p className="text-muted-foreground">exit {exit} · no output</p>
    ) : null,
  };
}

function otherView(tc: AgentToolCall): ToolView {
  const body =
    tc.locations?.length || tc.text || tc.input !== undefined || tc.output !== undefined ? (
      <>
        {tc.locations?.length ? <p className="truncate font-mono text-[11px] text-muted-foreground">{tc.locations.join(", ")}</p> : null}
        {tc.text ? <p className="whitespace-pre-wrap text-muted-foreground">{tc.text}</p> : null}
        <Json label="Arguments" value={tc.input} />
        <Json label="Result" value={tc.output} />
      </>
    ) : null;
  return { icon: Page, label: tc.title, meta: tc.class !== "other" ? tc.class : undefined, status: <ToolStatus status={tc.status} />, body };
}

function toolView(tc: AgentToolCall): ToolView {
  if (tc.class === "mcp") return mcpView(tc);
  if (tc.class === "edit" || tc.diffs?.length) return editView(tc);
  if (tc.class === "shell" || tc.shell) return shellView(tc);
  return otherView(tc);
}

// Which tool calls the reader opened, by entry: survives the row leaving the window of a long transcript.
const opened = new Set<string>();

function ToolCall({ m, highlighted }: { m: AgentMessage; highlighted: boolean }) {
  const tc = m.toolCall!;
  const [open, setOpen] = useState(() => opened.has(m.id));
  // The attribution badge's jump opens the call it lands on.
  useEffect(() => {
    if (highlighted) setOpen(true);
  }, [highlighted]);
  useEffect(() => {
    if (open) opened.add(m.id);
    else opened.delete(m.id);
  }, [open, m.id]);
  const v = toolView(tc);
  const line = (
    <>
      <NavArrowRight aria-hidden className={cn("size-3 shrink-0 transition-transform", open && "rotate-90", !v.body && "invisible")} />
      <v.icon aria-hidden className="size-3 shrink-0" />
      <span className="min-w-0 truncate">{v.label}</span>
      {v.meta ? <span className="shrink-0 opacity-80">{v.meta}</span> : null}
      {v.status}
    </>
  );
  const lineClass = "flex min-h-6 w-full min-w-0 items-center gap-1.5 rounded px-1 text-left text-[11px] text-muted-foreground";
  return (
    <div data-slot="tool-call" data-tool-class={tc.class} data-open={open || undefined} className="flex flex-col">
      {v.body ? (
        <button
          type="button"
          aria-expanded={open}
          onClick={() => setOpen((o) => !o)}
          className={cn(lineClass, "cursor-pointer hover:bg-hover hover:text-foreground focus-visible:ring-2 focus-visible:ring-ring focus-visible:outline-none")}
          data-slot="tool-line"
        >
          {line}
        </button>
      ) : (
        <div className={lineClass} data-slot="tool-line">
          {line}
        </div>
      )}
      {open && v.body ? <div className="mt-1 ml-5 flex flex-col gap-1.5 rounded-md border bg-background px-3 py-2 text-xs">{v.body}</div> : null}
    </div>
  );
}

function Permission({ m }: { m: AgentMessage }) {
  const p = m.permission!;
  const q = useQuery({ ...approvalsGetOptions({ path: { id: p.approvalId ?? "" } }), enabled: !!p.approvalId });
  if (p.approvalId && q.data) return <ApprovalCard approval={q.data} hideContext />;
  const what = p.title ?? p.operation ?? "A request";
  const how =
    p.state === "pending"
      ? "waits for a decision"
      : p.state === "approved"
        ? `allowed${p.grant === "session" ? " for the session" : ""}${p.rule ? ` by the preset rule ${p.rule}` : ""}`
        : p.state === "denied"
          ? `denied${p.rule ? ` by the preset rule ${p.rule}` : ""}`
          : "withdrawn";
  return (
    <p className="flex items-center gap-2 text-xs text-muted-foreground" data-slot="permission-line">
      <WarningTriangle aria-hidden className="size-3.5 shrink-0" />
      <span>
        <span className="text-foreground">{what}</span> {how}
        {p.note ? ` — “${p.note}”` : ""}
      </span>
    </p>
  );
}

function Commit({ m }: { m: AgentMessage }) {
  const c = m.commit!;
  if (c.refused) {
    return (
      <div role="note" className="flex flex-col gap-1 rounded-md border border-status-failed px-3 py-2 text-xs" data-slot="commit">
        <p className="font-medium text-status-failed-foreground">Nothing committed: the changes held a credential</p>
        <ul className="font-mono text-[11px]">
          {(c.findings ?? []).map((f, i) => (
            <li key={i}>
              {f.path}
              {f.line ? `:${f.line}` : ""} — {f.kind}
            </li>
          ))}
        </ul>
      </div>
    );
  }
  return (
    <details className="text-xs" data-slot="commit">
      <summary className="flex min-h-6 cursor-pointer items-center gap-2 text-muted-foreground select-none">
        <Check aria-hidden className="size-3.5" />
        Committed <code className="font-mono">{c.sha?.slice(0, 7)}</code> · {c.files?.length ?? 0} file{c.files?.length === 1 ? "" : "s"} on the session branch
      </summary>
      <ul className="mt-1 ml-5 font-mono text-[11px]">
        {(c.files ?? []).map((f) => (
          <li key={f}>
            <button type="button" className="hover:underline" onClick={() => openReference(`@recipe:${f}`)}>
              {f}
            </button>
          </li>
        ))}
      </ul>
    </details>
  );
}

function TurnEnd({ m }: { m: AgentMessage }) {
  const t = m.turnInfo!;
  const tokens = (t.inputTokens ?? 0) + (t.outputTokens ?? 0);
  return (
    <div className="flex items-center gap-2 text-[11px] text-muted-foreground" data-slot="turn-end">
      <span className="h-px flex-1 bg-border" />
      <span>
        Turn {m.turn} {t.stopReason && t.stopReason !== "end_turn" ? `· ${t.stopReason.replace(/_/g, " ")}` : "· finished"}
        {tokens ? ` · ${compact(t.inputTokens ?? 0)} in / ${compact(t.outputTokens ?? 0)} out` : ""}
      </span>
      <span className="h-px flex-1 bg-border" />
    </div>
  );
}

function Notice({ m }: { m: AgentMessage }) {
  return (
    <p
      className={cn(
        "text-xs",
        m.level === "error" ? "text-status-failed-foreground" : m.level === "warning" ? "text-status-warning-foreground" : "text-muted-foreground",
      )}
      data-slot="notice"
    >
      {m.text}
    </p>
  );
}

function body(m: AgentMessage, highlighted: boolean): ReactNode {
  switch (m.kind) {
    case "user_message":
      return <UserMessage m={m} />;
    case "agent_message":
      return <AgentText m={m} />;
    case "thought":
      return <Thought m={m} />;
    case "plan":
      return <Plan m={m} />;
    case "tool_call":
      return m.toolCall ? <ToolCall m={m} highlighted={highlighted} /> : null;
    case "permission":
      return m.permission ? <Permission m={m} /> : null;
    case "commit":
      return m.commit ? <Commit m={m} /> : null;
    case "turn":
      return m.turnInfo?.state === "ended" ? <TurnEnd m={m} /> : null;
    case "notice":
      return <Notice m={m} />;
  }
}

export const Entry = memo(
  function Entry({ m, highlighted }: { m: AgentMessage; highlighted: boolean }) {
    return (
      <article
        data-entry={m.id}
        data-kind={m.kind}
        data-seq={m.seq}
        data-tool-call={m.toolCall?.id ?? m.permission?.toolCallId}
        data-highlighted={highlighted || undefined}
        tabIndex={-1}
        className={cn("rounded-md px-1 outline-none focus-visible:ring-2 focus-visible:ring-ring", m.kind === "tool_call" ? "-my-1" : "py-1", highlighted && "bg-agent ring-2 ring-accent-line")}
      >
        {body(m, highlighted)}
      </article>
    );
  },
  (a, b) => a.m === b.m && a.highlighted === b.highlighted,
);
