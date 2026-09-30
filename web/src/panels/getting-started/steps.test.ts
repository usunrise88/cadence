import { afterEach, describe, expect, it } from "vitest";
import { DISMISS_KEY, isDismissed, setDismissed, setupSteps } from "./steps";

const state = (steps: ReturnType<typeof setupSteps>) => Object.fromEntries(steps.map((s) => [s.id, s.state]));

describe("setupSteps", () => {
  it("starts with only sign-in done and later phases marked", () => {
    const steps = setupSteps({ signedIn: true, projects: 0, datasets: [] });
    expect(state(steps)).toEqual({ admin: "done", mount: "later", project: "todo", dataset: "later", run: "later", gate: "later" });
    expect(steps.find((s) => s.id === "run")).toMatchObject({ phase: 2, command: "runs.new" });
    expect(steps.every((s) => /^[a-z][a-zA-Z]*\.[a-z]+$/.test(s.command))).toBe(true);
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
