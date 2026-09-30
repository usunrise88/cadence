import { Check, Circle, Xmark } from "iconoir-react";
import type { AgentPlaybook, PlaybookPlanItem } from "@/api/gen/types.gen";
import { cn } from "@/lib/utils";
import { budgetNote, estimateLine, planProgress, playbookStateLabel } from "@/shell/panel";

// A playbook session's plan in Chat (docs/spec/05-agents.md "What the Chat panel shows": a playbook's chain is the
// initial plan): the server ticks each step when the session's command for it succeeds and the ticks arrive with the
// session on agent.session.{id}; the estimate comes first; a stop or the end shows the summary and the next step.

const ITEM_LABEL: Record<PlaybookPlanItem["state"], string> = {
  pending: "to do",
  running: "in progress",
  done: "done",
  failed: "failed",
  skipped: "skipped",
};

function Mark({ state }: { state: PlaybookPlanItem["state"] }) {
  return (
    <span aria-label={ITEM_LABEL[state]} role="img" className="mt-0.5 inline-flex size-4 shrink-0 items-center justify-center">
      {state === "done" ? (
        <Check aria-hidden className="size-4 text-status-done-foreground" />
      ) : state === "failed" ? (
        <Xmark aria-hidden className="size-4 text-status-failed-foreground" />
      ) : state === "running" ? (
        <span aria-hidden className="size-2.5 rounded-full bg-status-running motion-safe:animate-pulse" />
      ) : state === "skipped" ? (
        <span aria-hidden className="h-px w-2.5 bg-muted-foreground" />
      ) : (
        <Circle aria-hidden className="size-3.5 text-muted-foreground" />
      )}
    </span>
  );
}

export function PlaybookPlan({ playbook }: { playbook: AgentPlaybook }) {
  const { done, total } = planProgress(playbook.plan);
  const budget = budgetNote(playbook.estimate);
  return (
    <details open className="group shrink-0 border-b px-3 py-2 text-xs" data-slot="playbook-plan" data-state={playbook.state}>
      <summary className="flex cursor-pointer list-none items-center gap-2 [&::-webkit-details-marker]:hidden">
        <span className="text-[11px] font-medium tracking-wide text-muted-foreground uppercase">Plan</span>
        <span className="min-w-0 truncate font-medium">{playbook.title}</span>
        <span
          className={cn(
            "ml-auto shrink-0 tabular-nums",
            playbook.state === "done" && "text-status-done-foreground",
            playbook.state === "stopped" && "text-status-warning-foreground",
          )}
          aria-label={`Plan, ${done} of ${total} done`}
        >
          {playbookStateLabel(playbook)}
        </span>
      </summary>
      <p className="mt-1 text-[11px] text-muted-foreground" data-slot="playbook-estimate">
        Estimate: {estimateLine(playbook.estimate)}
        {budget ? ` — ${budget}` : ""}
      </p>
      <ol className="mt-1 flex flex-col gap-0.5 text-[13px]" aria-label="Playbook plan">
        {playbook.plan.map((it) => (
          <li key={it.id} data-item={it.id} data-state={it.state} className="flex items-start gap-2">
            <Mark state={it.state} />
            <span className="min-w-0">
              <span
                className={cn(
                  it.state === "done" && "text-muted-foreground",
                  it.state === "running" && "font-medium",
                  it.state === "skipped" && "text-muted-foreground line-through",
                )}
              >
                {it.title}
              </span>{" "}
              <code className="text-[11px] text-muted-foreground">{it.command}</code>
              {it.note ? (
                <span className={cn("block text-[11px] text-muted-foreground", it.state === "failed" && "text-status-failed-foreground")}>{it.note}</span>
              ) : null}
            </span>
          </li>
        ))}
      </ol>
      {playbook.state !== "running" && playbook.summary ? (
        <div role="status" className="mt-1.5 rounded-md border bg-background px-2 py-1.5" data-slot="playbook-summary">
          <p>{playbook.summary}</p>
          {playbook.next ? <p className="mt-1 font-medium">Next: {playbook.next}</p> : null}
        </div>
      ) : null}
    </details>
  );
}
