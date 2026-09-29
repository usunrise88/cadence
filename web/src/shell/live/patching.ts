import { useEffect } from "react";
import { useQueryClient, type QueryClient } from "@tanstack/react-query";
import type { CadenceEvent, Project } from "@/api/gen/types.gen";
import { projectsGetQueryKey, projectsListQueryKey } from "@/api/gen/@tanstack/react-query.gen";
import { events } from "@/shell/registries";

// Cache patching: events carry the resulting entity, so the query cache is updated in place instead of refetched
// (docs/spec/06-platform.md "Real-time model"). Kinds are added here as their operations land.

export function patchCache(qc: QueryClient, batch: CadenceEvent[]): void {
  let listStale = false;
  for (const e of batch) {
    const project = (e.payload as { project?: Project } | undefined)?.project;
    if (e.entity?.kind === "project") {
      if (project) qc.setQueryData(projectsGetQueryKey({ path: { p: project.slug } }), project);
      listStale = true;
    }
  }
  if (listStale) void qc.invalidateQueries({ queryKey: projectsListQueryKey() });
}

/** The shell's own subscription (chrome, not a panel): keeps project lists and headers live. */
export function useCachePatching(): void {
  const qc = useQueryClient();
  useEffect(() => events.subscribe(["entity.project.*"], (b) => patchCache(qc, b), "shell"), [qc]);
}
