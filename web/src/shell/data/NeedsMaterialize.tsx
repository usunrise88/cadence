import { useState } from "react";
import { Download } from "iconoir-react";
import type { NeedsMaterialize as Needs, PipelineWarning } from "@/api/gen/types.gen";
import { Button } from "@/components/ui/button";
import { openDocument, openPanelById } from "@/shell/panel/actions";
import { errorMessage, runCommand } from "@/shell/panel/commands";
import { useTopic } from "@/shell/panel/context";

// "Needs materialize" (phase 4 tail): a training step would read a dataset version whose shards the cache evicted. A
// dry run of runs.new, runs.stage or pipelines.run answers it as a plan warning (needs-materialize); a pipeline run
// that is not done lists it (pipelineRuns.get needsMaterialize). The real call is refused (artifact-missing) until
// datasets.materialize brings the version back, so the notice offers exactly that command.

/** The needs-materialize entries of a plan's warnings, once per dataset version. */
export function materializeOf(warnings: PipelineWarning[] | undefined): Needs[] {
  const out: Needs[] = [];
  for (const w of warnings ?? []) {
    if (w.code === "needs-materialize" && w.materialize && !out.some((m) => m.versionId === w.materialize!.versionId)) out.push(w.materialize);
  }
  return out;
}

export function formatBytes(n: number): string {
  if (n >= 1e9) return `${(n / 1e9).toFixed(2)} GB`;
  if (n >= 1e6) return `${(n / 1e6).toFixed(1)} MB`;
  return `${Math.max(0, Math.round(n / 1e3))} kB`;
}

export type NeedsMaterializeProps = {
  items: Needs[];
  /** What the refusal blocks: "the run", "a retry", "the stage". */
  blocks?: string;
  /** Called when a version is back in the cache (its artifact's restore event), e.g. to estimate again. */
  onRestored?: (versionId: string) => void;
};

export function NeedsMaterialize({ items, blocks = "the run", onRestored }: NeedsMaterializeProps) {
  if (!items.length) return null;
  return (
    <div role="group" aria-label="Needs materialize" className="flex flex-col gap-1.5 rounded-md border border-status-warning bg-tool p-2 text-xs" data-slot="needs-materialize">
      <p className="font-medium text-status-warning-foreground">
        <span aria-hidden>⚠ </span>Needs materialize: training reads only what the cache holds, so {blocks} is refused until {items.length === 1 ? "this dataset version is" : "these dataset versions are"} back.
      </p>
      <ul className="flex flex-col gap-1">
        {items.map((m) => (
          <Entry key={m.versionId} m={m} onRestored={onRestored} />
        ))}
      </ul>
    </div>
  );
}

function Entry({ m, onRestored }: { m: Needs; onRestored?: (versionId: string) => void }) {
  const [busy, setBusy] = useState(false);
  const [state, setState] = useState<{ error: boolean; text: string } | null>(null);
  // The copy back clears the artifact's eviction (entity.artifact.{hash}).
  useTopic(state && !state.error ? [`entity.artifact.${m.artifact}`] : null, () => {
    setState({ error: false, text: "Back in the cache." });
    onRestored?.(m.versionId);
  });
  const materialize = async () => {
    setBusy(true);
    setState(null);
    try {
      const res = await runCommand("datasets.materialize", { versionId: m.versionId });
      setState({ error: false, text: "jobId" in res ? `Materializing (job ${res.jobId}): copying back from the mount.` : "Nothing to copy." });
    } catch (err) {
      setState({ error: true, text: errorMessage(err) });
    } finally {
      setBusy(false);
    }
  };
  const name = m.collection ? `${m.collection} ${m.version ?? ""}` : m.versionId;
  return (
    <li className="flex flex-col gap-1" data-version={m.versionId}>
      <div className="flex flex-wrap items-center gap-x-2 gap-y-1">
        <button type="button" className="font-medium underline-offset-2 hover:underline" onClick={() => openDocument(`dataset_version:${m.versionId}`)}>
          {name}
        </button>
        {m.input ? <span className="text-muted-foreground">input {m.input}</span> : null}
        <span className="tabular-nums" data-slot="copy-bytes">
          {formatBytes(m.copyBytes)} to copy back ({m.copyShards} of {m.shards} shard{m.shards === 1 ? "" : "s"})
          {m.from.length ? ` from ${m.from.map((f) => f.mount).join(", ")}` : ""}
        </span>
        <span className="ml-auto flex gap-1">
          <Button size="xs" variant="outline" disabled={busy || m.missing > 0 || (!!state && !state.error)} onClick={() => void materialize()} data-command="datasets.materialize">
            <Download aria-hidden />
            Materialize
          </Button>
          <Button size="xs" variant="ghost" onClick={() => openPanelById("storage")}>
            Storage
          </Button>
        </span>
      </div>
      {m.missing > 0 ? (
        <p className="text-destructive">
          {m.missing} shard{m.missing === 1 ? " is" : "s are"} on no mount and in no cache: materialize cannot bring {m.missing === 1 ? "it" : "them"} back; freeze or import the data again.
        </p>
      ) : null}
      {state ? (
        <p role={state.error ? "alert" : "status"} className={state.error ? "text-destructive" : "text-muted-foreground"}>
          {state.text}
        </p>
      ) : null}
    </li>
  );
}
