import { useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { approvalsGetOptions, deploymentTargetsListOptions } from "@/api/gen/@tanstack/react-query.gen";
import type { ApprovalAccepted, Deployment, DeploymentPromotion, DeploymentPromote } from "@/api/gen/types.gen";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { NativeSelect } from "@/components/ui/native-select";
import { Textarea } from "@/components/ui/textarea";
import { errorMessage, runCommand } from "@/shell/panel";
import { Message, Verdict } from "./ui";

// The confirm modal of a promotion or a rollback (05 "Guardrails": canary, production and rollback are decided by a
// person in a confirm modal). Check runs the request as a dry run: every check of 02 with its verdict, and whether it
// is a config-only promotion. Confirm sends it for real — it always answers an approval, for people too (rule
// deployments) — and then approves that approval as the person who confirmed, so the signed record names them as the
// approver. The approved request appends the record and queues its delivery bundle.

export type PromoteMode = "promote" | "rollback";

const isApproval = (r: unknown): r is ApprovalAccepted => !!r && typeof r === "object" && "approvalId" in r;

const CHECK_LABEL: Record<string, string> = {
  "target-serves": "Target serves the export",
  engine: "Engine loads on the target",
  parity: "Parity",
  benchmark: "Latency benchmark",
  shadow: "Shadow volume",
  "slot-free": "Slot free",
  canary: "Confirmed canary",
  rollback: "Earlier production version",
  "not-pending": "No receipt pending",
};

export function PromoteDialog({ d, mode, onClose, onDone }: { d: Deployment; mode: PromoteMode; onClose: () => void; onDone: (text: string) => void }) {
  const qc = useQueryClient();
  const targets = useQuery({ ...deploymentTargetsListOptions(), enabled: mode === "promote" });
  const delivery = (targets.data?.items ?? []).filter((t) => t.kind === "delivery" && t.state === "active");
  const onDelivery = d.stage === "canary" || d.stage === "production";
  const [stage, setStage] = useState<"canary" | "production">(d.stage === "canary" ? "production" : "canary");
  const [target, setTarget] = useState(onDelivery ? d.targetName : "");
  const [slot, setSlot] = useState(d.slot ?? "");
  const [share, setShare] = useState("");
  const [reason, setReason] = useState("");
  const [plan, setPlan] = useState<DeploymentPromotion | null>(null);
  const [busy, setBusy] = useState(false);
  const [message, setMessage] = useState<{ error: boolean; text: string } | null>(null);
  const t = delivery.find((x) => x.name === target || x.id === target);
  const slots = t?.slots ?? [];

  const promoteBody = (): DeploymentPromote => ({
    stage,
    reason: reason.trim(),
    ...(target ? { target } : {}),
    ...(slot ? { slot } : {}),
    ...(stage === "canary" && share ? { trafficShare: Number(share) } : {}),
  });
  const send = (dryRun: boolean) =>
    mode === "promote"
      ? runCommand("deployments.promote", { deployment: d, body: promoteBody(), dryRun })
      : runCommand("deployments.rollback", { deployment: d, body: { reason: reason.trim() }, dryRun });

  const check = async () => {
    setBusy(true);
    setMessage(null);
    try {
      const res = await send(true);
      if (!isApproval(res)) setPlan(res);
    } catch (err) {
      setPlan(null);
      setMessage({ error: true, text: errorMessage(err) });
    } finally {
      setBusy(false);
    }
  };

  const confirm = async () => {
    setBusy(true);
    setMessage(null);
    try {
      const res = await send(false);
      let done: DeploymentPromotion | undefined;
      if (isApproval(res)) {
        const approval = await qc.fetchQuery(approvalsGetOptions({ path: { id: res.approvalId } }));
        const decided = await runCommand("approvals.approve", { approval, note: reason.trim() || undefined });
        const status = decided.result?.status ?? 0;
        if (status >= 400) {
          setMessage({ error: true, text: `Approved (${res.approvalId}), but the request was refused (${status}); see Approvals.` });
          return;
        }
        done = decided.result?.body as DeploymentPromotion | undefined;
      } else {
        done = res;
      }
      const rec = done?.record;
      onDone(
        rec
          ? `Signed ${rec.kind} record ${rec.id} (seq ${rec.seq}); its delivery bundle is being built. Run deliver.sh on the production host, then paste its receipt below.`
          : "Approved: the signed record is being written.",
      );
    } catch (err) {
      setMessage({ error: true, text: errorMessage(err) });
    } finally {
      setBusy(false);
    }
  };

  const title = mode === "promote" ? `Promote ${d.modelVersion}` : `Roll back ${d.modelVersion}`;
  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="flex max-h-[calc(100dvh-2rem)] flex-col gap-3 text-xs sm:max-w-xl" data-testid={`promote-dialog-${d.id}`}>
        <DialogHeader>
          <DialogTitle className="text-sm">{title}</DialogTitle>
          <DialogDescription className="text-xs">
            {mode === "promote"
              ? "The checks run first; confirming asks for the approval and decides it as you: the signed promotion record names you as its approver."
              : "The slot returns to its earlier production version, which stayed loaded on the production host. Confirming signs a rollback record and a small delivery script."}
          </DialogDescription>
        </DialogHeader>
        <div className="flex flex-col gap-2 overflow-y-auto">
          {mode === "promote" ? (
            <div className="grid grid-cols-[8rem_1fr] items-center gap-x-3 gap-y-1.5">
              <label htmlFor={`stage-${d.id}`} className="text-muted-foreground">
                Stage
              </label>
              <NativeSelect id={`stage-${d.id}`} value={stage} onChange={(e) => setStage(e.target.value as "canary" | "production")}>
                <option value="canary">canary</option>
                <option value="production">production</option>
              </NativeSelect>
              <label htmlFor={`target-${d.id}`} className="text-muted-foreground">
                Delivery target
              </label>
              <NativeSelect id={`target-${d.id}`} value={target} onChange={(e) => setTarget(e.target.value)}>
                <option value="">—</option>
                {delivery.map((x) => (
                  <option key={x.id} value={x.name}>
                    {x.name}
                  </option>
                ))}
              </NativeSelect>
              <label htmlFor={`slot-${d.id}`} className="text-muted-foreground">
                Slot
              </label>
              <NativeSelect id={`slot-${d.id}`} value={slot} onChange={(e) => setSlot(e.target.value)}>
                <option value="">—</option>
                {slots.map((s) => (
                  <option key={s} value={s}>
                    {s}
                  </option>
                ))}
                {slot && !slots.includes(slot) ? <option value={slot}>{slot}</option> : null}
              </NativeSelect>
              {stage === "canary" ? (
                <>
                  <label htmlFor={`share-${d.id}`} className="text-muted-foreground">
                    Traffic share
                  </label>
                  <Input id={`share-${d.id}`} className="h-7 text-xs" inputMode="decimal" placeholder="deploy.canary_share (0.05)" value={share} onChange={(e) => setShare(e.target.value)} />
                </>
              ) : null}
            </div>
          ) : null}
          <label className="flex flex-col gap-0.5">
            <span className="text-muted-foreground">Reason (signed into the record)</span>
            <Textarea aria-label="Reason" className="min-h-14 text-xs" value={reason} onChange={(e) => setReason(e.target.value)} />
          </label>
          {plan ? (
            <div className="flex flex-col gap-1" data-slot="promote-checks" data-ready={plan.ready}>
              <p>
                {plan.kind} to <span className="font-medium">{plan.stage}</span>
                {plan.targetName ? ` on ${plan.targetName}` : ""}
                {plan.slot ? ` / ${plan.slot}` : ""}
                {plan.trafficShare !== undefined ? ` at ${(plan.trafficShare * 100).toFixed(0)}%` : ""}
                {plan.configOnly ? " · config-only (no new model)" : ""}
              </p>
              <ul className="flex flex-col gap-0.5">
                {plan.checks.map((c, i) => (
                  <li key={`${c.name}-${i}`} data-check={c.name} data-state={c.state}>
                    <Verdict value={c.state} /> {CHECK_LABEL[c.name] ?? c.name}
                    {c.problemType ? <code className="ml-1 text-[11px] text-muted-foreground">{c.problemType}</code> : null}
                    {c.detail ? <span className="text-muted-foreground"> — {c.detail}</span> : null}
                  </li>
                ))}
              </ul>
            </div>
          ) : null}
          <Message m={message} />
        </div>
        <DialogFooter className="items-center">
          <Button size="sm" variant="outline" disabled={busy || !reason.trim()} onClick={() => void check()}>
            Check
          </Button>
          <Button size="sm" disabled={busy || !plan?.ready || !reason.trim()} onClick={() => void confirm()} data-command={`deployments.${mode}`}>
            {mode === "promote" ? "Confirm promotion" : "Confirm rollback"}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
