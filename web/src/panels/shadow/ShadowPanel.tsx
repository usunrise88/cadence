import { useMemo, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Play, Refresh, SoundHigh } from "iconoir-react";
import {
  deploymentsGetOptions,
  deploymentsGetQueryKey,
  deploymentsListOptions,
  shadowReplaysGetOptions,
  shadowReplaysGetQueryKey,
  shadowReplaysListOptions,
  shadowReplaysListQueryKey,
} from "@/api/gen/@tanstack/react-query.gen";
import type { ApprovalAccepted, Deployment, ShadowReplay, ShadowSegment } from "@/api/gen/types.gen";
import { Button } from "@/components/ui/button";
import { NativeSelect } from "@/components/ui/native-select";
import { cn } from "@/lib/utils";
import { openAudio } from "@/shell/audio";
import { AnalyticsChart, type LineSpec } from "@/shell/charts";
import { openDiffSegment } from "@/shell/diff/segment";
import { EmptyState, PanelToolbar, StatusChip } from "@/shell/entity/primitives";
import { errorMessage, formatRate, openDocument, openPanelById, runCommand, useFollowedDoc, useProject, useTopic, type PanelProps } from "@/shell/panel";

// Shadow (docs/spec/11-ui-panels.md "Panel catalogue"; phase 5 · stream D4): a shadow deployment's nightly replays —
// how far this model's transcripts of the night's calls diverge from the comparison model's (production, or the
// project's baseline before any production), night by night with the bootstrap interval, the hours replayed against
// what a canary needs, and the most divergent segments of a night, which open in Diff (the two transcripts aligned)
// and Audio. It follows the active document when that is a deployment (or the shadows of the active Model document),
// else the picker. Live through shadow.{deployment} and deploy.{deployment}. Marking a segment for triage comes with
// the flywheel's triage path (phase 5 · F2).

export function ShadowEmpty() {
  return (
    <EmptyState
      step="review"
      title="No shadow deployment"
      hint="Deploy a model version to shadow (Model document → Deploy to shadow): it replays the night's calls against production and shows here."
    />
  );
}

/** The deployment a followed document names: `deployment:<id>`, or the model version of `model:<id>`. */
export function followedTarget(doc: string | null): { deployment?: string; model?: string } {
  if (doc?.startsWith("deployment:")) return { deployment: doc.slice("deployment:".length) };
  if (doc?.startsWith("model:")) return { model: doc.slice("model:".length) };
  return {};
}

function hours(v: number | undefined): string {
  return v === undefined ? "—" : `${v.toFixed(v < 10 ? 1 : 0)} h`;
}

function nightLabel(r: ShadowReplay): string {
  return r.trigger === "manual" ? `${r.night} (now)` : r.night;
}

/** The divergence per finished night, oldest first, with its interval: the chart's one series. */
export function divergenceSpec(nights: ShadowReplay[]): LineSpec {
  const done = nights.filter((n) => n.state === "done" && n.divergence?.wer !== undefined).reverse();
  return {
    kind: "line",
    title: "Divergence per night",
    xLabel: "Night",
    yLabel: "WER against the comparison model",
    format: (v) => formatRate(v, 1),
    note: "Each point is one night's replay; the bar is its bootstrap interval, resampled by call.",
    series: [
      {
        id: "divergence",
        label: "Divergence",
        points: done.map((n, i) => ({
          x: i + 1,
          y: n.divergence!.wer!,
          ...(n.divergence?.ci ? { low: n.divergence.ci[0], high: n.divergence.ci[1] } : {}),
          label: nightLabel(n),
        })),
      },
    ],
  };
}

export function ShadowPanel({ instanceId }: PanelProps) {
  const project = useProject();
  const { doc } = useFollowedDoc(instanceId);
  const followed = followedTarget(doc);
  const [picked, setPicked] = useState<string | undefined>();
  const list = useQuery({ ...deploymentsListOptions({ path: { p: project ?? "" } }), enabled: !!project });
  const withShadow = useMemo(() => (list.data?.items ?? []).filter((d) => d.shadow !== undefined), [list.data]);
  const candidates = followed.model ? withShadow.filter((d) => d.modelVersionId === followed.model) : withShadow;
  const id = followed.deployment ?? (picked && candidates.some((d) => d.id === picked) ? picked : candidates.find((d) => d.stage === "shadow")?.id ?? candidates[0]?.id);
  return (
    <div className="flex h-full min-h-0 flex-col" data-slot="shadow">
      <PanelToolbar>
        <label htmlFor={`${instanceId}-dep`} className="text-xs text-muted-foreground">
          Deployment
        </label>
        <NativeSelect
          id={`${instanceId}-dep`}
          className="w-auto max-w-80"
          value={id ?? ""}
          disabled={!!followed.deployment || candidates.length === 0}
          onChange={(e) => setPicked(e.target.value || undefined)}
        >
          {followed.deployment && !candidates.some((d) => d.id === followed.deployment) ? <option value={followed.deployment}>{followed.deployment}</option> : null}
          {candidates.length === 0 && !followed.deployment ? <option value="">No shadow deployments</option> : null}
          {candidates.map((d) => (
            <option key={d.id} value={d.id}>
              {d.modelVersion} · {d.profile} · {d.stage}
            </option>
          ))}
        </NativeSelect>
      </PanelToolbar>
      {list.error ? (
        <p role="alert" className="p-3 text-xs text-destructive">
          {errorMessage(list.error)}
        </p>
      ) : null}
      <div className="min-h-0 flex-1 overflow-auto">{id ? <ShadowOf key={id} id={id} /> : list.isLoading ? <p className="p-3 text-xs text-muted-foreground">Loading…</p> : <ShadowEmpty />}</div>
    </div>
  );
}

function ShadowOf({ id }: { id: string }) {
  const qc = useQueryClient();
  const dep = useQuery(deploymentsGetOptions({ path: { id } }));
  const nights = useQuery(shadowReplaysListOptions({ path: { id } }));
  const [night, setNight] = useState<string | undefined>();
  useTopic([`shadow.${id}`, `deploy.${id}`], () => {
    void qc.invalidateQueries({ queryKey: deploymentsGetQueryKey({ path: { id } }) });
    void qc.invalidateQueries({ queryKey: shadowReplaysListQueryKey({ path: { id } }) });
    if (night) void qc.invalidateQueries({ queryKey: shadowReplaysGetQueryKey({ path: { id: night } }) });
  });
  if (dep.isLoading) return <p className="p-3 text-xs text-muted-foreground">Loading…</p>;
  if (dep.error)
    return (
      <p role="alert" className="p-3 text-xs text-destructive">
        {errorMessage(dep.error)}
      </p>
    );
  const d = dep.data;
  if (!d) return <ShadowEmpty />;
  const items = nights.data?.items ?? [];
  const current = night ?? items.find((n) => n.state === "done")?.id;
  return (
    <div className="flex flex-col gap-3 p-3 text-xs">
      <Summary d={d} />
      {items.some((n) => n.state === "done") ? (
        <div className="h-56" data-testid="shadow-chart">
          <AnalyticsChart spec={divergenceSpec(items)} height={220} />
        </div>
      ) : null}
      <Nights items={items} selected={current} onSelect={setNight} loading={nights.isLoading} />
      {current ? <Worst key={current} replayId={current} deployment={d} /> : null}
    </div>
  );
}

function Summary({ d }: { d: Deployment }) {
  const sh = d.shadow;
  const [busy, setBusy] = useState(false);
  const [plan, setPlan] = useState<ShadowReplay | null>(null);
  const [note, setNote] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);
  const share = sh && sh.minHours > 0 ? Math.min(1, sh.hours / sh.minHours) : 0;
  const run = async (dryRun: boolean) => {
    setBusy(true);
    setError(null);
    setNote(null);
    try {
      const res = await runCommand("shadowReplays.new", { deploymentId: d.id, dryRun });
      if (dryRun) setPlan(res as ShadowReplay);
      else {
        setPlan(null);
        setNote("approvalId" in res ? `The replay waits for an approval (${(res as ApprovalAccepted).approvalId}).` : `Replay ${(res as ShadowReplay).id} started.`);
      }
    } catch (err) {
      setError(errorMessage(err));
    } finally {
      setBusy(false);
    }
  };
  return (
    <section aria-label="Shadow progress" className="flex flex-col gap-2 rounded-md border p-3">
      <div className="flex flex-wrap items-center gap-2">
        <button type="button" className="text-[13px] font-medium underline-offset-2 hover:underline" onClick={() => openDocument(`model:${d.modelVersionId}`)}>
          {d.modelVersion}
        </button>
        <span className="text-muted-foreground">
          {d.profile} · {d.targetName}
        </span>
        <StatusChip state={d.stage} />
        {d.state !== "active" ? <StatusChip state={d.state} /> : null}
        <span className="ml-auto flex gap-1">
          <Button size="xs" variant="outline" disabled={busy || d.stage !== "shadow"} onClick={() => void run(true)}>
            <Play aria-hidden />
            Replay now…
          </Button>
        </span>
      </div>
      {sh?.against ? (
        <p className="text-muted-foreground">
          Compared with <span className="text-foreground">{sh.against.label ?? sh.against.versionId}</span>
          {sh.against.kind === "base_model" ? " (base model)" : sh.against.deploymentId ? " (production)" : ""}
        </p>
      ) : null}
      {sh ? (
        <div className="flex flex-col gap-1">
          <div className="flex flex-wrap items-baseline gap-x-3 tabular-nums">
            <span>
              <span className="font-medium">{hours(sh.hours)}</span> of {hours(sh.minHours)} a canary needs
            </span>
            <span className="text-muted-foreground">
              {sh.calls} calls · {sh.nights} nights · {sh.utterances} segments
            </span>
            {sh.divergence?.wer !== undefined ? <span>Divergence {formatRate(sh.divergence.wer, 1)}</span> : null}
            {sh.nextReplayAt ? <span className="text-muted-foreground">Next replay {new Date(sh.nextReplayAt).toLocaleString()}</span> : null}
          </div>
          <div
            className="h-1.5 overflow-hidden rounded-full bg-muted"
            role="progressbar"
            aria-label="Shadow hours toward a canary"
            aria-valuemin={0}
            aria-valuemax={sh.minHours}
            aria-valuenow={Math.round(sh.hours * 10) / 10}
          >
            <div className={cn("h-full", share >= 1 ? "bg-status-done" : "bg-status-running")} style={{ width: `${Math.round(share * 100)}%` }} />
          </div>
        </div>
      ) : null}
      {plan ? (
        <div className="flex flex-wrap items-center gap-2 rounded bg-tool p-2" data-testid="replay-plan">
          <span>
            {plan.calls ?? 0} calls, {hours(plan.hours)} against {plan.against?.label ?? "the comparison model"}
            {plan.estimate?.gpuHours !== undefined ? `, about ${plan.estimate.gpuHours.toFixed(2)} GPU-hours` : ""}
          </span>
          <Button size="xs" disabled={busy} onClick={() => void run(false)}>
            Start replay
          </Button>
          <Button size="xs" variant="ghost" onClick={() => setPlan(null)}>
            Cancel
          </Button>
        </div>
      ) : null}
      {note ? (
        <p role="status" className="text-muted-foreground">
          {note}{" "}
          {note.includes("approval") ? (
            <button type="button" className="underline" onClick={() => openPanelById("approvals")}>
              Open Approvals
            </button>
          ) : null}
        </p>
      ) : null}
      {error ? (
        <p role="alert" className="text-destructive">
          {error}
        </p>
      ) : null}
    </section>
  );
}

function Nights({ items, selected, onSelect, loading }: { items: ShadowReplay[]; selected?: string; onSelect: (id: string) => void; loading: boolean }) {
  if (loading) return <p className="text-muted-foreground">Loading nights…</p>;
  if (items.length === 0) return <p className="text-muted-foreground">No night replayed yet.</p>;
  return (
    <div className="overflow-auto rounded-md border">
      <table aria-label="Nights" className="w-full text-xs">
        <thead className="bg-tool text-left text-muted-foreground">
          <tr>
            {["Night", "State", "Calls", "Hours", "Segments", "Divergence", "Note"].map((h) => (
              <th key={h} scope="col" className="px-2 py-1.5 font-normal whitespace-nowrap">
                {h}
              </th>
            ))}
          </tr>
        </thead>
        <tbody className="divide-y">
          {items.map((n) => (
            <tr key={n.id} aria-selected={n.id === selected} className={cn(n.id === selected && "bg-selected")}>
              <td className="px-2 py-1.5 whitespace-nowrap">
                {n.state === "done" ? (
                  <button type="button" className="underline-offset-2 hover:underline" onClick={() => onSelect(n.id)}>
                    {nightLabel(n)}
                  </button>
                ) : (
                  nightLabel(n)
                )}
              </td>
              <td className="px-2 py-1.5">
                <StatusChip state={n.state} />
              </td>
              <td className="px-2 py-1.5 tabular-nums">{n.calls ?? 0}</td>
              <td className="px-2 py-1.5 tabular-nums">{hours(n.hours)}</td>
              <td className="px-2 py-1.5 tabular-nums">{n.utterances ?? 0}</td>
              <td className="px-2 py-1.5 tabular-nums">
                {n.divergence?.wer !== undefined ? formatRate(n.divergence.wer, 1) : "—"}
                {n.divergence?.ci ? <span className="text-muted-foreground"> [{n.divergence.ci.map((v) => formatRate(v, 1)).join(", ")}]</span> : null}
              </td>
              <td className="px-2 py-1.5 text-muted-foreground">{n.textsEvictedAt ? "Texts cleared after retention" : (n.reason ?? "")}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

function segmentLabel(s: ShadowSegment): string {
  const call = (s.call ?? s.uri ?? s.audio).replace(/^mount:\/\/[^/]+\//, "").split("/").pop() ?? s.audio;
  return s.start !== undefined && s.end !== undefined ? `${call} ${s.start.toFixed(1)}–${s.end.toFixed(1)} s` : call;
}

function Worst({ replayId, deployment }: { replayId: string; deployment: Deployment }) {
  const q = useQuery(shadowReplaysGetOptions({ path: { id: replayId } }));
  const r = q.data;
  if (q.isLoading) return <p className="text-muted-foreground">Loading the night's segments…</p>;
  if (q.error)
    return (
      <p role="alert" className="text-destructive">
        {errorMessage(q.error)}
      </p>
    );
  if (!r) return null;
  if (r.textsEvictedAt) return <p className="text-muted-foreground">The night's texts and audio were cleared after deploy.shadow_artifact_retention_days; its summary stays.</p>;
  const rows = r.worst ?? [];
  const against = r.against?.label ?? "comparison model";
  const open = (s: ShadowSegment) => {
    openDiffSegment({
      audio: s.audio,
      ref: s.current ?? "",
      hyp: s.candidate ?? "",
      refLabel: against,
      hypLabel: deployment.modelVersion,
      label: `${segmentLabel(s)} · ${nightLabel(r)}`,
      wer: s.wer,
      start: s.start,
      end: s.end,
      duration: s.duration,
    });
    openAudio({ utterance: s.audio });
  };
  return (
    <section aria-label={`Most divergent segments of ${r.night}`} className="flex flex-col gap-1">
      <div className="flex items-center gap-2">
        <h3 className="text-[13px] font-medium">Most divergent segments · {nightLabel(r)}</h3>
        <Button size="icon-xs" variant="ghost" aria-label="Reload the night" className="ml-auto" onClick={() => void q.refetch()}>
          <Refresh aria-hidden />
        </Button>
      </div>
      {rows.length === 0 ? (
        <p className="text-muted-foreground">Both models wrote the same words on every segment.</p>
      ) : (
        <ol className="flex flex-col divide-y rounded-md border" data-testid="shadow-worst">
          {rows.map((s, i) => (
            <li key={`${s.audio}-${i}`} className="flex items-start gap-2 p-2">
              <div className="min-w-0 flex-1">
                <div className="flex items-baseline gap-2">
                  <span className="truncate text-muted-foreground" title={s.call}>
                    {segmentLabel(s)}
                  </span>
                  <span className="ml-auto shrink-0 tabular-nums">WER {formatRate(s.wer, 1)}</span>
                </div>
                <p dir="auto" className="break-words">
                  <span className="text-muted-foreground">{against}: </span>
                  {s.current}
                </p>
                <p dir="auto" className="break-words">
                  <span className="text-muted-foreground">{deployment.modelVersion}: </span>
                  {s.candidate}
                </p>
              </div>
              <Button size="xs" variant="outline" onClick={() => open(s)} aria-label={`Open ${segmentLabel(s)} in Diff and Audio`}>
                <SoundHigh aria-hidden />
                Diff
              </Button>
            </li>
          ))}
        </ol>
      )}
    </section>
  );
}
