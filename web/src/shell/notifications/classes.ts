import type { CadenceEvent, NotificationClass, NotificationRule } from "@/api/gen/types.gen";

// The event class of an event, as the server's router classifies it (control-plane/internal/notify/classify.go keeps
// the same table): the routing rule of that class decides whether the in-app history shows it.

const TABLE: Record<string, NotificationClass> = {
  "approval.requested": "approval_requested",
  "backup.failed": "failure",
  "backup.restore_failed": "failure",
  "mount.unhealthy": "failure",
  "compute.card_closed": "failure",
  "gate.verdict": "outcome",
  "deployment.promoted": "outcome",
  "schedule.finished": "outcome",
  "batch.closed": "outcome",
  "backup.succeeded": "progress",
  "backup.restore_passed": "progress",
  "pipeline_step.done": "progress",
  "checkpoint.saved": "progress",
  "triage.item_added": "progress",
  "notification.digest": "digest",
};

/** The class of e, or undefined for events outside the routing table (they are shown as before). */
export function classOf(e: CadenceEvent): NotificationClass | undefined {
  if (e.type === "job.state_changed") {
    const state = (e.payload as { job?: { state?: string } } | undefined)?.job?.state;
    return state === "failed" ? "failure" : state === "done" ? "progress" : undefined;
  }
  return TABLE[e.type];
}

/** Whether the in-app history shows e under rules (all of it while the rules are unknown). */
export function inAppAllowed(e: CadenceEvent, rules: NotificationRule[] | undefined): boolean {
  const cls = classOf(e);
  if (!cls || !rules) return true;
  const rule = rules.find((r) => r.eventClass === cls);
  return !rule || rule.channels.inApp; // the in-app history is always immediate; timing is Telegram's
}
