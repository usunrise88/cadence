import { describe, expect, it, vi } from "vitest";
import type { CadenceEvent, NotificationRule } from "@/api/gen/types.gen";
import { classOf, inAppAllowed } from "./classes";
import { applyNoticeBatch, noticeFor } from "./live";

const actor = { kind: "automation" as const, id: "cadence", name: "Cadence" };
const ev = (over: Partial<CadenceEvent>): CadenceEvent => ({ seq: 1, topic: "backups", type: "x", actor, at: "2026-09-30T10:00:00Z", ...over });
const rule = (eventClass: NotificationRule["eventClass"], inApp: boolean): NotificationRule => ({
  id: `ntr_${eventClass}`,
  eventClass,
  label: eventClass,
  events: [],
  channels: { inApp, telegram: false },
  timing: "none",
  bypassQuietHours: false,
  rev: 1,
  updatedAt: "",
  departures: [],
});

describe("event classes", () => {
  it("classifies like the server's router", () => {
    expect(classOf(ev({ type: "backup.failed" }))).toBe("failure");
    expect(classOf(ev({ type: "notification.digest", topic: "notifications" }))).toBe("digest");
    expect(classOf(ev({ topic: "job.j", type: "job.state_changed", payload: { job: { state: "failed" } } }))).toBe("failure");
    expect(classOf(ev({ topic: "job.j", type: "job.state_changed", payload: { job: { state: "done" } } }))).toBe("progress");
    expect(classOf(ev({ type: "mix.edited" }))).toBeUndefined();
    expect(classOf(ev({ topic: "job.j", type: "job.state_changed", payload: { job: { state: "failed", kind: "step" } } }))).toBeUndefined();
    const step = (state: string) => ev({ topic: "pipeline_run.plr_1", type: "pipeline_run.step_changed", payload: { pipelineRunId: "plr_1", step: { state } } });
    expect(classOf(step("done"))).toBe("progress");
    expect(classOf(step("failed"))).toBe("failure");
    expect(classOf(step("running"))).toBeUndefined();
    const health = (state: string) => ev({ topic: "compute.cmp_1", type: "compute.health", payload: { hostId: "cmp_1", health: { state } } });
    expect(classOf(health("unreachable"))).toBe("failure");
    expect(classOf(health("healthy"))).toBeUndefined();
  });

  it("lets a class into the in-app history only when its rule has in-app on", () => {
    const rules = [rule("progress", false), rule("failure", true)];
    const done = ev({ topic: "job.j", type: "job.state_changed", payload: { job: { state: "done", kind: "noop" } } });
    expect(inAppAllowed(done, rules)).toBe(false);
    expect(inAppAllowed(ev({ type: "backup.failed" }), rules)).toBe(true);
    expect(inAppAllowed(done, undefined)).toBe(true); // rules not loaded (or not the admin): everything shows
    expect(inAppAllowed(ev({ type: "credential.revoked" }), rules)).toBe(true); // outside the table
  });

  it("drops notices of switched-off classes from a batch", () => {
    const push = vi.fn();
    const batch = [
      ev({ seq: 1, topic: "job.j", type: "job.state_changed", payload: { job: { id: "j", kind: "noop", state: "done", progress: 1, attempt: 1, rev: 2, actor, createdAt: "", updatedAt: "" } } }),
      ev({ seq: 2, type: "backup.failed", payload: { backup: { id: "bkp_1" }, error: "pg_dump: disk full" } }),
    ];
    applyNoticeBatch(batch, () => {}, push, [rule("progress", false)]);
    expect(push).toHaveBeenCalledTimes(1);
    expect(push.mock.calls[0]![0]).toMatchObject({ level: "error", title: "Backup failed", detail: "pg_dump: disk full", open: { panel: "settings" } });
  });

  it("notices restore tests and the digest", () => {
    expect(noticeFor(ev({ type: "backup.restore_failed", payload: { backup: { id: "bkp_1" }, error: "table projects has 1 rows" } }))).toMatchObject({ level: "error", title: "Restore test failed" });
    expect(noticeFor(ev({ type: "backup.restore_passed", payload: { backup: { id: "bkp_1" } } }))).toMatchObject({ level: "success", detail: "set bkp_1" });
    expect(noticeFor(ev({ type: "backup.queued" }))).toBeUndefined();
    expect(noticeFor(ev({ topic: "notifications", type: "notification.digest", payload: { title: "Cadence daily digest · Wed 30 Sep", text: "Open approvals: 0" } }))).toMatchObject({
      level: "info",
      title: "Cadence daily digest · Wed 30 Sep",
      detail: "Open approvals: 0",
    });
  });
});
