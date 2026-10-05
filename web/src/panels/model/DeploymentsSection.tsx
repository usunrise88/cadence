import { useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { deploymentsListOptions } from "@/api/gen/@tanstack/react-query.gen";
import type { ApprovalAccepted, Deployment, DeploymentNew, ModelVersion } from "@/api/gen/types.gen";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { errorMessage, runCommand, useProject, useTopic } from "@/shell/panel";
import { PromoteDialog, type PromoteMode } from "./PromoteDialog";
import { PromotionChain, ReceiptBox } from "./ReceiptBox";
import { hours, Message, Section, Verdict } from "./ui";

// The deployments of a model version in the open project (02 "Deployments"): a shadow on the staging target replayed
// every night against the comparison model, then canary and production on a delivery target's slot. Deploy to shadow
// dry-runs first; Promote and Roll back open the confirm modal; a pending promotion shows its receipt box, a
// deployment on a delivery target its slot's promotion chain.

const isApproval = (r: unknown): r is ApprovalAccepted => !!r && typeof r === "object" && "approvalId" in r;

type ShadowForm = { mount: string; path: string; source: string; language: string; profile: string; against: string };
const EMPTY: ShadowForm = { mount: "", path: "", source: "", language: "", profile: "", against: "" };

function shadowBody(m: ModelVersion, f: ShadowForm): DeploymentNew {
  const t = (s: string) => s.trim();
  return {
    version: m.id,
    ...(t(f.profile) ? { profile: t(f.profile) } : {}),
    ...(t(f.against) ? { against: t(f.against) } : {}),
    replay: { mount: t(f.mount), source: t(f.source), ...(t(f.path) ? { path: t(f.path) } : {}), ...(t(f.language) ? { language: t(f.language) } : {}) },
  };
}

function ShadowLine({ d }: { d: Deployment }) {
  const s = d.shadow;
  if (!s) return null;
  const div = s.divergence;
  return (
    <p data-slot="deployment-shadow">
      <span className="text-muted-foreground">Shadow: </span>
      {hours(s.hours)} of {s.minHours} h replayed ({s.calls} calls, {s.nights} nights)
      {div?.wer !== undefined ? (
        <span>
          {" "}
          · divergence {(div.wer * 100).toFixed(1)}%{div.ci ? ` (${(div.ci[0] * 100).toFixed(1)}–${(div.ci[1] * 100).toFixed(1)}%)` : ""}
        </span>
      ) : null}
      {s.against?.label ? <span className="text-muted-foreground"> · against {s.against.label}</span> : null}
      {s.nextReplayAt && d.stage === "shadow" ? <span className="text-muted-foreground"> · next replay {new Date(s.nextReplayAt).toLocaleString()}</span> : null}
    </p>
  );
}

function DeploymentCard({ d, project, onAction, refresh }: { d: Deployment; project: string; onAction: (d: Deployment, mode: PromoteMode) => void; refresh: () => void }) {
  const live = d.state === "active" || d.state === "pending-delivery";
  const onDelivery = d.stage === "canary" || d.stage === "production";
  return (
    <li className="flex flex-col gap-1 rounded-md border p-2" data-deployment={d.id} data-stage={d.stage} data-state={d.state}>
      <p className="flex flex-wrap items-center gap-2">
        <span className="font-medium">{d.stage}</span>
        <Verdict value={d.state} />
        <span className="text-muted-foreground">
          {d.targetName}
          {d.slot ? ` / ${d.slot}` : ""} · {d.profile}
          {d.trafficShare !== undefined ? ` · ${(d.trafficShare * 100).toFixed(0)}% of traffic` : ""}
        </span>
        <code className="text-[11px] text-muted-foreground">{d.id}</code>
        {live && d.state === "active" ? (
          <span className="ml-auto flex gap-1.5">
            {d.stage !== "retired" ? (
              <Button size="xs" variant="outline" onClick={() => onAction(d, "promote")} data-command="deployments.promote">
                Promote…
              </Button>
            ) : null}
            {onDelivery ? (
              <Button size="xs" variant="outline" onClick={() => onAction(d, "rollback")} data-command="deployments.rollback">
                Roll back…
              </Button>
            ) : null}
          </span>
        ) : null}
      </p>
      <ShadowLine d={d} />
      {d.decoding.boostLists.length ? (
        <p className="text-muted-foreground">Boost lists: {d.decoding.boostLists.map((b) => `${b.locale}/${b.domain}`).join(", ")}</p>
      ) : null}
      {d.pending ? (
        <>
          <p className="text-muted-foreground">
            Pending {d.pending.kind} to {d.pending.stage}
            {d.pending.slot ? ` on ${d.pending.slot}` : ""}
            {d.pending.configOnly ? " (config-only)" : ""}
          </p>
          <ReceiptBox recordId={d.pending.recordId} onConfirmed={refresh} />
        </>
      ) : null}
      {onDelivery ? <PromotionChain target={d.targetId} project={project} slot={d.slot} /> : null}
    </li>
  );
}

export function DeploymentsSection({ m }: { m: ModelVersion }) {
  const project = useProject();
  const qc = useQueryClient();
  const opts = deploymentsListOptions({ path: { p: project ?? "" }, query: { version: m.id, state: "all" } });
  const list = useQuery({ ...opts, enabled: !!project });
  const items = list.data?.items ?? [];
  const refresh = () => void qc.invalidateQueries({ queryKey: opts.queryKey });
  const topics = items.flatMap((d) => [`deploy.${d.id}`, `shadow.${d.id}`]);
  useTopic(project ? [...topics, `entity.model.${m.id}`] : null, refresh);
  const [form, setForm] = useState<ShadowForm | null>(null);
  const [planned, setPlanned] = useState<Deployment | null>(null);
  const [dialog, setDialog] = useState<{ d: Deployment; mode: PromoteMode } | null>(null);
  const [busy, setBusy] = useState(false);
  const [message, setMessage] = useState<{ error: boolean; text: string } | null>(null);

  const create = async (dryRun: boolean) => {
    if (!project || !form) return;
    setBusy(true);
    setMessage(null);
    try {
      const res = await runCommand("deployments.new", { project, body: shadowBody(m, form), dryRun });
      if (isApproval(res)) {
        setMessage({ error: false, text: `Waiting for an approval (${res.approvalId}).` });
      } else if (dryRun) {
        setPlanned(res);
      } else {
        setForm(null);
        setPlanned(null);
        setMessage({ error: false, text: `Shadow deployment ${res.id} on ${res.targetName}: it replays calls every night.` });
        refresh();
      }
    } catch (err) {
      setMessage({ error: true, text: errorMessage(err) });
    } finally {
      setBusy(false);
    }
  };
  const field = (k: keyof ShadowForm, label: string, placeholder = "") => (
    <label className="flex flex-col gap-0.5">
      <span className="text-muted-foreground">{label}</span>
      <Input
        aria-label={label}
        className="h-7 text-xs"
        placeholder={placeholder}
        value={form?.[k] ?? ""}
        onChange={(e) => {
          setPlanned(null);
          setForm((f) => ({ ...(f ?? EMPTY), [k]: e.target.value }));
        }}
      />
    </label>
  );

  return (
    <Section
      id={`model-deployments-${m.id}`}
      title="Deployments"
      slot="model-deployments"
      actions={
        project ? (
          <Button size="xs" variant="outline" onClick={() => setForm(form ? null : EMPTY)} data-command="deployments.new">
            Deploy to shadow…
          </Button>
        ) : null
      }
    >
      {!project ? (
        <p className="text-muted-foreground">Open a project to see its deployments of this model.</p>
      ) : items.length ? (
        <ul className="flex flex-col gap-1.5">
          {items.map((d) => (
            <DeploymentCard key={d.id} d={d} project={project} onAction={(dep, mode) => setDialog({ d: dep, mode })} refresh={refresh} />
          ))}
        </ul>
      ) : (
        <p className="text-muted-foreground">Not deployed in {project}: deploy an export to shadow to replay real calls against it.</p>
      )}
      {form ? (
        <div className="flex flex-col gap-2 rounded-md border bg-muted/30 p-2" data-slot="shadow-form">
          <div className="grid grid-cols-2 gap-2 @lg:grid-cols-3">
            {field("mount", "Replay mount", "corpora")}
            {field("path", "Path under the mount", "calls/2026")}
            {field("source", "Registered source", "era-calls")}
            {field("language", "Language", "from the gating eval")}
            {field("profile", "Latency profile", "primary")}
            {field("against", "Compare with", "production, else @baseline")}
          </div>
          <div className="flex gap-1.5">
            <Button size="xs" variant="outline" disabled={busy || !form.mount.trim() || !form.source.trim()} onClick={() => void create(true)}>
              Check
            </Button>
            <Button size="xs" disabled={busy || !planned} onClick={() => void create(false)}>
              Deploy to shadow
            </Button>
            <Button size="xs" variant="ghost" onClick={() => setForm(null)}>
              Cancel
            </Button>
          </div>
          {planned ? (
            <p className="text-muted-foreground" data-slot="shadow-plan">
              A shadow of {planned.modelVersion} at {planned.profile} on {planned.targetName}
              {planned.shadow?.against?.label ? `, compared with ${planned.shadow.against.label}` : ""}; a canary needs {planned.shadow?.minHours ?? "—"} h of
              replayed calls.
            </p>
          ) : null}
        </div>
      ) : null}
      <Message m={message} />
      {dialog ? (
        <PromoteDialog
          d={dialog.d}
          mode={dialog.mode}
          onClose={() => setDialog(null)}
          onDone={(text) => {
            setDialog(null);
            setMessage({ error: false, text });
            refresh();
          }}
        />
      ) : null}
    </Section>
  );
}
