import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import type { ReactNode } from "react";
import { playbooksListQueryKey } from "@/api/gen/@tanstack/react-query.gen";
import type { AgentPlaybook, Playbook, PlaybookEstimate, PlaybookRunResult } from "@/api/gen/types.gen";
import { PlaybookLauncher } from "./PlaybookLauncher";
import { askedInputs, budgetNote, estimateLine, initialValues, inputProblem, planProgress, playbookStateLabel, runInputs } from "./playbooks";

const runCommand = vi.fn();
vi.mock("@/shell/commands/api", async (orig) => ({ ...(await orig<object>()), runCommand: (...a: unknown[]) => runCommand(...a) }));

const range = (value: number, pm = 0.5) => ({ value, low: +(value * (1 - pm)).toFixed(3), high: +(value * (1 + pm)).toFixed(3) });
const estimate: PlaybookEstimate = {
  basis: "mixed",
  plusMinus: 0.5,
  gpuHours: range(0.933),
  durationSeconds: range(3360),
  budget: { gpuHoursPerProjectPerDay: 8, withinDailyBudget: true },
  steps: [],
};

const finetune: Playbook = {
  name: "finetune-from-dataset",
  title: "Fine-tune from a dataset version",
  description: "Mix, calibrate, train.",
  typicalCost: "1–4 GPU-hours",
  availableFrom: 2,
  runnable: true,
  version: "2026-09-30.abc",
  prompt: "",
  stop: [],
  chain: [],
  estimate,
  inputs: [
    { name: "dataset", type: "dataset_version", required: true, multiple: true, description: "The dataset version(s)" },
    { name: "base", type: "base_model", required: false, multiple: false, from: "project", default: "ver_base" },
    { name: "steps", type: "integer", required: false, multiple: false, defaultRef: "training.steps", default: 3000, min: 1, max: 200000 },
    { name: "replayShare", type: "number", required: false, multiple: false, defaultRef: "mix.replay_share", default: 0.15, min: 0, max: 0.9 },
  ],
};
const later: Playbook = { ...finetune, name: "adapt-new-language", title: "Adapt a new language", availableFrom: 4, runnable: false, unavailable: "runs from roadmap phase 4" };

describe("playbook form model", () => {
  it("asks for the inputs without a project fact and starts from their defaults", () => {
    expect(askedInputs(finetune).map((i) => i.name)).toEqual(["dataset", "steps", "replayShare"]);
    expect(initialValues(finetune)).toEqual({ dataset: "", steps: "3000", replayShare: "0.15" });
  });

  it("checks required inputs, numbers and the safe range", () => {
    const [dataset, steps, share] = askedInputs(finetune);
    expect(inputProblem(dataset!, " ")).toBe("Required");
    expect(inputProblem(steps!, "12.5")).toBe("A whole number");
    expect(inputProblem(steps!, "0")).toBe("At least 1");
    expect(inputProblem(share!, "0.95")).toBe("At most 0.9");
    expect(inputProblem(share!, "x")).toBe("Not a number");
    expect(inputProblem(steps!, "500")).toBeUndefined();
  });

  it("sends typed values, lists split, defaults left out", () => {
    expect(runInputs(finetune, { dataset: "dataset/a, dataset/b\n@main", steps: "500", replayShare: "0.15" })).toEqual({
      dataset: ["dataset/a", "dataset/b", "@main"],
      steps: 500,
    });
  });

  it("reads an estimate and a plan", () => {
    expect(estimateLine(estimate)).toBe("0.93 GPU-hours (0.47–1.40, ±50%, mixed), about 56 min");
    expect(budgetNote(estimate)).toBe("within the 8 GPU-hour daily budget");
    expect(estimateLine({ ...estimate, basis: "none", gpuHours: range(0) })).toBe("No GPU time");
    const plan: AgentPlaybook["plan"] = [
      { id: "mix", title: "Mix", command: "mixes.new", state: "done", spending: false },
      { id: "train", title: "Train", command: "runs.new", state: "running", spending: true },
      { id: "eval", title: "Eval", command: "evals.new", state: "skipped", spending: false },
    ];
    expect(planProgress(plan)).toMatchObject({ done: 1, total: 2, failed: false, current: { id: "train" } });
    const pb: AgentPlaybook = { name: "p", title: "P", state: "stopped", inputs: {}, estimate, plan, stop: { on: "step", when: "failed", message: "oom" } };
    expect(playbookStateLabel(pb)).toBe("stopped (step failed) · 1/2");
    expect(playbookStateLabel({ ...pb, state: "running" })).toBe("1/2 done");
  });
});

let qc: QueryClient;
function wrap(ui: ReactNode) {
  return render(<QueryClientProvider client={qc}>{ui}</QueryClientProvider>);
}

beforeEach(() => {
  qc = new QueryClient({ defaultOptions: { queries: { retry: false, staleTime: Infinity } } });
  qc.setQueryData(playbooksListQueryKey({ query: { project: "demo" } }), { items: [finetune, later] });
  runCommand.mockReset();
});
afterEach(() => cleanup());

describe("New session → from a playbook", () => {
  it("builds the form from the playbook's inputs and shows the estimate before it starts", async () => {
    const started = vi.fn();
    const dry: PlaybookRunResult = {
      playbook: finetune,
      inputs: {},
      prompt: "Run the playbook",
      estimate: { ...estimate, gpuHours: range(0.24) },
      plan: [
        { id: "train", title: "Start the training run", command: "runs.new", state: "pending", spending: true },
        { id: "eval", title: "Eval matrix", command: "evals.new", state: "skipped", spending: false },
      ],
    };
    runCommand.mockResolvedValueOnce(dry).mockResolvedValueOnce({ ...dry, session: { id: "ses_9" } });
    wrap(<PlaybookLauncher project="demo" driver="opencode" onStarted={started} />);

    // The later playbook is listed but cannot be chosen; project facts are shown, not asked.
    const option = screen.getByRole("option", { name: /Adapt a new language \(from phase 4\)/ }) as HTMLOptionElement;
    expect(option.disabled).toBe(true);
    expect(screen.getByText("ver_base")).toBeTruthy();
    expect(screen.getByText(/Estimate \(with defaults\):/).parentElement?.textContent).toContain("0.93 GPU-hours");

    const submit = screen.getByRole("button", { name: "Show the estimate" }) as HTMLButtonElement;
    expect(submit.disabled).toBe(true); // the dataset is required
    fireEvent.change(screen.getByLabelText("dataset *"), { target: { value: "dataset/fleurs-he-smoke" } });
    fireEvent.change(screen.getByLabelText("steps"), { target: { value: "500" } });
    fireEvent.click(submit);
    await waitFor(() => expect(runCommand).toHaveBeenCalledTimes(1));
    expect(runCommand).toHaveBeenCalledWith("playbooks.run", {
      project: "demo",
      name: "finetune-from-dataset",
      version: "2026-09-30.abc",
      body: { inputs: { dataset: ["dataset/fleurs-he-smoke"], steps: 500 }, driver: "opencode" },
      dryRun: true,
      open: false,
    });

    // The dry run's estimate and plan, then the real start.
    expect(await screen.findByText(/^Estimate:/)).toBeTruthy();
    expect(screen.getByLabelText("Plan").textContent).toContain("Start the training run");
    expect(screen.getByLabelText("Plan").textContent).toContain("dry run first");
    fireEvent.click(screen.getByRole("button", { name: "Start playbook" }));
    await waitFor(() => expect(started).toHaveBeenCalled());
    expect(runCommand.mock.calls[1]![1]).toMatchObject({ dryRun: false, open: true });

  });

  it("a changed input drops the estimate; a refusal shows the problem", async () => {
    runCommand.mockResolvedValueOnce({ playbook: finetune, inputs: {}, prompt: "", estimate, plan: [] });
    wrap(<PlaybookLauncher project="demo" initial="finetune-from-dataset" />);
    expect(screen.queryByRole("combobox")).toBeNull();
    fireEvent.change(screen.getByLabelText("dataset *"), { target: { value: "dataset/x" } });
    fireEvent.click(screen.getByRole("button", { name: "Show the estimate" }));
    expect(await screen.findByRole("button", { name: "Start playbook" })).toBeTruthy();
    fireEvent.change(screen.getByLabelText("steps"), { target: { value: "0" } });
    expect(screen.getByRole("button", { name: "Show the estimate" })).toBeTruthy();
    expect(screen.getByText(/^At least 1/)).toBeTruthy();
  });
});
