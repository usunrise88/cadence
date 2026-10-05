import { useMemo, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { deploymentTargetsListOptions } from "@/api/gen/@tanstack/react-query.gen";
import type { ApprovalAccepted, ModelExport, ModelVersion, PipelineEstimate, PipelineRun } from "@/api/gen/types.gen";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { NativeSelect } from "@/components/ui/native-select";
import { AnalyticsChart, type AnalyticsSpec } from "@/shell/charts";
import { errorMessage, runCommand, useProject } from "@/shell/panel";
import { estimateText, Message, ms, pct, Section, shortHash, Verdict } from "./ui";

// Exports of a model version (02 "Model exports"): one deployable per latency profile and format, with its newest
// parity check (R31) and its benchmarks, newest first. Export, Parity check and Benchmark each dry-run first (the
// steps and the estimate), then start the generated pipeline (or answer the approval a GPU budget asks for).

type Check = "models.export" | "models.parity" | "models.benchmark";
type Planned = { estimate?: PipelineEstimate; pipelineRun?: PipelineRun; steps?: { step: string }[]; profiles?: { profile: string; action: string }[] };

const isApproval = (r: unknown): r is ApprovalAccepted => !!r && typeof r === "object" && "approvalId" in r;

/** The benchmark chart: p95 chunk latency at the verdict's concurrency of every finished benchmark, beside its budget. */
export function benchmarkSpec(exports: ModelExport[]): AnalyticsSpec | undefined {
  const rows = exports.flatMap((x) =>
    x.benchmarks
      .filter((b) => b.state === "done" && b.p95ChunkLatencyMs !== undefined)
      .map((b) => ({ x, b, at: b.finishedAt ?? b.createdAt ?? "" })),
  );
  if (!rows.length) return undefined;
  rows.sort((a, b) => a.at.localeCompare(b.at));
  return {
    kind: "bar",
    title: "p95 chunk latency against the budget",
    yLabel: "Latency",
    unit: "ms",
    categories: rows.map(({ x, b, at }) => `${x.profile} · ${b.streams} streams${at ? ` · ${at.slice(0, 10)}` : ""}${b.contended ? " (contended)" : ""}`),
    series: [
      { id: "p95", label: "p95 chunk latency", slot: 0, values: rows.map(({ b }) => b.p95ChunkLatencyMs ?? null) },
      { id: "budget", label: "Budget", slot: 1, values: rows.map(({ b }) => b.budgetMs) },
    ],
    note: "Each bar is one benchmark at its verdict's concurrency (the target's, else deploy.target_concurrency).",
  };
}

function ExportRow({ x }: { x: ModelExport }) {
  const p = x.parity;
  const b = x.benchmarks[0];
  return (
    <li className="flex flex-col gap-0.5 rounded-md border p-2" data-export={x.id} data-state={x.state}>
      <p className="flex flex-wrap items-center gap-2">
        <span className="font-medium">{x.profile}</span>
        <span className="text-muted-foreground">{x.format}</span>
        <Verdict value={x.state} />
        {x.deployableHash ? <code className="text-[11px] text-muted-foreground">{shortHash(x.deployableHash)}</code> : null}
      </p>
      {x.error ? <p className="text-destructive">{x.error}</p> : null}
      <p data-slot="export-parity">
        <span className="text-muted-foreground">Parity: </span>
        {p ? (
          <>
            <Verdict value={p.state} />
            {p.werDelta !== undefined ? <span> · ΔWER {(p.werDelta * 100).toFixed(2)} points</span> : null}
            {p.identicalShare !== undefined ? <span> · {pct(p.identicalShare)} identical ({p.compared ?? "text"})</span> : null}
            {p.reasons?.length ? <span className="text-muted-foreground"> · {p.reasons.join("; ")}</span> : null}
            {p.error ? <span className="text-destructive"> · {p.error}</span> : null}
          </>
        ) : (
          <span className="text-muted-foreground">not checked</span>
        )}
      </p>
      <p data-slot="export-benchmark">
        <span className="text-muted-foreground">Benchmark: </span>
        {b ? (
          <>
            <Verdict value={b.verdict ?? b.state} />
            <span>
              {" "}
              · p95 {ms(b.p95ChunkLatencyMs)} at {b.streams} streams (budget {b.budgetMs} ms)
            </span>
            {b.maxStreamsWithinBudget !== undefined ? <span> · {b.maxStreamsWithinBudget} streams per card</span> : null}
            {b.cardClass ? <span className="text-muted-foreground"> · {b.cardClass}</span> : null}
            {b.contended ? <span className="text-muted-foreground"> · contended</span> : null}
          </>
        ) : (
          <span className="text-muted-foreground">not run</span>
        )}
      </p>
    </li>
  );
}

export function ExportsSection({ m }: { m: ModelVersion }) {
  const project = useProject();
  const exports = useMemo(() => m.exports ?? [], [m.exports]);
  const spec = useMemo(() => benchmarkSpec(exports), [exports]);
  const targets = useQuery({ ...deploymentTargetsListOptions(), enabled: !!project });
  const delivery = (targets.data?.items ?? []).filter((t) => t.kind === "delivery" && t.state === "active");
  const [op, setOp] = useState<Check | null>(null);
  const [profile, setProfile] = useState("");
  const [target, setTarget] = useState("");
  const [plan, setPlan] = useState<Planned | null>(null);
  const [busy, setBusy] = useState(false);
  const [message, setMessage] = useState<{ error: boolean; text: string } | null>(null);

  const call = (p: string, o: Check, dryRun: boolean): Promise<unknown> => {
    const prof = profile.trim();
    switch (o) {
      case "models.export":
        return runCommand(o, { project: p, body: { version: m.id, ...(prof ? { profiles: [prof] } : {}) }, dryRun });
      case "models.parity":
        return runCommand(o, { project: p, body: { version: m.id, ...(prof ? { profile: prof } : {}) }, dryRun });
      default:
        return runCommand(o, { project: p, body: { version: m.id, ...(prof ? { profile: prof } : {}), ...(target ? { target } : {}) }, dryRun });
    }
  };
  const run = async (dryRun: boolean) => {
    if (!project || !op) return;
    setBusy(true);
    setMessage(null);
    try {
      const res = await call(project, op, dryRun);
      if (isApproval(res)) {
        setPlan(null);
        setMessage({ error: false, text: `Waiting for an approval (${res.approvalId}): the GPU time needs a person (Approvals).` });
      } else if (dryRun) {
        setPlan(res as Planned);
      } else {
        const r = res as Planned;
        setPlan(null);
        setOp(null);
        setMessage({ error: false, text: r.pipelineRun ? `Started pipeline run ${r.pipelineRun.id}.` : "Nothing to run: the export exists already." });
      }
    } catch (err) {
      setMessage({ error: true, text: errorMessage(err) });
    } finally {
      setBusy(false);
    }
  };
  const open = (o: Check) => {
    setOp(o);
    setPlan(null);
    setMessage(null);
  };
  const LABEL: Record<Check, string> = { "models.export": "Export", "models.parity": "Parity check", "models.benchmark": "Benchmark" };

  return (
    <Section
      id={`model-exports-${m.id}`}
      title="Exports"
      slot="model-exports"
      actions={
        project
          ? (Object.keys(LABEL) as Check[]).map((o) => (
              <Button key={o} size="xs" variant="outline" onClick={() => open(o)} data-command={o}>
                {LABEL[o]}…
              </Button>
            ))
          : null
      }
    >
      {exports.length ? (
        <ul className="flex flex-col gap-1.5">
          {exports.map((x) => (
            <ExportRow key={x.id} x={x} />
          ))}
        </ul>
      ) : (
        <p className="text-muted-foreground">No export yet: Export writes one deployable per latency profile.</p>
      )}
      {spec ? (
        <figure className="flex min-w-0 flex-col gap-1" data-chart="model-benchmarks">
          <figcaption className="text-[11px] text-muted-foreground">{spec.title}</figcaption>
          <div className="h-48">
            <AnalyticsChart spec={spec} hideTitle />
          </div>
        </figure>
      ) : null}
      {!project ? <p className="text-muted-foreground">Open a project to export, check or benchmark: its GPU budget pays for them.</p> : null}
      {op ? (
        <div className="flex flex-col gap-2 rounded-md border bg-muted/30 p-2" data-slot="model-check-form" data-op={op}>
          <div className="flex flex-wrap items-end gap-2">
            <label className="flex flex-col gap-0.5">
              <span className="text-muted-foreground">Latency profile</span>
              <Input className="h-7 w-32 text-xs" value={profile} placeholder="primary" onChange={(e) => setProfile(e.target.value)} aria-label="Latency profile" />
            </label>
            {op === "models.benchmark" ? (
              <label className="flex flex-col gap-0.5">
                <span className="text-muted-foreground">Target concurrency of</span>
                <NativeSelect className="w-48" value={target} onChange={(e) => setTarget(e.target.value)} aria-label="Delivery target">
                  <option value="">deploy.target_concurrency</option>
                  {delivery.map((t) => (
                    <option key={t.id} value={t.name}>
                      {t.name} ({t.concurrency ?? "default"} streams)
                    </option>
                  ))}
                </NativeSelect>
              </label>
            ) : null}
            <Button size="xs" variant="outline" disabled={busy} onClick={() => void run(true)}>
              Plan
            </Button>
            <Button size="xs" disabled={busy || !plan} onClick={() => void run(false)}>
              {LABEL[op]}
            </Button>
            <Button size="xs" variant="ghost" disabled={busy} onClick={() => setOp(null)}>
              Cancel
            </Button>
          </div>
          {plan ? (
            <p className="text-muted-foreground" data-slot="model-check-plan">
              {plan.profiles ? `${plan.profiles.map((p) => `${p.profile}: ${p.action}`).join(", ")} · ` : null}
              {plan.steps ? `${plan.steps.length} steps · ` : null}
              {estimateText(plan.estimate)}
            </p>
          ) : null}
        </div>
      ) : null}
      <Message m={message} />
    </Section>
  );
}
