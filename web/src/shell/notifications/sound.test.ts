import { beforeEach, describe, expect, it } from "vitest";
import { soundFor, useNotificationSound } from "./sound";
import { notify, useNotices } from "./store";

describe("notification sound", () => {
  beforeEach(() => {
    useNotices.setState({ items: [] });
    useNotificationSound.getState().setEnabled(true);
  });

  it("plays for approval requests and failures only, and never when switched off", () => {
    expect(soundFor("warning", true)).toHaveLength(2);
    expect(soundFor("error", true)).toHaveLength(2);
    expect(soundFor("success", true)).toBeUndefined();
    expect(soundFor("info", true)).toBeUndefined();
    expect(soundFor("warning", false)).toBeUndefined();
  });

  it("remembers the choice per browser", () => {
    useNotificationSound.getState().setEnabled(false);
    expect(localStorage.getItem("cadence.notificationSound")).toBe("off");
    expect(useNotificationSound.getState().enabled).toBe(false);
  });

  it("notify reports a live event seen twice as not new, so it sounds once", () => {
    expect(notify({ level: "warning", title: "Approval requested", seq: 7 })).toBe(true);
    expect(notify({ level: "warning", title: "Approval requested", seq: 7 })).toBe(false);
    expect(notify({ level: "info", title: "local" })).toBe(true);
  });
});
