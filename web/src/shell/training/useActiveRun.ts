import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { runsListOptions } from "@/api/gen/@tanstack/react-query.gen";
import { useFollowedDoc } from "@/shell/selection/store";
import { runIdOfDoc } from "./runs";

/**
 * The run a tool panel shows (Metrics, Checkpoints: "of the active run"): the Run document it follows (the active
 * document, or the one it is pinned to); after the focus moves to another kind of document, the last run it showed in
 * the current project; before any (or after the project changes), the project's newest run.
 */
export function useActiveRun(instanceId: string, project: string | undefined): { runId: string | undefined; pinned: boolean } {
  const { doc, pinned } = useFollowedDoc(instanceId);
  const followed = runIdOfDoc(doc);
  // The last followed run, remembered with the project it was followed in: another project never falls back to it.
  const [last, setLast] = useState<{ project: string | undefined; runId: string } | undefined>(followed ? { project, runId: followed } : undefined);
  if (followed && last?.runId !== followed) setLast({ project, runId: followed });
  const lastRun = last && last.project === project ? last.runId : undefined;
  const newest = useQuery({ ...runsListOptions({ path: { p: project ?? "" }, query: { limit: 1 } }), enabled: !!project && !followed && !lastRun });
  return { runId: followed ?? lastRun ?? newest.data?.items[0]?.id, pinned: pinned && !!followed };
}
