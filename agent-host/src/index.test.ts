import assert from "node:assert/strict";
import { test } from "node:test";
import { describe } from "./index.ts";

// Placeholder until spike A1 lands the driver contract tests against recorded ACP transcripts.
test("describe names the driver and worktree", () => {
  assert.equal(describe({ id: "s1", driver: "opencode", projectId: "p", worktree: "/w" }), "opencode session s1 in /w (project p)");
});
