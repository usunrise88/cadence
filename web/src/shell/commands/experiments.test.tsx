import { beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import { TooltipProvider } from "@/components/ui/tooltip";
import { experimentEntity } from "@/entities/experiment";
import { useEditRequests } from "@/shell/entity/edits";
import { ActionBar } from "@/shell/entity/primitives";
import { commands } from "@/shell/registries";
import { commandContext } from "@/shell/state";
import { registerEvaluationCommands } from "./evaluation";
import { registerExperimentCommands } from "./experiments";

// The Experiment document's commands: one API operation each; sweeps.run carries the experiment's revision, and
// without a body it (and models.register from the header) asks the open document instead.

const sdk = vi.hoisted(() => ({ experimentsNew: vi.fn(), sweepsRun: vi.fn(), modelsRegister: vi.fn(), evalsNew: vi.fn() }));
const openDocument = vi.hoisted(() => vi.fn());
const openPanelById = vi.hoisted(() => vi.fn());
vi.mock("@/shell/panel/actions", async (orig) => ({ ...(await orig<object>()), openDocument, openPanelById }));
vi.mock("@/api/gen/sdk.gen", async (orig) => ({ ...(await orig<object>()), ...sdk }));

beforeAll(() => {
  if (!commands.get("sweeps.run")) registerExperimentCommands();
  if (!commands.get("models.register")) registerEvaluationCommands();
});
beforeEach(() => {
  for (const f of Object.values(sdk)) f.mockReset().mockResolvedValue({ data: { id: "x", rev: 1 } });
  openDocument.mockReset();
  openPanelById.mockReset();
});

const run = (id: string, args: unknown) => commands.run(id, { ...commandContext(), project: "demo" }, args);
const entity = { id: "exp_1", name: "lr", state: "done", rev: 5 };

describe("experiment commands", () => {
  it("experiments.new without a body opens the Experiment panel's form; with one it calls the API", async () => {
    expect(await run("experiments.new", undefined)).toBeUndefined();
    expect(openPanelById).toHaveBeenCalledWith("experiment");
    await run("experiments.new", { project: "demo", body: { name: "lr", question: "q", mix: "mix_1" }, dryRun: true });
    expect(sdk.experimentsNew.mock.calls[0]![0]).toMatchObject({ path: { p: "demo" }, query: { dryRun: true } });
  });

  it("sweeps.run sends the experiment's revision; without a body it opens the sweep form", async () => {
    await run("sweeps.run", { experiment: { id: "exp_1", rev: 5 }, body: { parameters: [{ name: "peak_lr", values: [0.0001] }] }, dryRun: true });
    expect(sdk.sweepsRun.mock.calls[0]![0]).toMatchObject({ path: { id: "exp_1" }, query: { dryRun: true }, headers: { "If-Match": '"5"' } });
    const before = useEditRequests.getState().seq["experiment:exp_1"] ?? 0;
    expect(await run("sweeps.run", { entity })).toBeUndefined();
    expect(openDocument).toHaveBeenCalledWith("experiment:exp_1");
    expect(useEditRequests.getState().seq["experiment:exp_1"]).toBe(before + 1);
    expect(sdk.sweepsRun).toHaveBeenCalledTimes(1);
  });

  it("models.register calls the API with a body, else asks the Experiment document", async () => {
    await run("models.register", { project: "demo", body: { checkpointId: "ckp_b" } });
    expect(sdk.modelsRegister.mock.calls[0]![0]).toMatchObject({ path: { p: "demo" }, body: { checkpointId: "ckp_b" } });
    expect(sdk.modelsRegister.mock.calls[0]![0].headers["If-Match"]).toBeUndefined();
    await run("models.register", { entity });
    expect(useEditRequests.getState().seq["register:experiment:exp_1"]).toBe(1);
    await run("evals.new", { project: "demo", body: { subject: { checkpointId: "ckp_b" } }, dryRun: true });
    expect(sdk.evalsNew.mock.calls[0]![0]).toMatchObject({ path: { p: "demo" }, query: { dryRun: true } });
  });

  it("the Experiment header's verbs run another entity's commands (EntityVerb.command)", () => {
    const e = { ...entity, experiment: { sweeps: [], runCount: 2, best: { registrable: false, reason: "no gate yet" } } };
    render(
      <TooltipProvider>
        <ActionBar manifest={experimentEntity} entity={e} />
      </TooltipProvider>,
    );
    const sweep = screen.getByRole("button", { name: "Run sweep" });
    expect(sweep.getAttribute("data-command")).toBe("sweeps.run");
    expect((sweep as HTMLButtonElement).disabled).toBe(false);
    const register = screen.getByRole("button", { name: "Register model version" });
    expect(register.getAttribute("data-command")).toBe("models.register");
    expect((register as HTMLButtonElement).disabled).toBe(true);
  });
});
