import { beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import { commands } from "@/shell/registries";
import { commandContext } from "@/shell/state";
import { registerTrainingCommands } from "./training";

// The phase-2 commands: one API operation each, with If-Match from what the panel shows — a queue entry has no
// revision, so the job commands read it first; recipes.edit sends the commit that last changed the file.

const sdk = vi.hoisted(() => ({
  jobsGet: vi.fn(),
  jobsEdit: vi.fn(),
  jobsPause: vi.fn(),
  jobsResume: vi.fn(),
  jobsCancel: vi.fn(),
  pipelineRunsRetry: vi.fn(),
  pipelineRunsCancel: vi.fn(),
  mixesPreview: vi.fn(),
  recipesEdit: vi.fn(),
  recipesNew: vi.fn(),
}));
vi.mock("@/api/gen/sdk.gen", async (orig) => ({ ...(await orig<object>()), ...sdk }));

beforeAll(() => {
  if (!commands.get("jobs.pause")) registerTrainingCommands();
});
beforeEach(() => {
  for (const f of Object.values(sdk)) f.mockReset().mockResolvedValue({ data: { id: "x", rev: 4 } });
});

const run = (id: string, args: unknown) => commands.run(id, commandContext(), args);

describe("training commands", () => {
  it("job commands read the job's revision when the queue entry has none", async () => {
    await run("jobs.pause", { jobId: "job_1" });
    expect(sdk.jobsGet).toHaveBeenCalledWith(expect.objectContaining({ path: { id: "job_1" } }));
    expect(sdk.jobsPause.mock.calls[0]![0]).toMatchObject({ path: { id: "job_1" }, headers: { "If-Match": '"4"' } });
    sdk.jobsGet.mockClear();
    await run("jobs.edit", { jobId: "job_1", rev: 7, priority: 3 });
    expect(sdk.jobsGet).not.toHaveBeenCalled();
    expect(sdk.jobsEdit.mock.calls[0]![0]).toMatchObject({ body: { priority: 3 }, headers: { "If-Match": '"7"' } });
    await run("jobs.resume", { jobId: "job_2", rev: 1 });
    await run("jobs.cancel", { jobId: "job_3", rev: 2 });
    expect(sdk.jobsResume.mock.calls[0]![0].headers["Idempotency-Key"]).toBeTruthy();
    expect(sdk.jobsCancel.mock.calls[0]![0]).toMatchObject({ path: { id: "job_3" }, headers: { "If-Match": '"2"' } });
  });

  it("pipeline run commands send the run's revision", async () => {
    await run("pipelineRuns.retry", { run: { id: "plr_1", rev: 5 }, body: { step: "train" } });
    expect(sdk.pipelineRunsRetry.mock.calls[0]![0]).toMatchObject({ path: { id: "plr_1" }, body: { step: "train" }, headers: { "If-Match": '"5"' } });
    await run("pipelineRuns.cancel", { run: { id: "plr_1", rev: 6 } });
    expect(sdk.pipelineRunsCancel.mock.calls[0]![0]).toMatchObject({ headers: { "If-Match": '"6"' } });
  });

  it("mixes.preview needs no revision; recipes.edit sends the file's last commit", async () => {
    await run("mixes.preview", { project: "demo", body: { name: "m", groups: [] } });
    const p = sdk.mixesPreview.mock.calls[0]![0];
    expect(p.path).toEqual({ p: "demo" });
    expect(p.headers["If-Match"]).toBeUndefined();
    await run("recipes.edit", { project: "demo", path: "augment/telephony.yaml", expect: "abc1234", body: { content: "seed: 1\n" } });
    expect(sdk.recipesEdit.mock.calls[0]![0]).toMatchObject({ path: { p: "demo", path: "augment/telephony.yaml" }, headers: { "If-Match": '"abc1234"' } });
    await run("recipes.new", { project: "demo", body: { path: "augment/x.yaml", content: "" } });
    expect(sdk.recipesNew.mock.calls[0]![0].headers["If-Match"]).toBeUndefined();
  });

  it("refuses to run from the palette without arguments", async () => {
    await expect(run("jobs.cancel", undefined)).rejects.toThrow(/from its panel/);
  });
});
