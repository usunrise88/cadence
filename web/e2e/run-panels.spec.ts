import { execFileSync } from "node:child_process";
import path from "node:path";
import type { APIRequestContext } from "@playwright/test";
import { openMix } from "./agent";
import { expect, openWorkspace, test } from "./fixtures";
import { FIXTURE_TRAIN_STAGE, ScriptedWorker } from "./worker";

// The Run, Metrics and Checkpoints panels against the real control plane: the stack seeds the fixture family's base
// model, a trainable dataset version and a calibration (e2e/stack.sh seed-training); a scripted worker publishes the
// family's train step. The Mix document launches a run (estimate first), the Run document opens and follows its
// status, the worker's metric points draw live in Metrics, and a failed run offers Resume from checkpoint.

const SEED = path.resolve(import.meta.dirname, "../.e2e/seed-training");

async function trainingProject(request: APIRequestContext, seededBase: string, dataset: string): Promise<string> {
  // A project whose base model is the fixture family's (the wizard's base model field).
  const slug = `e2e-train-${Date.now().toString(36)}`;
  const created = await request.post("/api/projects", { data: { slug, name: `Train ${slug}`, baseModel: seededBase }, headers: { "Idempotency-Key": `key-${slug}` } });
  expect(created.status(), await created.text()).toBe(202);
  const { jobId } = (await created.json()) as { jobId: string };
  await expect.poll(async () => ((await (await request.get(`/api/jobs/${jobId}:wait`, { params: { timeout: 10 } })).json()) as { state: string }).state, { timeout: 60_000 }).toBe("done");
  const file = `/api/projects/${slug}/recipes/${encodeURIComponent("pipelines/train-stage.yaml")}`;
  const cur = await request.get(file);
  const key = `recipe-${slug}`;
  const res =
    cur.status() === 200
      ? await request.patch(file, {
          data: { content: FIXTURE_TRAIN_STAGE, message: "train-stage: fixture family (e2e)" },
          headers: { "Idempotency-Key": key, "If-Match": `"${((await cur.json()) as { history: { sha: string }[] }).history[0]!.sha}"` },
        })
      : await request.post(`/api/projects/${slug}/recipes`, { data: { path: "pipelines/train-stage.yaml", content: FIXTURE_TRAIN_STAGE }, headers: { "Idempotency-Key": key } });
  expect(res.status(), await res.text()).toBeLessThan(300);
  const mix = await request.post(`/api/projects/${slug}/mixes`, {
    data: { name: "he-mix", groups: [{ name: "target", datasets: [dataset] }] },
    headers: { "Idempotency-Key": `mix-${slug}` },
  });
  expect(mix.status(), await mix.text()).toBe(201);
  return slug;
}

test("a run launched from a mix: estimate, Run document, live metrics, failure and resume", async ({ page, request }) => {
  const seeded = JSON.parse(execFileSync(SEED, ["--dataset", "e2e-he"], { encoding: "utf8" })) as { baseModel: string; dataset: string };
  const worker = await ScriptedWorker.connect();
  try {
    const slug = await trainingProject(request, seeded.baseModel, seeded.dataset);
    await openWorkspace(page, slug, "Training");
    await page.locator('[data-tab="metrics"]').click();
    await expect(page.locator('[data-panel="metrics"]')).toContainText("No run yet");
    await page.locator('[data-tab="checkpoints"]').click();
    await expect(page.locator('[data-panel="checkpoints"]')).toContainText("No run yet");

    // Launch from the mix: the measured estimate first, then the run.
    await openMix(page, "he-mix");
    const mixPanel = page.locator('[data-panel="mix"]');
    await mixPanel.getByRole("button", { name: "Launch a run with this mix" }).click();
    const card = mixPanel.locator('[data-slot="run-launch"]');
    await expect(card.locator('[data-slot="run-estimate"]')).toContainText("measured by calibration");
    await card.getByRole("spinbutton").fill("40");
    await card.getByRole("button", { name: "Estimate again" }).click();
    await expect(card.locator('[data-slot="run-estimate"]')).toContainText("40 × 0.5 s");
    await card.getByRole("button", { name: "Start run" }).click();

    const run = page.locator('[data-panel="run"]');
    await expect(run.locator('[data-slot="entity-header"]')).toContainText("he-mix r1");
    const overview = run.locator("[data-run]");
    const runId = (await overview.getAttribute("data-run"))!;
    await expect(overview).toHaveAttribute("data-status", "queued");
    await expect(run.locator('[data-stage="train"]')).toBeVisible();
    await expect(run.getByRole("table", { name: "Departures from defaults" })).toContainText("40");

    // The worker leases the train step: the run is running; its points draw live in Metrics.
    let jobId: string | undefined;
    await expect
      .poll(async () => {
        const res = await request.get("/api/queue-entries", { params: { project: slug } });
        jobId = ((await res.json()) as { items: { jobId: string; runId?: string }[] }).items.find((e) => e.runId === runId)?.jobId;
        return jobId;
      })
      .toBeTruthy();
    const lease = await worker.claimJob(jobId!);
    await expect(overview).toHaveAttribute("data-status", "running");
    await expect(run.locator('[data-stage="train"]')).toHaveAttribute("data-state", "running");

    await page.locator('[data-tab="metrics"]').click();
    const metrics = page.locator('[data-panel="metrics"]');
    await expect(metrics).toContainText("he-mix r1");
    await worker.metrics(
      lease,
      [1, 2, 3, 4, 5].map((step) => ({ name: "loss", step, value: 3 - step * 0.2 })),
    );
    await expect(metrics.locator('[data-chart="loss"]')).toBeVisible();
    await worker.metrics(lease, [{ name: "val_wer", step: 5, value: 0.42 }]);
    await expect(metrics.locator('[data-chart="val_wer"]')).toBeVisible();
    // The chart's table view holds the raw numbers.
    await metrics.locator('[data-chart="loss"]').getByRole("button", { name: "Table" }).click();
    await expect(metrics.locator('[data-chart="loss"]')).toContainText("2.2");

    // The step fails: the run fails; without a training state, Resume from checkpoint says why.
    await worker.fail(lease, "the fixture step gave up");
    await expect(overview).toHaveAttribute("data-status", "failed");
    await expect(run.locator('[data-stage="train"]')).toContainText("the fixture step gave up");
    await run.locator('[data-slot="action-bar"]').getByRole("button", { name: "Resume from checkpoint" }).click();
    await expect(page.getByText(/training state/i).first()).toBeVisible();

    // Checkpoints follows the run: none were saved.
    await page.locator('[data-tab="checkpoints"]').click();
    await expect(page.locator('[data-panel="checkpoints"]')).toContainText("No checkpoint yet");
  } finally {
    await worker.dispose();
  }
});
