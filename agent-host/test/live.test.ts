// Live driver tests: the A1 lifecycle against the real agents. Off unless CADENCE_LIVE_AGENTS names the drivers
// (e.g. CADENCE_LIVE_AGENTS=claude,opencode npm test); they need a Claude login / an opencode provider and cost tokens.
// Models: CADENCE_LIVE_CLAUDE_MODEL (default haiku), CADENCE_LIVE_OPENCODE_MODEL (default opencode/big-pickle).

import assert from "node:assert/strict";
import { mkdir, mkdtemp, readFile, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { test } from "node:test";
import { drivers } from "../src/drivers/index.ts";
import type { DriverName } from "../src/drivers/types.ts";
import { runLifecycle } from "../src/spikes/lifecycle.ts";
import { startMcpEcho } from "../src/spikes/mcp-echo.ts";

const live = new Set((process.env.CADENCE_LIVE_AGENTS ?? "").split(",").filter(Boolean));
const models: Record<DriverName, string> = {
  claude: process.env.CADENCE_LIVE_CLAUDE_MODEL ?? "haiku",
  opencode: process.env.CADENCE_LIVE_OPENCODE_MODEL ?? "opencode/big-pickle",
};

for (const name of ["claude", "opencode"] as const) {
  test(`live ${name}: A1 lifecycle`, { skip: !live.has(name) && "set CADENCE_LIVE_AGENTS", timeout: 300_000 }, async () => {
    const root = await mkdtemp(join(tmpdir(), `a1-live-${name}-`));
    const cwd = join(root, "workspace");
    await mkdir(cwd);
    await writeFile(join(cwd, "notes.txt"), "alpha\n");
    await writeFile(join(cwd, "opencode.json"), JSON.stringify({ permission: { edit: "ask", bash: "ask", "cadence_*": "ask" } }));
    const env: NodeJS.ProcessEnv = {};
    for (const x of ["CONFIG", "DATA", "STATE", "CACHE"]) env[`XDG_${x}_HOME`] = join(root, x.toLowerCase());
    const mcp = await startMcpEcho("cst_live", "abc123");
    try {
      const res = await runLifecycle({
        driver: drivers[name],
        cwd,
        mcp: { url: mcp.url, token: "cst_live" },
        launch: () => ({ env, model: models[name], thoughts: true }),
      });
      assert.equal(mcp.log.unauthorized, 0);
      assert.deepEqual(mcp.log.calls, [{ text: "ping", reply: "echo:ping:abc123" }]);
      assert.deepEqual(res.steps.map((s) => s.stopReason), ["end_turn", "end_turn", "end_turn", "cancelled", "end_turn"]);
      assert.ok(res.diffs.some((d) => d.path.endsWith("notes.txt")));
      assert.equal(await readFile(join(cwd, "notes.txt"), "utf8"), "beta\n");
      assert.match(res.steps.at(-1)?.text ?? "", /echo:ping:abc123/);
    } finally {
      await mcp.close();
      await rm(root, { recursive: true, force: true });
    }
  });
}
