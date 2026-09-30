import { readFileSync } from "node:fs";
import path from "node:path";
import { expect, request as playwrightRequest, type APIRequestContext } from "@playwright/test";
import type { Lease, PipelineList, PipelineRun, StepKindDescriptor, Worker } from "../src/api/gen/types.gen";
import { API_URL } from "./agent";

// A scripted worker for the specs: it speaks the worker protocol (workerRegistrations.new, workerLeases.claim |
// report | release, workerLogs.new) with the worker credential the e2e stack writes to CADENCE_WORKER_TOKEN_FILE
// (e2e/stack.sh), so a spec can put a step job in the queue, lease it, stream its log and end it — without the
// Python worker or a GPU. It publishes one runtime-neutral kind, dataset_import@1, which the bundled `import`
// pipeline uses (no input artifact needed).

const TOKEN_FILE = path.resolve(import.meta.dirname, "../.e2e/worker-token");

const param = (description: string, def: string) => ({ type: "string", default: def, "x-cadence": { default: def, description, source: "e2e", range: { maxLength: 200 } } });

const DATASET_IMPORT: StepKindDescriptor = {
  version: "1",
  params: {
    type: "object",
    properties: {
      name: param("Dataset collection name", "fleurs-he"),
      source_name: param("Source name", "fleurs"),
      licence: param("Licence of the corpus", "CC-BY-4.0"),
      locale: param("Locale", "he-IL"),
      hf_config: param("Hugging Face configuration", "he_il"),
      hf_revision: param("Hugging Face revision", "main"),
    },
  },
  consumes: {},
  produces: { dataset: "dataset" },
  resources: { gpu: false, jobKind: "data" },
  neutral: true,
  help: "steps.dataset-import",
};

export class ScriptedWorker {
  private readonly ctx: APIRequestContext;
  readonly worker: Worker;

  private constructor(ctx: APIRequestContext, worker: Worker) {
    this.ctx = ctx;
    this.worker = worker;
  }

  static async connect(): Promise<ScriptedWorker> {
    const token = readFileSync(TOKEN_FILE, "utf8").trim();
    const ctx = await playwrightRequest.newContext({ baseURL: API_URL, extraHTTPHeaders: { Authorization: `Bearer ${token}` } });
    const res = await ctx.post("/api/worker-registrations", {
      data: { host: "staging", instance: `e2e-${process.pid}`, runtime: { name: "e2e", version: "1" }, stepKinds: { dataset_import: DATASET_IMPORT } },
    });
    expect(res.status(), await res.text()).toBeLessThan(300);
    return new ScriptedWorker(ctx, (await res.json()) as Worker);
  }

  /** Claims the next lease (or none), reporting one card's telemetry. */
  async claim(usedMb = 24000): Promise<Lease | undefined> {
    const res = await this.ctx.post("/api/worker-leases:claim", {
      data: { workerId: this.worker.id, wait: 0, cards: [{ index: 0, name: "e2e card", memoryTotalMb: 49152, memoryUsedMb: usedMb, utilization: 0.3 }] },
    });
    expect(res.status(), await res.text()).toBe(200);
    return ((await res.json()) as { lease?: Lease }).lease;
  }

  /** Claims until the given job is leased (the scheduler may need a moment after enqueueing). */
  async claimJob(jobId: string): Promise<Lease> {
    let lease: Lease | undefined;
    await expect
      .poll(
        async () => {
          lease = lease ?? (await this.claim());
          return lease?.jobId;
        },
        { timeout: 15_000 },
      )
      .toBe(jobId);
    return lease!;
  }

  async report(lease: Lease, fraction: number, message: string): Promise<{ stop: boolean }> {
    const res = await this.ctx.post(`/api/worker-leases/${lease.id}:report`, { data: { progress: { fraction, message } } });
    expect(res.status(), await res.text()).toBe(200);
    return (await res.json()) as { stop: boolean };
  }

  async log(lease: Lease, lines: { level?: "debug" | "info" | "warn" | "error"; msg: string }[]): Promise<void> {
    const body = lines.map((l) => JSON.stringify({ t: new Date().toISOString(), level: l.level ?? "info", msg: l.msg })).join("\n");
    const res = await this.ctx.post(`/api/worker-leases/${lease.id}/worker-logs`, { data: body, headers: { "Content-Type": "application/x-ndjson" } });
    expect(res.status(), await res.text()).toBeLessThan(300);
  }

  async fail(lease: Lease, message: string): Promise<void> {
    const res = await this.ctx.post(`/api/worker-leases/${lease.id}:release`, { data: { state: "failed", error: { type: "step", message, retryable: false } } });
    expect(res.status(), await res.text()).toBeLessThan(300);
  }

  async dispose(): Promise<void> {
    await this.ctx.dispose();
  }
}

/** Starts the bundled `import` pipeline in a project as the person (pipelines.run with the pipeline's version). */
export async function runImport(request: APIRequestContext, slug: string): Promise<PipelineRun> {
  const list = await request.get(`/api/projects/${slug}/pipelines`);
  expect(list.status(), await list.text()).toBe(200);
  const p = ((await list.json()) as PipelineList).items.find((x) => x.name === "import");
  expect(p, "the import pipeline").toBeTruthy();
  const res = await request.post(`/api/projects/${slug}/pipelines/import:run`, { data: {}, headers: { "Idempotency-Key": `run-${slug}-${Date.now()}`, "If-Match": `"${p!.version}"` } });
  expect(res.status(), await res.text()).toBe(201);
  return (await res.json()) as PipelineRun;
}
