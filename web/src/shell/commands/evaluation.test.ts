import { beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import { useEditRequests } from "@/shell/entity/edits";
import { commands } from "@/shell/registries";
import { commandContext } from "@/shell/state";
import { registerEvaluationCommands } from "./evaluation";
import { registerExperimentCommands } from "./experiments";

// Evaluation setup commands (adopt, baseline, gates.yaml, Run eval): one API operation each, with the If-Match each
// operation wants; without their arguments they ask the open document for its card or form.

const sdk = vi.hoisted(() => ({
  projectsGet: vi.fn(),
  projectsAdopt: vi.fn(),
  aliasesGet: vi.fn(),
  aliasesSet: vi.fn(),
  gatesEdit: vi.fn(),
  evalsGate: vi.fn(),
}));
const openDocument = vi.hoisted(() => vi.fn());
const notify = vi.hoisted(() => vi.fn());
const notifyError = vi.hoisted(() => vi.fn());
vi.mock("@/shell/panel/actions", async (orig) => ({ ...(await orig<object>()), openDocument }));
vi.mock("@/shell/notifications/store", async (orig) => ({ ...(await orig<object>()), notify, notifyError }));
vi.mock("@/api/gen/sdk.gen", async (orig) => ({ ...(await orig<object>()), ...sdk }));

beforeAll(() => {
  if (!commands.get("sweeps.run")) registerExperimentCommands();
  if (!commands.get("projects.adopt")) registerEvaluationCommands();
});
beforeEach(() => {
  for (const f of Object.values(sdk)) f.mockReset();
  openDocument.mockReset();
  notify.mockReset();
  notifyError.mockReset();
});

const run = (id: string, args: unknown, activeDoc: string | null = null) => commands.run(id, { ...commandContext(), project: "demo", activeDoc }, args);
const seq = (doc: string) => useEditRequests.getState().seq[doc] ?? 0;

describe("evaluation setup commands", () => {
  it("projects.adopt reads the project's revision for If-Match; from a header it asks the golden set's adopt card", async () => {
    sdk.projectsGet.mockResolvedValue({ data: { rev: 7 } });
    sdk.projectsAdopt.mockResolvedValue({ data: { projectId: "prj_1" } });
    await run("projects.adopt", { version: "ver_g", dryRun: true });
    expect(sdk.projectsAdopt.mock.calls[0]![0]).toMatchObject({ path: { p: "demo" }, body: { version: "ver_g" }, query: { dryRun: true }, headers: { "If-Match": '"7"' } });
    const before = seq("adopt:golden_set:ver_g");
    expect(await run("projects.adopt", { entity: { id: "ver_g", name: "g", state: "frozen" } })).toBeUndefined();
    expect(openDocument).toHaveBeenCalledWith("golden_set:ver_g");
    expect(seq("adopt:golden_set:ver_g")).toBe(before + 1);
    expect(sdk.projectsAdopt).toHaveBeenCalledTimes(1);
  });

  it("aliases.set sends the alias's revision when it exists, and from a header reports the approval", async () => {
    sdk.aliasesGet.mockResolvedValue({ data: { rev: 3 } });
    sdk.aliasesSet.mockResolvedValue({ data: { approvalId: "apr_9" } });
    const res = await run("aliases.set", { version: "ver_m" });
    expect(res).toEqual({ approvalId: "apr_9" });
    expect(sdk.aliasesSet.mock.calls[0]![0]).toMatchObject({ path: { p: "demo", name: "baseline" }, body: { version: "ver_m" }, headers: { "If-Match": '"3"' } });
    expect(notify).not.toHaveBeenCalled();

    sdk.aliasesGet.mockResolvedValue({ data: undefined, error: { status: 404 } });
    await run("aliases.set", { entity: { id: "ver_base", name: "base-model/x", state: "frozen" } });
    expect(sdk.aliasesSet.mock.calls[1]![0].headers["If-Match"]).toBeUndefined();
    expect(sdk.aliasesSet.mock.calls[1]![0].body).toEqual({ version: "ver_base" });
    expect(notify.mock.calls[0]![0]).toMatchObject({ title: "@baseline waits for an approval", detail: expect.stringContaining("apr_9") });

    // The palette with a Model document open: its version.
    await run("aliases.set", undefined, "model:ver_open");
    expect(sdk.aliasesSet.mock.calls[2]![0].body).toEqual({ version: "ver_open" });
    // Nothing to point at: said as a notice, not thrown.
    expect(await run("aliases.set", undefined)).toBeUndefined();
    expect(notifyError).toHaveBeenCalledTimes(1);
  });

  it("gates.edit sends the ETag of gates.get; without a body it opens the Project home's gate editor", async () => {
    sdk.gatesEdit.mockResolvedValue({ data: { exists: true } });
    await run("gates.edit", { expect: "defaults", body: { content: "primaryProfile: 160ms\n" }, dryRun: true });
    expect(sdk.gatesEdit.mock.calls[0]![0]).toMatchObject({ path: { p: "demo" }, query: { dryRun: true }, headers: { "If-Match": '"defaults"' } });
    const before = seq("gate:project:demo");
    expect(await run("gates.edit", {})).toBeUndefined();
    expect(openDocument).toHaveBeenCalledWith("project:demo");
    expect(seq("gate:project:demo")).toBe(before + 1);
  });

  it("evals.new without a body asks the Eval report for its Run eval form", async () => {
    const before = seq("evalnew:eval:evl_1");
    expect(await run("evals.new", { entity: { id: "evl_1", name: "e", state: "done" } })).toBeUndefined();
    expect(openDocument).toHaveBeenCalledWith("eval:evl_1");
    expect(seq("evalnew:eval:evl_1")).toBe(before + 1);
    await expect(run("evals.new", {})).rejects.toThrow(/open an Eval report/);
  });

  it("evals.gate runs on an eval by id without an open report (Getting started)", async () => {
    sdk.evalsGate.mockResolvedValue({ data: { id: "evl_2", gate: { verdict: "passed", checks: [] } } });
    await run("evals.gate", { entity: { id: "evl_2", rev: 4, name: "e", state: "done" } }, "project:demo");
    expect(sdk.evalsGate.mock.calls[0]![0]).toMatchObject({ path: { id: "evl_2" }, headers: { "If-Match": '"4"' } });
    expect(notify.mock.calls[0]![0]).toMatchObject({ title: "Gate passed" });
  });
});
