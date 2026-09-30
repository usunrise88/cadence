import { useEffect, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { runsListOptions } from "@/api/gen/@tanstack/react-query.gen";
import { useFollowedDoc } from "@/shell/selection/store";
import { runIdOfDoc } from "./runs";

/**
 * The run a tool panel shows (Metrics, Checkpoints: "of the active run"): the Run document it follows (the active
 * document, or the one it is pinned to); after the focus moves to another kind of document, the last run it showed;
 * before any, the project's newest run.
 */
export function useActiveRun(instanceId: string, project: string | undefined): { runId: string | undefined; pinned: boolean } {
  const { doc, pinned } = useFollowedDoc(instanceId);
  const followed = runIdOfDoc(doc);
  const [last, setLast] = useState<string | undefined>(followed);
  useEffect(() => {
    if (followed) setLast(followed);
  }, [followed]);
  const newest = useQuery({ ...runsListOptions({ path: { p: project ?? "" }, query: { limit: 1 } }), enabled: !!project && !followed && !last });
  return { runId: followed ?? last ?? newest.data?.items[0]?.id, pinned: pinned && !!followed };
}
