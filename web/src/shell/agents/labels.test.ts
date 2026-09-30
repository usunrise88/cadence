import { describe, expect, it } from "vitest";
import { isAsleep, sessionStateLabel } from "./labels";
import { session } from "./testdata";

describe("session state labels", () => {
  it("calls an idle pause asleep and every other state by its name", () => {
    const idle = session({ state: "paused", pauseReason: { code: "idle", message: "no message for 30 min" } });
    expect(isAsleep(idle)).toBe(true);
    expect(sessionStateLabel(idle)).toBe("asleep");
    expect(sessionStateLabel(session({ state: "paused", pauseReason: { code: "runaway", message: "same call 3 times" } }))).toBe("paused");
    expect(sessionStateLabel(session({ state: "waiting_approval" }))).toBe("waiting approval");
    expect(isAsleep(session({ state: "running", pauseReason: { code: "idle", message: "x" } }))).toBe(false);
  });
});
