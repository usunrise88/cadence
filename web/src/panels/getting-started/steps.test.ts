import { afterEach, describe, expect, it } from "vitest";
import type { EvalGate } from "@/api/gen/types.gen";
import { DISMISS_KEY, isDismissed, retired, setDismissed, setupSteps } from "./steps";

const state = (steps: ReturnType<typeof setupSteps>) => Object.fromEntries(steps.map((s) => [s.id, s.state]));
const step = (steps: ReturnType<typeof setupSteps>, id: string) => steps.find((s) => s.id === id)!;
const gate = (verdict: "passed" | "failed") => ({ verdict, gatesSha: "", checks: [], at: "" }) as unknown as EvalGate;

describe("setupSteps", () => {
  it("starts with only sign-in done and later phases marked", () => {
    const steps = setupSteps({ signedIn: true, projects: 0, datasets: [] });
    expect(state(steps)).toEqual({ admin: "done", mount: "todo", project: "todo", dataset: "later", run: "todo", gate: "todo" });
    expect(step(steps, "run")).toMatchObject({ command: "playbooks.run", blocked: "Open a project first" });
    expect(step(steps, "mount").blocked).toMatch(/Storage panel/);
    expect(state(setupSteps({ signedIn: true, projects: 0, datasets: [], mounts: 1 })).mount).toBe("done");
    expect(steps.every((s) => /^[a-z][a-zA-Z]*\.[a-z]+$/.test(s.command))).toBe(true);
  });

  it("derives the run from runs.list and the gate from evals.list, with the eval to gate", () => {
    const none = setupSteps({ signedIn: true, projects: 1, datasets: [], runs: [], evals: [] });
    expect(state(none)).toMatchObject({ run: "todo", gate: "todo" });
    expect(step(none, "run").blocked).toBeUndefined();
    expect(step(none, "gate").blocked).toMatch(/Evaluate a checkpoint first/);

    const evals = [
      { id: "evl_3", rev: 1, status: "running" as const },
      { id: "evl_2", rev: 4, status: "done" as const, gate: gate("failed") },
      { id: "evl_1", rev: 2, status: "done" as const },
    ];
    const ran = setupSteps({ signedIn: true, projects: 1, datasets: [], runs: [{ status: "done" }], evals });
    expect(state(ran)).toMatchObject({ run: "done", gate: "todo" });
    expect(step(ran, "gate")).toMatchObject({ command: "evals.gate", args: { entity: { id: "evl_2", rev: 4 } } });
    expect(retired({ evals })).toBe(false);

    const passed = [{ id: "evl_4", rev: 3, status: "done" as const, gate: gate("passed") }, ...evals];
    expect(state(setupSteps({ signedIn: true, projects: 1, datasets: [], runs: [{ status: "done" }], evals: passed }))).toMatchObject({ gate: "done" });
    expect(retired({ evals: passed })).toBe(true);
  });

  it("ticks the project and a dataset someone froze — bundled fixtures do not count", () => {
    const bundled = { state: "frozen" as const, actor: { kind: "automation" as const, id: "cadence" } };
    expect(state(setupSteps({ signedIn: true, projects: 1, datasets: [bundled] }))).toMatchObject({ project: "done", dataset: "later" });
    const mine = { state: "frozen" as const, actor: { kind: "agent" as const, id: "crd_1" } };
    expect(state(setupSteps({ signedIn: true, projects: 1, datasets: [bundled, mine] }))).toMatchObject({ dataset: "done" });
  });
});

describe("dismiss", () => {
  afterEach(() => localStorage.removeItem(DISMISS_KEY));
  it("is remembered in this browser", () => {
    expect(isDismissed()).toBe(false);
    setDismissed(true);
    expect(isDismissed()).toBe(true);
    setDismissed(false);
    expect(isDismissed()).toBe(false);
  });
});
