import { describe, expect, it } from "vitest";
import type { JobLogLine } from "@/api/gen/types.gen";
import { atBottom, cap, formatLine, matches, mergeLines, visibleRange } from "./model";

const line = (seq: number, level: JobLogLine["level"] = "info", msg = `line ${seq}`, fields?: Record<string, unknown>): JobLogLine => ({
  seq,
  t: "2026-09-30T10:00:00Z",
  level,
  msg,
  ...(fields ? { fields } : {}),
});

describe("log view model", () => {
  it("filters by minimum level and case-insensitive text, like jobLogs.list", () => {
    expect(matches(line(1, "debug"), "info", "")).toBe(false);
    expect(matches(line(1, "error"), "warn", "")).toBe(true);
    expect(matches(line(1, "info", "CUDA out of memory"), "debug", "out of MEMORY")).toBe(true);
    expect(matches(line(1, "info", "step 10"), "debug", "oom")).toBe(false);
  });

  it("appends live lines after the history without duplicates", () => {
    const h = [line(1), line(2)];
    expect(mergeLines(h, [])).toBe(h);
    expect(mergeLines(h, [line(2), line(3), line(3), line(4)]).map((l) => l.seq)).toEqual([1, 2, 3, 4]);
    expect(mergeLines([], [line(7)]).map((l) => l.seq)).toEqual([7]);
    expect(cap([line(1), line(2), line(3)], 2).map((l) => l.seq)).toEqual([2, 3]);
  });

  it("copies time, level, message and fields", () => {
    expect(formatLine(line(3, "warn", "slow step", { step: 10, stage: "train" }))).toBe("2026-09-30T10:00:00Z WARN  slow step step=10 stage=train");
  });

  it("renders only the rows in view, with overscan", () => {
    expect(visibleRange(0, 200, 20, 0)).toEqual([0, 0]);
    expect(visibleRange(0, 200, 20, 1000, 5)).toEqual([0, 16]);
    expect(visibleRange(2000, 200, 20, 1000, 5)).toEqual([95, 116]);
    expect(visibleRange(19990, 200, 20, 1000, 5)).toEqual([994, 1000]);
    expect(atBottom(800, 200, 1000)).toBe(true);
    expect(atBottom(700, 200, 1000)).toBe(false);
  });
});
