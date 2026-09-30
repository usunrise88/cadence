import { useEffect, useId, useState, type KeyboardEvent } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { NavArrowRight } from "iconoir-react";
import { ProblemError } from "@/api/client";
import { projectsListOptions } from "@/api/gen/@tanstack/react-query.gen";
import type { Approval } from "@/api/gen/types.gen";
import { Button } from "@/components/ui/button";
import { Kbd } from "@/components/ui/kbd";
import { Textarea } from "@/components/ui/textarea";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import { cn } from "@/lib/utils";
import { runCommand } from "@/shell/commands/api";
import { StatusChip } from "@/shell/entity/primitives";
import { notify } from "@/shell/notifications/store";
import { openDocument } from "@/shell/panel/actions";
import { patchApprovals } from "./cache";
import { actorLabel, countdown, decisionLine, estimateLine, requestLine, time } from "./format";
import { useFocusedApproval } from "./store";

// The approval card (docs/spec/11-ui-panels.md "Approvals"; docs/spec/05-agents.md "Guardrails"): one gated
// request with its action, estimate, requester, scope, rule and reason, context, requested-at and expiry countdown,
// and the decision — Approve once, Approve for this session, Deny — with an optional note. The card is the confirm:
// Enter approves once and Backspace denies while the card itself has focus. The Approvals panel lists these cards;
// the Chat panel embeds the same component inline.

export type ApprovalCardProps = {
  approval: Approval;
  /** Hide the project / session context links (the Chat panel is already the context). */
  hideContext?: boolean;
  /** Called after this card's decision went through (the list moves focus on). */
  onDecided?: (a: Approval) => void;
  className?: string;
};

function useNow(active: boolean, ms = 1000): number {
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    if (!active) return;
    const t = setInterval(() => setNow(Date.now()), ms);
    return () => clearInterval(t);
  }, [active, ms]);
  return now;
}

type Decision = { kind: "approve"; grant: "once" | "session" } | { kind: "deny" };

export function ApprovalCard({ approval: a, hideContext, onDecided, className }: ApprovalCardProps) {
  const qc = useQueryClient();
  const titleId = useId();
  const hintId = useId();
  const pending = a.state === "pending";
  const now = useNow(pending);
  const expiry = countdown(a.expiresAt, now);
  const [note, setNote] = useState("");
  const [noteOpen, setNoteOpen] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const projects = useQuery({ ...projectsListOptions(), enabled: !!a.projectId });
  const project = a.projectId ? projects.data?.items.find((p) => p.id === a.projectId) : undefined;
  const estimate = estimateLine(a);
  const sessionReason = a.actor.sessionId ? true : "Only a request from an agent session can be approved for the session";

  const decide = async (d: Decision) => {
    if (!pending || busy || expiry.expired) return;
    setBusy(true);
    setError(null);
    try {
      const n = note.trim() || undefined;
      const result =
        d.kind === "approve"
          ? await runCommand("approvals.approve", { approval: a, grant: d.grant, note: n })
          : await runCommand("approvals.deny", { approval: a, note: n });
      patchApprovals(qc, [result]);
      if (d.kind === "approve" && result.result && result.result.status >= 400) {
        notify({ level: "error", title: `Approved, but ${a.operation} failed`, detail: `The replayed request answered ${result.result.status}.` });
      }
      onDecided?.(result);
    } catch (err) {
      if (err instanceof ProblemError && err.status === 412) {
        setError("This request changed meanwhile (decided elsewhere or expired). The list is refreshed.");
        void qc.invalidateQueries({ predicate: (q) => (q.queryKey[0] as { _id?: string } | undefined)?._id === "approvalsList" });
      } else {
        setError(err instanceof ProblemError ? (err.problem.detail ?? err.problem.title) : err instanceof Error ? err.message : String(err));
      }
    } finally {
      setBusy(false);
    }
  };

  const onKey = (e: KeyboardEvent<HTMLElement>) => {
    if (e.target !== e.currentTarget || !pending) return;
    if (e.key === "Enter") {
      e.preventDefault();
      void decide({ kind: "approve", grant: "once" });
    } else if (e.key === "Backspace") {
      e.preventDefault();
      void decide({ kind: "deny" });
    }
  };

  return (
    <article
      data-slot="approval-card"
      data-approval-card={a.id}
      data-state={a.state}
      tabIndex={0}
      aria-labelledby={titleId}
      aria-describedby={pending ? hintId : undefined}
      aria-busy={busy || undefined}
      onKeyDown={onKey}
      onFocus={(e) => e.target === e.currentTarget && useFocusedApproval.getState().set(a)}
      className={cn(
        "@container flex flex-col gap-2 rounded-md border bg-background p-3 text-xs outline-none focus-visible:ring-2 focus-visible:ring-ring",
        pending ? "border-status-warning" : "opacity-90",
        className,
      )}
    >
      <div className="flex min-w-0 items-center gap-2">
        {a.scope === "registry" ? (
          <span className="shrink-0 rounded-full bg-accent-soft px-2 py-0.5 text-[11px] font-medium text-accent-text">Registry</span>
        ) : (
          <span className="shrink-0 rounded-full border px-2 py-0.5 text-[11px] text-muted-foreground" title="Project scope">
            {project?.slug ?? "Project"}
          </span>
        )}
        <h3 id={titleId} className="min-w-0 font-mono text-[13px] font-medium break-all">
          {a.operation}
        </h3>
        <span className="ml-auto shrink-0">
          {pending ? (
            <span className={cn("tabular-nums", expiry.urgent ? "font-medium text-status-warning-foreground" : "text-muted-foreground")} title={`Expires ${time(a.expiresAt)}`}>
              {expiry.expired ? "Expired" : `Expires in ${expiry.label}`}
            </span>
          ) : (
            <StatusChip state={a.state} />
          )}
        </span>
      </div>

      <p className="text-[13px] leading-snug">{a.reason}</p>

      <dl className="grid grid-cols-[6.5rem_1fr] gap-x-3 gap-y-1">
        <dt className="text-muted-foreground">Requested by</dt>
        <dd className="truncate">
          <span className={cn("rounded px-1", a.actor.kind === "agent" && "bg-agent text-agent-foreground")}>{actorLabel(a.actor)}</span>
        </dd>
        <dt className="text-muted-foreground">Estimate</dt>
        <dd>{estimate ?? "No GPU spend"}</dd>
        <dt className="text-muted-foreground">Rule</dt>
        <dd className="truncate font-mono">{a.rule}</dd>
        <dt className="text-muted-foreground">Action</dt>
        <dd className="truncate font-mono" title={requestLine(a)}>
          {requestLine(a)}
        </dd>
        <dt className="text-muted-foreground">Requested</dt>
        <dd className="tabular-nums">{time(a.createdAt)}</dd>
        {!pending ? (
          <>
            <dt className="text-muted-foreground">Decision</dt>
            <dd>
              {decisionLine(a)}
              {a.decidedAt ? <span className="text-muted-foreground tabular-nums"> · {time(a.decidedAt)}</span> : null}
              {a.decision?.note ? <span className="block text-muted-foreground">“{a.decision.note}”</span> : null}
              {a.result ? <span className="block text-muted-foreground">Replay answered {a.result.status}</span> : null}
            </dd>
          </>
        ) : null}
      </dl>

      {a.request.body !== undefined ? (
        <details className="rounded border bg-tool px-2 py-1">
          <summary className="cursor-pointer text-muted-foreground">Request body</summary>
          <pre className="mt-1 max-h-40 overflow-auto font-mono text-[11px] whitespace-pre-wrap">{JSON.stringify(a.request.body, null, 2)}</pre>
        </details>
      ) : null}

      {!hideContext && (project || a.actor.sessionId) ? (
        <div className="flex flex-wrap items-center gap-1">
          {project ? (
            <Button size="xs" variant="ghost" onClick={() => openDocument(`project:${project.slug}`)}>
              Open project {project.slug}
              <NavArrowRight aria-hidden />
            </Button>
          ) : null}
          {a.actor.sessionId ? (
            <Tooltip>
              <TooltipTrigger render={<span tabIndex={0} />}>
                <Button size="xs" variant="ghost" disabled>
                  Open the requesting Chat
                </Button>
              </TooltipTrigger>
              <TooltipContent>The Chat panel for session {a.actor.sessionId} arrives with agent sessions</TooltipContent>
            </Tooltip>
          ) : null}
        </div>
      ) : null}

      {pending ? (
        <>
          {noteOpen ? (
            <label className="flex flex-col gap-1">
              <span className="text-muted-foreground">Note (optional, recorded with the decision)</span>
              <Textarea value={note} maxLength={2000} onChange={(e) => setNote(e.target.value)} className="min-h-12 text-xs" autoFocus />
            </label>
          ) : null}
          <div className="flex flex-wrap items-center gap-1.5">
            <Button size="xs" disabled={busy || expiry.expired} onClick={() => void decide({ kind: "approve", grant: "once" })} data-command="approvals.approve">
              Approve once
            </Button>
            {sessionReason === true ? (
              <Button size="xs" variant="outline" disabled={busy || expiry.expired} onClick={() => void decide({ kind: "approve", grant: "session" })}>
                Approve for this session
              </Button>
            ) : (
              <Tooltip>
                <TooltipTrigger render={<span tabIndex={0} />}>
                  <Button size="xs" variant="outline" disabled>
                    Approve for this session
                  </Button>
                </TooltipTrigger>
                <TooltipContent>{sessionReason}</TooltipContent>
              </Tooltip>
            )}
            <Button size="xs" variant="destructive" disabled={busy || expiry.expired} onClick={() => void decide({ kind: "deny" })} data-command="approvals.deny">
              Deny
            </Button>
            {!noteOpen ? (
              <Button size="xs" variant="ghost" onClick={() => setNoteOpen(true)}>
                Add note
              </Button>
            ) : null}
            <span id={hintId} className="ml-auto hidden items-center gap-1 text-[11px] text-muted-foreground @xs:flex">
              <Kbd>Enter</Kbd> approve once · <Kbd>Backspace</Kbd> deny
            </span>
          </div>
          {error ? (
            <p role="alert" className="text-destructive">
              {error}
            </p>
          ) : null}
        </>
      ) : null}
    </article>
  );
}
