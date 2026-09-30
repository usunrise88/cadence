import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createAutosaver } from "./autosave";

function harness(delayMs = 2000) {
  const state = { now: "a", saved: "a" as string | undefined, blocked: false };
  const calls: { snap: string; urgent: boolean }[] = [];
  let release: (() => void) | undefined;
  let hold = false;
  const saver = createAutosaver({
    delayMs,
    snapshot: () => (state.blocked ? undefined : state.now),
    saved: () => state.saved,
    save: (snap, urgent) => {
      calls.push({ snap, urgent });
      const done = () => {
        state.saved = snap;
      };
      if (!hold) {
        done();
        return Promise.resolve();
      }
      return new Promise<void>((r) => {
        release = () => {
          done();
          r();
        };
      });
    },
  });
  return {
    state,
    calls,
    saver,
    holdSaves: () => (hold = true),
    release: () => release?.(),
  };
}

describe("workspace autosave", () => {
  beforeEach(() => vi.useFakeTimers());
  afterEach(() => vi.useRealTimers());

  it("saves once, 2 s after the last of many changes (a window being dragged)", async () => {
    const h = harness();
    for (let i = 0; i < 20; i++) {
      h.state.now = `drag-${i}`;
      h.saver.schedule();
      await vi.advanceTimersByTimeAsync(100);
    }
    expect(h.calls).toHaveLength(0);
    await vi.advanceTimersByTimeAsync(1899); // 1999 ms since the last change
    expect(h.calls).toHaveLength(0);
    await vi.advanceTimersByTimeAsync(1);
    expect(h.calls).toEqual([{ snap: "drag-19", urgent: false }]);
  });

  it("skips a save when the layout equals the stored one (moved and put back)", async () => {
    const h = harness();
    h.state.now = "moved";
    h.saver.schedule();
    h.state.now = "a";
    await vi.advanceTimersByTimeAsync(2000);
    expect(h.calls).toHaveLength(0);
  });

  it("does not save while restoring or in conflict", async () => {
    const h = harness();
    h.state.now = "b";
    h.state.blocked = true;
    h.saver.schedule();
    await vi.advanceTimersByTimeAsync(5000);
    await h.saver.flush(true);
    expect(h.calls).toHaveLength(0);
  });

  it("flush saves a pending change at once and cancels the timer", async () => {
    const h = harness();
    h.state.now = "b";
    h.saver.schedule();
    await h.saver.flush(true);
    expect(h.calls).toEqual([{ snap: "b", urgent: true }]);
    await vi.advanceTimersByTimeAsync(5000);
    expect(h.calls).toHaveLength(1);
  });

  it("flush with nothing changed sends nothing", async () => {
    const h = harness();
    await h.saver.flush(true);
    expect(h.calls).toHaveLength(0);
  });

  it("keeps one save in flight; a change during it is saved after it (never two on one rev)", async () => {
    const h = harness();
    h.holdSaves();
    h.state.now = "b";
    const first = h.saver.flush();
    h.state.now = "c";
    const second = h.saver.flush();
    expect(h.calls).toHaveLength(1);
    h.release();
    await vi.advanceTimersByTimeAsync(0);
    expect(h.calls.map((c) => c.snap)).toEqual(["b", "c"]);
    h.release();
    await first;
    await second;
    expect(h.state.saved).toBe("c");
  });

  it("dispose stops the pending timer", async () => {
    const h = harness();
    h.state.now = "b";
    h.saver.schedule();
    h.saver.dispose();
    await vi.advanceTimersByTimeAsync(5000);
    h.saver.schedule();
    await vi.advanceTimersByTimeAsync(5000);
    expect(h.calls).toHaveLength(0);
  });
});
