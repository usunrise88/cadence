import type { CadenceEvent, NotificationClass, NotificationRule } from "@/api/gen/types.gen";

// The event class of an event, as the server's router classifies it (control-plane/internal/notify/classify.go keeps
// the same table; its TestClassTableMatchesWeb compares the two): the routing rule of that class decides whether the
// in-app history shows it. Types classified by their payload are in classOf.

const TABLE: Record<string, NotificationClass> = {
  "approval.requested": "approval_requested",
  "backup.failed": "failure",
  "backup.restore_failed": "failure",
  "mount.unhealthy": "failure",
  "storage.low_space": "failure",
  "eval.gated": "outcome",
  "sweep.ended": "outcome",
  "deployment.promoted": "outcome",
  "schedule.finished": "outcome",
  "batch.closed": "outcome",
  "branch.waiting": "outcome",
  "backup.succeeded": "progress",
  "backup.restore_passed": "progress",
  "checkpoint.saved": "progress",
  "triage.item_added": "progress",
  "golden_set.frozen": "progress",
  "notification.digest": "digest",
};

/** Evals (evl_…) tell their end once; their pipeline run's steps are not told one by one. */
export const EVAL_RUN_PREFIX = "evl_";

/** The class of e, or undefined for events outside the routing table (they are shown as before). */
export function classOf(e: CadenceEvent): NotificationClass | undefined {
  if (e.type === "job.state_changed") {
    const job = (e.payload as { job?: { state?: string; kind?: string } } | undefined)?.job;
    if (job?.kind === "step") return undefined; // its pipeline step's event tells it
    return doneOrFailed(job?.state);
  }
  if (e.type === "pipeline_run.step_changed") {
    const p = e.payload as { runId?: string; step?: { state?: string } } | undefined;
    if (p?.step?.state === "done" && p.runId?.startsWith(EVAL_RUN_PREFIX)) return undefined;
    return doneOrFailed(p?.step?.state);
  }
  if (e.type === "eval.status_changed") {
    return doneOrFailed((e.payload as { eval?: { status?: string } } | undefined)?.eval?.status);
  }
  if (e.type === "compute.health") {
    const state = (e.payload as { health?: { state?: string } } | undefined)?.health?.state;
    return state === "unreachable" ? "failure" : undefined;
  }
  return TABLE[e.type];
}

function doneOrFailed(state: string | undefined): NotificationClass | undefined {
  return state === "failed" ? "failure" : state === "done" ? "progress" : undefined;
}

/** Whether the in-app history shows e under rules (all of it while the rules are unknown). */
export function inAppAllowed(e: CadenceEvent, rules: NotificationRule[] | undefined): boolean {
  const cls = classOf(e);
  if (!cls || !rules) return true;
  const rule = rules.find((r) => r.eventClass === cls);
  return !rule || rule.channels.inApp; // the in-app history is always immediate; timing is Telegram's
}
