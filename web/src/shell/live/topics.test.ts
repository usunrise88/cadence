import { describe, expect, it } from "vitest";
import { coverPatterns, topicMatches } from "./topics";

describe("topicMatches", () => {
  it.each([
    ["run.123.*", "run.123.metrics", true],
    ["run.123.*", "run.123.status", true],
    ["run.123.*", "run.123", false],
    ["run.123.*", "run.1234.metrics", false],
    ["job.*", "job.5.log", true],
    ["entity.project.*", "entity.project.prj_1", true],
    ["entity.project.*", "entity.workspace.w1", false],
    ["queue", "queue", true],
    ["queue", "queue.x", false],
    ["*", "anything.at.all", true],
  ])("%s ~ %s → %s", (p, t, want) => {
    expect(topicMatches(p, t)).toBe(want);
  });
});

describe("coverPatterns", () => {
  it("drops covered patterns and duplicates", () => {
    expect(coverPatterns(["run.1.*", "run.1.metrics", "queue", "queue"])).toEqual(["queue", "run.1.*"]);
  });
  it("collapses to * when present", () => {
    expect(coverPatterns(["a", "*"])).toEqual(["*"]);
  });
});
