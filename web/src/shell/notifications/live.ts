import { useEffect, useRef } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { notificationRulesListOptions } from "@/api/gen/@tanstack/react-query.gen";
import type { Approval, Backup, CadenceEvent, Credential, Job, NotificationRule } from "@/api/gen/types.gen";
import { actorLabel, estimateLine } from "@/shell/approvals/format";
import { patchApprovals } from "@/shell/approvals/cache";
import { events } from "@/shell/registries";
import { inAppAllowed } from "./classes";
import { notify, type Notice } from "./store";

// In-app notification history from live events (docs/spec/06-platform.md "Notifications"): approval requested and
// decided, job failed or done (a step job through its pipeline step: step failed or done), a compute host turning
// unreachable, backup and restore-test outcomes, the daily digest, credential revoked. Each notice
// also reaches the polite live region (WCAG 4.1.3). The routing table (Settings → Notifications) decides which
// classes the history shows; Telegram is routed on the server by the same table.

export const NOTICE_TOPICS = ["approvals", "job.*", "pipeline_run.*", "compute.*", "entity.credential.*", "backups", "notifications"];

type NoticeInput = Omit<Notice, "id" | "at" | "read">;

const APPROVALS = { label: "Open Approvals", panel: "approvals" };
const SETTINGS = { label: "Open Settings", panel: "settings" };

/** The notice an event deserves, if any. Pure: the shell subscription and the tests share it. */
export function noticeFor(e: CadenceEvent): NoticeInput | undefined {
  const p = (e.payload ?? {}) as { approval?: Approval; job?: Job; credential?: Credential };
  // Approval events go out twice (the approvals topic and the entity topic); the approvals topic is the one noticed.
  if (e.topic !== "approvals" && e.type.startsWith("approval.")) return undefined;
  if (e.type === "approval.requested" && p.approval) {
    const a = p.approval;
    const estimate = estimateLine(a);
    return {
      level: "warning",
      title: `Approval requested: ${a.operation}`,
      detail: `${actorLabel(a.actor)} — ${a.reason}${estimate ? ` (${estimate})` : ""}`,
      open: APPROVALS,
      seq: e.seq,
    };
  }
  if (e.type === "approval.decided" && p.approval) {
    const a = p.approval;
    const expired = a.decision?.expired;
    return {
      level: a.state === "approved" ? "success" : "info",
      title: `${expired ? "Approval expired" : a.state === "approved" ? "Approved" : "Denied"}: ${a.operation}`,
      detail: a.decision?.note ?? (a.decidedBy && !expired ? `by ${actorLabel(a.decidedBy)}` : undefined),
      open: APPROVALS,
      seq: e.seq,
    };
  }
  if (e.type === "job.state_changed" && p.job) {
    const j = p.job;
    if (j.kind === "step") return undefined; // its pipeline step's event tells it, once per step
    if (j.state === "failed") return { level: "error", title: `Job failed: ${j.kind}`, detail: j.error ?? j.message, seq: e.seq };
    if (j.state === "done") return { level: "success", title: `Job done: ${j.kind}`, detail: j.message, seq: e.seq };
    return undefined;
  }
  if (e.type === "pipeline_run.step_changed" && e.topic.startsWith("pipeline_run.")) {
    const sp = e.payload as { pipelineRunId?: string; step?: { step?: string; kind?: string; state?: string; error?: { type?: string; message?: string } } } | undefined;
    const s = sp?.step;
    const name = `${s?.step ?? "step"}${s?.kind ? ` (${s.kind})` : ""}`;
    if (s?.state === "failed") return { level: "error", title: `Step failed: ${name}`, detail: s.error ? `${s.error.type}: ${s.error.message}` : sp?.pipelineRunId, seq: e.seq };
    if (s?.state === "done") return { level: "success", title: `Step done: ${name}`, detail: sp?.pipelineRunId, seq: e.seq };
    return undefined;
  }
  if (e.type === "compute.health" && e.topic.startsWith("compute.")) {
    const h = e.payload as { hostId?: string; health?: { state?: string; detail?: string } } | undefined;
    if (h?.health?.state === "unreachable") return { level: "error", title: "Compute host unreachable", detail: h.health.detail ?? h.hostId, open: SETTINGS, seq: e.seq };
    return undefined;
  }
  if (e.topic === "backups") {
    const b = (e.payload as { backup?: Backup; error?: string } | undefined)?.backup;
    const err = (e.payload as { error?: string } | undefined)?.error;
    if (e.type === "backup.failed") return { level: "error", title: "Backup failed", detail: err ?? b?.error, open: SETTINGS, seq: e.seq };
    if (e.type === "backup.restore_failed") return { level: "error", title: "Restore test failed", detail: err ?? b?.restoreTest?.error, open: SETTINGS, seq: e.seq };
    if (e.type === "backup.restore_passed") return { level: "success", title: "Restore test passed", detail: b ? `set ${b.id}` : undefined, open: SETTINGS, seq: e.seq };
    return undefined;
  }
  if (e.type === "notification.digest") {
    const d = e.payload as { title?: string; text?: string } | undefined;
    return { level: "info", title: d?.title ?? "Daily digest", detail: d?.text, seq: e.seq };
  }
  if (e.type === "credential.revoked" && p.credential) {
    const c = p.credential;
    return { level: "warning", title: `Credential revoked: ${c.name}`, detail: `${c.kind.replace("_", " ")} · by ${actorLabel(e.actor)}`, seq: e.seq };
  }
  return undefined;
}

/** Applies one batch: patches the approvals cache, then records the notices the routing rules let into the history. */
export function applyNoticeBatch(batch: CadenceEvent[], patch: (approvals: Approval[]) => void, push: (n: NoticeInput) => void, rules?: NotificationRule[]): void {
  const approvals = batch.flatMap((e) => ((e.payload as { approval?: Approval } | undefined)?.approval && e.topic === "approvals" ? [(e.payload as { approval: Approval }).approval] : []));
  patch(approvals);
  for (const e of batch) {
    const n = noticeFor(e);
    if (n && inAppAllowed(e, rules)) push(n);
  }
}

/** The chrome's own subscription (not a panel's): lives as long as the signed-in shell. */
export function useLiveNotifications(): void {
  const qc = useQueryClient();
  // The routing table (admin only: a failed read leaves every class in the history).
  const rules = useQuery({ ...notificationRulesListOptions(), staleTime: 60_000, retry: false });
  const rulesRef = useRef<NotificationRule[] | undefined>(undefined);
  useEffect(() => {
    rulesRef.current = rules.data?.items;
  }, [rules.data]);
  useEffect(
    () =>
      events.subscribe(
        NOTICE_TOPICS,
        (batch) =>
          applyNoticeBatch(
            batch,
            (a) => patchApprovals(qc, a),
            (n) => notify(n),
            rulesRef.current,
          ),
        "shell",
      ),
    [qc],
  );
}
