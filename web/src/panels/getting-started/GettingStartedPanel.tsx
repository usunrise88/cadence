import { useEffect } from "react";
import { useQuery } from "@tanstack/react-query";
import { Check, Circle, Clock } from "iconoir-react";
import { authGetOptions, datasetsListOptions, evalsListOptions, projectsListOptions, runsListOptions } from "@/api/gen/@tanstack/react-query.gen";
import { Button } from "@/components/ui/button";
import { cn } from "@/lib/utils";
import { EmptyState, PanelToolbar } from "@/shell/entity/primitives";
import { closePanelInstance, runCommand, useCommand, useProject, useTopic, type PanelProps } from "@/shell/panel";
import { retired, setDismissed, setupSteps, type SetupStep } from "./steps";

// Getting started (docs/spec/11-ui-panels.md "First run and progressive disclosure"): the setup checklist, ticking
// itself from existing data, each step with its command. Dismissing it keeps it out of the default workspaces; it
// stays reachable from View → Open Getting started.

export function GettingStartedEmpty() {
  return <EmptyState step="prepare" title="Getting started" hint="The setup checklist appears here." />;
}

const ICON = { done: Check, todo: Circle, later: Clock } as const;

export function GettingStartedPanel({ instanceId }: PanelProps) {
  const auth = useQuery({ ...authGetOptions(), staleTime: Infinity });
  const projects = useQuery(projectsListOptions());
  const datasets = useQuery(datasetsListOptions({ query: { state: "frozen" } }));
  const project = useProject();
  const runs = useQuery({ ...runsListOptions({ path: { p: project ?? "" }, query: { status: "done", limit: 1 } }), enabled: !!project });
  const evals = useQuery({ ...evalsListOptions({ path: { p: project ?? "" }, query: { limit: 50 } }), enabled: !!project });
  useTopic(["entity.project.*", "entity.dataset_version.*", "entity.run.*", "entity.eval.*"], () => {
    void projects.refetch();
    void datasets.refetch();
    if (project) {
      void runs.refetch();
      void evals.refetch();
    }
  });
  const facts = {
    signedIn: !!auth.data?.actor,
    projects: projects.data?.items.length ?? 0,
    datasets: datasets.data?.items ?? [],
    runs: project ? (runs.data?.items ?? []) : undefined,
    evals: project ? (evals.data?.items ?? []) : undefined,
  };
  const steps = setupSteps(facts);
  const done = steps.filter((s) => s.state === "done").length;
  const over = retired(facts);
  // The first gate passed: the checklist retires and stays out of the default workspaces from now on.
  useEffect(() => {
    if (over) setDismissed(true);
  }, [over]);
  return (
    <div className="flex h-full min-h-0 flex-col">
      <PanelToolbar>
        <span className="text-xs text-muted-foreground" aria-live="polite">
          {done} of {steps.length} done
        </span>
        <div aria-hidden className="h-1.5 flex-1 overflow-hidden rounded-full bg-hover">
          <div className="h-full bg-primary" style={{ width: `${(done / steps.length) * 100}%` }} />
        </div>
        <Button
          size="xs"
          variant="ghost"
          onClick={() => {
            setDismissed(true);
            closePanelInstance(instanceId);
          }}
        >
          Dismiss
        </Button>
      </PanelToolbar>
      {over ? (
        <p role="status" className="border-b px-3 py-2 text-xs text-status-done-foreground" data-slot="setup-retired">
          The first gate passed: setup is complete, and this checklist no longer opens with the workspaces. It stays under View → Open Getting started.
        </p>
      ) : null}
      <ol className="min-h-0 flex-1 overflow-auto p-2" aria-label="Setup steps">
        {steps.map((s, i) => (
          <StepRow key={s.id} step={s} n={i + 1} />
        ))}
      </ol>
    </div>
  );
}

function StepRow({ step: s, n }: { step: SetupStep; n: number }) {
  const cmd = useCommand(s.command);
  const Icon = ICON[s.state];
  return (
    <li className="flex gap-2 border-b px-1 py-2 last:border-0" data-step={s.id} data-state={s.state}>
      <span
        aria-hidden
        className={cn(
          "mt-0.5 flex size-5 shrink-0 items-center justify-center rounded-full border [&_svg]:size-3",
          s.state === "done" ? "border-status-done text-status-done-foreground" : s.state === "later" ? "text-muted-foreground" : "border-accent-line text-accent-text",
        )}
      >
        <Icon />
      </span>
      <div className="min-w-0 flex-1 text-xs">
        <p className={cn("text-[13px] font-medium", s.state === "later" && "text-muted-foreground")}>
          {n}. {s.title}
          <span className="sr-only"> — {s.state === "done" ? "done" : s.state === "later" ? `arrives in phase ${s.phase}` : "to do"}</span>
        </p>
        <p className="text-muted-foreground">{s.detail}</p>
        <p className="mt-1 flex flex-wrap items-center gap-2">
          <code className="rounded bg-hover px-1 font-mono text-[11px]">{s.command}</code>
          {s.state === "later" ? <span className="rounded-full border px-1.5 text-[11px] text-muted-foreground">phase {s.phase}</span> : null}
          {s.state === "done" ? <span className="text-status-done-foreground">Done</span> : null}
          {s.state === "todo" && cmd && s.blocked ? <span className="text-muted-foreground">{s.blocked}</span> : null}
          {s.state === "todo" && cmd && !s.blocked ? (
            <Button
              size="xs"
              variant="outline"
              disabled={cmd.enabled !== true}
              title={cmd.enabled === true ? undefined : cmd.enabled}
              onClick={() => void (s.args && s.command === "evals.gate" ? runCommand("evals.gate", s.args) : cmd.run())}
            >
              {cmd.title}
            </Button>
          ) : null}
        </p>
      </div>
    </li>
  );
}
