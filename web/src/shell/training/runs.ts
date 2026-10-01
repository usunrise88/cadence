import type { Run } from "@/api/gen/types.gen";

// How a training run is named wherever it appears (Run header, Metrics and Checkpoints pickers, links).

/** "he-first r3 · from base · 0192ab34" */
export function runLabel(r: Pick<Run, "id" | "mix" | "init">): string {
  return `${r.mix.name} r${r.mix.revision} · ${r.init === "checkpoint" ? "stage" : "from base"} · ${r.id.slice(4, 12)}`;
}

/** The run a document reference names (`run:<id>`), if it names one. */
export function runIdOfDoc(doc: string | null | undefined): string | undefined {
  return doc?.startsWith("run:") ? doc.slice(4) : undefined;
}

export const RUN_ENDED = ["done", "failed", "cancelled"] as const;
