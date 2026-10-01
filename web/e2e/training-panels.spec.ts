import { expect, newProject, openWorkspace, test } from "./fixtures";
import { runImport, ScriptedWorker } from "./worker";

// The phase-2 tool panels against the real control plane: a pipeline run puts a step job in the queue, a scripted
// worker leases it, streams its log and fails it. Queue & GPU shows the job, pauses, resumes and cancels it and draws
// the card's telemetry; Pipeline run shows the step's state, its log and retries it; Logs follows the focused job.

type QueueItem = { jobId: string; state: string; pipelineRunId?: string };

async function queued(request: import("@playwright/test").APIRequestContext, slug: string, runId: string): Promise<QueueItem> {
  let item: QueueItem | undefined;
  await expect
    .poll(
      async () => {
        const res = await request.get("/api/queue-entries", { params: { project: slug } });
        item = ((await res.json()) as { items: QueueItem[] }).items.find((e) => e.pipelineRunId === runId);
        return item?.state;
      },
      { timeout: 15_000 },
    )
    .toBeTruthy();
  return item!;
}

test("Queue & GPU: a step job waits, pauses, resumes and is cancelled; the card's telemetry arrives live", async ({ page, request }) => {
  const slug = await newProject(request, "Queue");
  await openWorkspace(page, slug, "Ops");
  const panel = page.locator('[data-panel="queue-gpu"]');
  await expect(panel.getByText("Nothing waiting.")).toBeVisible();
  const card = panel.locator('[data-card="staging#0"]');
  await expect(card).toContainText("Training slot: free");
  await expect(card).toContainText("Availability: any time");

  // The worker publishes the step kind first: a pipeline naming a kind no runtime offers is refused.
  const worker = await ScriptedWorker.connect();
  try {
    const run = await runImport(request, slug);
    const item = await queued(request, slug, run.id);
    const row = panel.locator(`[data-job="${item.jobId}"]`);
    await expect(row).toHaveAttribute("data-state", "waiting");
    await expect(row).toContainText("dataset_import@1");
    await expect(row).toContainText(slug);
    // The only waiting job cannot move.
    await expect(row.getByRole("button", { name: "Move dataset_import up" })).toBeDisabled();

    await row.getByRole("button", { name: "Pause" }).click();
    await expect(row).toHaveAttribute("data-state", "paused");
    await row.getByRole("button", { name: "Resume" }).click();
    await expect(row).toHaveAttribute("data-state", "waiting");

    // A worker's claim reports the card: everything used is resident while no Cadence job holds the card. The job
    // needs no card (data), so the claim leases it and it runs without one.
    await worker.claim(24000);
    await expect(card.locator('[data-slot="memory"]')).toContainText("Resident services 23.4 GB");
    await expect(card).toContainText("healthy");
    await expect(panel.locator(`[data-job="${item.jobId}"]`)).toHaveAttribute("data-state", "running");

    // Cancel asks twice and removes the job from the queue (a leased job stops when its worker is told).
    const cancelRow = panel.locator(`[data-job="${item.jobId}"]`);
    await cancelRow.getByRole("button", { name: "Cancel" }).click();
    await cancelRow.getByRole("button", { name: "Confirm cancel" }).click();
    await expect
      .poll(async () => {
        const res = await request.get(`/api/jobs/${item.jobId}`);
        const j = (await res.json()) as { state: string; cancelRequestedAt?: string };
        return j.state === "cancelled" || !!j.cancelRequestedAt;
      })
      .toBe(true);
  } finally {
    await worker.dispose();
  }
});

test("Pipeline run: a step's state, attempts and log arrive live; Logs follows it; a failed step is retried", async ({ page, request }) => {
  const slug = await newProject(request, "Pipeline");
  await openWorkspace(page, slug, "Data");
  await page.locator('[data-tab="pipeline-run"]').click();
  const panel = page.locator('[data-panel="pipeline-run"]');
  await expect(panel.getByText("No pipeline run yet")).toBeVisible();

  const worker = await ScriptedWorker.connect();
  const run = await runImport(request, slug);
  const step = panel.locator('[data-step="import"]');
  await expect(panel.locator(`[data-run="${run.id}"]`)).toBeVisible();
  await expect(step).toHaveAttribute("data-state", /waiting|queued/);
  const item = await queued(request, slug, run.id);

  try {
    const lease = await worker.claimJob(item.jobId);
    await expect(step).toHaveAttribute("data-state", "running");
    // Opening the step focuses its job: its log is embedded here and in the Logs panel.
    await step.getByRole("button", { name: /^import/ }).click();
    const embedded = step.getByRole("log", { name: "Log of import" });
    await worker.log(lease, [{ msg: "downloading he_il" }, { level: "warn", msg: "slow mirror" }]);
    await expect(embedded).toContainText("downloading he_il");
    await expect(embedded.locator('[data-level="warn"]')).toContainText("slow mirror");
    const logs = page.locator('[data-panel="logs"]');
    await expect(logs.getByRole("log")).toContainText("slow mirror");

    await worker.log(lease, [{ level: "error", msg: "the mirror refused the download" }]);
    await worker.fail(lease, "the mirror refused the download");
    await expect(step).toHaveAttribute("data-state", "failed");
    await expect(step).toContainText("step: the mirror refused the download");
    await expect(panel.locator(`[data-run="${run.id}"]`)).toHaveAttribute("data-state", "failed");
    await expect(embedded.locator('[data-level="error"]')).toContainText("refused");
    await expect(step.getByRole("table", { name: "Attempts of import" })).toContainText("initial");

    // Retry puts the step back in the queue as attempt 2.
    await step.getByRole("button", { name: "Retry" }).click();
    await expect(step).toHaveAttribute("data-state", /waiting|queued/);
    await expect(step.getByRole("table", { name: "Attempts of import" })).toContainText("retry");
    await expect(panel.locator(`[data-run="${run.id}"]`)).toHaveAttribute("data-state", "running");
  } finally {
    await worker.dispose();
  }
});
