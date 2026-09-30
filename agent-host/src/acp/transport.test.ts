import assert from "node:assert/strict";
import { existsSync, mkdtempSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { test } from "node:test";
import { launch } from "./transport.ts";

test("kill reaches the agent's whole process group; groupGone waits for a descendant that outlives it", async () => {
  const dir = mkdtempSync(join(tmpdir(), "cadence-transport-"));
  try {
    const late = join(dir, "late.txt");
    // The leader (cat) exits on SIGTERM at once; its background child ignores SIGTERM and writes a file afterwards,
    // the way Claude's CLI writes its MCP logs after the adapter has gone.
    const proc = launch({ command: "sh", args: ["-c", `(trap '' TERM; sleep 0.3; echo late > ${late}) & exec cat`], cwd: dir, env: { PATH: process.env.PATH } });
    await new Promise((r) => setTimeout(r, 100));
    proc.kill();
    await proc.exited;
    assert.equal(await proc.groupGone(5000), true);
    assert.ok(existsSync(late), "groupGone resolved only after the descendant finished");
    assert.throws(() => process.kill(-proc.pid!, 0), "no process of the group is left");
  } finally {
    rmSync(dir, { recursive: true, force: true });
  }
});
