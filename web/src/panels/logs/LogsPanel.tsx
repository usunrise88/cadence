import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { Pin, PinSolid } from "iconoir-react";
import { jobsGetOptions } from "@/api/gen/@tanstack/react-query.gen";
import { Button } from "@/components/ui/button";
import { EmptyState, PanelToolbar, StatusChip } from "@/shell/entity/primitives";
import { LogView, useFocusedJob, type FocusedJob, type PanelProps } from "@/shell/panel";

// Logs (docs/spec/11-ui-panels.md "Panel catalogue"): the streaming log of the active job — the one focused in Queue &
// GPU or Pipeline run (later the Run document) — with follow, search and copy. Pin keeps the current job while the
// focus moves on.

export function LogsEmpty() {
  return <EmptyState step="run" title="No job selected" hint="Pick a job in Queue & GPU or a step in Pipeline run; its log streams here while it runs." />;
}

export function LogsPanel(_props: PanelProps) {
  const focused = useFocusedJob();
  const [pinned, setPinned] = useState<FocusedJob | null>(null);
  const job = pinned ?? focused;
  if (!job) return <LogsEmpty />;
  return (
    <div className="flex h-full min-h-0 flex-col">
      <PanelToolbar>
        <JobTitle job={job} />
        <Button
          size="icon-xs"
          variant="ghost"
          className="ml-auto size-6"
          aria-pressed={!!pinned}
          aria-label={pinned ? "Unpin: follow the active job" : "Pin this job"}
          title={pinned ? "Follow the active job again" : "Keep this job while the focus moves"}
          onClick={() => setPinned(pinned ? null : job)}
        >
          {pinned ? <PinSolid aria-hidden /> : <Pin aria-hidden />}
        </Button>
      </PanelToolbar>
      <LogView key={job.id} jobId={job.id} label={`Log of ${job.label ?? job.id}`} className="min-h-0 flex-1" />
    </div>
  );
}

function JobTitle({ job }: { job: FocusedJob }) {
  const q = useQuery({ ...jobsGetOptions({ path: { id: job.id } }), refetchInterval: (query) => (query.state.data && ["queued", "running"].includes(query.state.data.state) ? 5000 : false) });
  return (
    <span className="flex min-w-0 items-center gap-2 text-xs">
      <span className="truncate text-[13px] font-medium" title={job.id}>
        {job.label ?? job.id}
      </span>
      {q.data ? <StatusChip state={q.data.pausedAt && q.data.state === "queued" ? "paused" : q.data.state} /> : null}
      {q.data?.message ? (
        <span className="truncate text-muted-foreground" title={q.data.message}>
          {q.data.message}
        </span>
      ) : null}
    </span>
  );
}
