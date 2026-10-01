import { describe, expect, it } from "vitest";
import { formatDays, formatWindows, windowError } from "./windows";

describe("availability windows", () => {
  it("names day runs", () => {
    expect(formatDays(["mon", "tue", "wed", "thu", "fri"])).toBe("Mon–Fri");
    expect(formatDays(["sun", "sat"])).toBe("Sat, Sun");
    expect(formatDays(["mon", "wed", "thu", "fri", "sun"])).toBe("Mon, Wed–Fri, Sun");
    expect(formatDays(["mon", "tue", "wed", "thu", "fri", "sat", "sun"])).toBe("Every day");
  });

  it("formats windows per job kind, marking a close after midnight", () => {
    expect(formatWindows(undefined)).toEqual([]);
    expect(formatWindows({ training: [{ days: ["mon", "tue", "wed", "thu", "fri"], start: "20:00", end: "08:00" }], eval: [] })).toEqual([
      "training: Mon–Fri 20:00–08:00 (next day) (instance time)",
    ]);
    expect(formatWindows({ export: [{ days: ["sat"], start: "00:00", end: "24:00", timezone: "Europe/Berlin" }] })).toEqual(["export: Sat 00:00–24:00 Europe/Berlin"]);
  });

  it("checks what the contract would refuse", () => {
    expect(windowError({ days: [], start: "20:00", end: "08:00" })).toMatch(/day/);
    expect(windowError({ days: ["mon"], start: "24:00", end: "08:00" })).toMatch(/Opens/);
    expect(windowError({ days: ["mon"], start: "20:00", end: "8:00" })).toMatch(/Closes/);
    expect(windowError({ days: ["mon"], start: "20:00", end: "20:00" })).toMatch(/same time/);
    expect(windowError({ days: ["mon"], start: "20:00", end: "24:00" })).toBeUndefined();
  });
});
