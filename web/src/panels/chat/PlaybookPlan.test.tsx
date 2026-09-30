import { afterEach, describe, expect, it } from "vitest";
import { cleanup, render, screen } from "@testing-library/react";
import type { AgentPlaybook } from "@/api/gen/types.gen";
import { PlaybookPlan } from "./PlaybookPlan";

const r = (value: number) => ({ value, low: value / 2, high: value * 1.5 });
const playbook = (over: Partial<AgentPlaybook> = {}): AgentPlaybook => ({
  name: "finetune-from-dataset",
  title: "Fine-tune from a dataset version",
  state: "running",
  inputs: {},
  estimate: { basis: "table", plusMinus: 0.5, gpuHours: r(0.24), durationSeconds: r(860), steps: [], budget: { gpuHoursPerProjectPerDay: 8, withinDailyBudget: true } },
  plan: [
    { id: "mix", title: "Mix the dataset with replay", command: "mixes.new", state: "done", note: "mix_1", spending: false },
    { id: "calibrate", title: "Calibrate", command: "runs.calibrate", state: "running", note: "dry run answered: 0.10 GPU-hours", spending: true },
    { id: "train", title: "Train", command: "runs.new", state: "pending", spending: true },
    { id: "eval", title: "Eval matrix", command: "evals.new", state: "skipped", note: "arrives in roadmap phase 3", spending: false },
  ],
  ...over,
});

afterEach(() => cleanup());

describe("Chat: the playbook plan checklist", () => {
  it("shows the estimate first and every step with the state the server ticked", () => {
    render(<PlaybookPlan playbook={playbook()} />);
    expect(screen.getByLabelText("Plan, 1 of 3 done").textContent).toBe("1/3 done");
    expect(document.querySelector('[data-slot="playbook-estimate"]')?.textContent).toContain("0.24 GPU-hours (0.12–0.36, ±50%, table)");
    const items = [...document.querySelectorAll("[data-item]")].map((li) => `${li.getAttribute("data-item")}=${li.getAttribute("data-state")}`);
    expect(items).toEqual(["mix=done", "calibrate=running", "train=pending", "eval=skipped"]);
    expect(screen.getByRole("img", { name: "in progress" })).toBeTruthy();
    expect(screen.getByText("dry run answered: 0.10 GPU-hours")).toBeTruthy();
    expect(document.querySelector('[data-slot="playbook-summary"]')).toBeNull();
  });

  it("ticks live: a new session value re-renders the checklist", () => {
    const { rerender } = render(<PlaybookPlan playbook={playbook()} />);
    const next = playbook();
    next.plan = next.plan.map((p) => (p.id === "calibrate" ? { ...p, state: "done" as const } : p));
    rerender(<PlaybookPlan playbook={next} />);
    expect(screen.getByLabelText("Plan, 2 of 3 done")).toBeTruthy();
  });

  it("a stopped playbook shows the summary and the next step", () => {
    render(
      <PlaybookPlan
        playbook={playbook({
          state: "stopped",
          stop: { on: "step", when: "failed", message: "job failed" },
          summary: "The playbook stopped (step failed).",
          next: "Read the run's logs.",
        })}
      />,
    );
    expect(screen.getByLabelText("Plan, 1 of 3 done").textContent).toBe("stopped (step failed) · 1/3");
    expect(screen.getByRole("status").textContent).toContain("Next: Read the run's logs.");
  });
});
