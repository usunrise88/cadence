import { useEffect } from "react";
import { useQueryClient } from "@tanstack/react-query";
import type { Approval, CadenceEvent, Credential, Job } from "@/api/gen/types.gen";
import { actorLabel, estimateLine } from "@/shell/approvals/format";
import { patchApprovals } from "@/shell/approvals/cache";
import { events } from "@/shell/registries";
import { notify, type Notice } from "./store";

// In-app notification history from live events (docs/spec/06-platform.md "Notifications"): approval requested and
// decided, job failed or done, credential revoked. Each notice also reaches the polite live region (WCAG 4.1.3).
// Telegram joins behind the same routing table in phase 2.

export const NOTICE_TOPICS = ["approvals", "job.*", "entity.credential.*"];

type NoticeInput = Omit<Notice, "id" | "at" | "read">;

const APPROVALS = { label: "Open Approvals", panel: "approvals" };

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
    if (j.state === "failed") return { level: "error", title: `Job failed: ${j.kind}`, detail: j.error ?? j.message, seq: e.seq };
    if (j.state === "done") return { level: "success", title: `Job done: ${j.kind}`, detail: j.message, seq: e.seq };
    return undefined;
  }
  if (e.type === "credential.revoked" && p.credential) {
    const c = p.credential;
    return { level: "warning", title: `Credential revoked: ${c.name}`, detail: `${c.kind.replace("_", " ")} · by ${actorLabel(e.actor)}`, seq: e.seq };
  }
  return undefined;
}

/** Applies one batch: patches the approvals cache, then records notices. */
export function applyNoticeBatch(batch: CadenceEvent[], patch: (approvals: Approval[]) => void, push: (n: NoticeInput) => void): void {
  const approvals = batch.flatMap((e) => ((e.payload as { approval?: Approval } | undefined)?.approval && e.topic === "approvals" ? [(e.payload as { approval: Approval }).approval] : []));
  patch(approvals);
  for (const e of batch) {
    const n = noticeFor(e);
    if (n) push(n);
  }
}

/** The chrome's own subscription (not a panel's): lives as long as the signed-in shell. */
export function useLiveNotifications(): void {
  const qc = useQueryClient();
  useEffect(
    () =>
      events.subscribe(
        NOTICE_TOPICS,
        (batch) =>
          applyNoticeBatch(
            batch,
            (a) => patchApprovals(qc, a),
            (n) => notify(n),
          ),
        "shell",
      ),
    [qc],
  );
}
