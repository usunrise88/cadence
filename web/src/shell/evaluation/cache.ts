import type { QueryClient } from "@tanstack/react-query";

// Live updates of evaluation reads: every evals.get of one eval (any ?worst/?cell variant) and the evals lists.
// entity.eval.{id} (created, status_changed, gated) and eval.{id}.progress carry summaries only, so the reads are
// refetched rather than patched.

type KeyHead = { _id?: string; path?: { id?: string } } | undefined;

export function invalidateEval(qc: QueryClient, id: string): void {
  void qc.invalidateQueries({
    predicate: (q) => {
      const k = q.queryKey[0] as KeyHead;
      return (k?._id === "evalsGet" && k.path?.id === id) || k?._id === "evalsList";
    },
  });
}

/** Every read of a language pack (and the pack list) after a commit or a recipe.* event. */
export function invalidateLangpacks(qc: QueryClient): void {
  void qc.invalidateQueries({
    predicate: (q) => {
      const id = (q.queryKey[0] as KeyHead)?._id;
      return id === "langpacksGet" || id === "langpacksList";
    },
  });
}
