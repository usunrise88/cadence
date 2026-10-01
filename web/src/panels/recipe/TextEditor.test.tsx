import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import type { Recipe } from "@/api/gen/types.gen";
import { PanelContext } from "@/shell/panel/context";
import { defaultMessage, editable, TextEditor } from "./TextEditor";

const runCommand = vi.fn();
vi.mock("@/shell/panel/commands", async (orig) => ({
  ...(await orig<object>()),
  runCommand: (...a: unknown[]) => runCommand(...a),
  problemOf: (e: unknown) => (e as { problem?: unknown } | undefined)?.problem,
}));

const content = "name: train-stage\nsteps: []\n";
const recipe: Recipe = {
  path: "pipelines/train-stage.yaml",
  ref: "main",
  commit: "fff0000",
  bytes: content.length,
  encoding: "utf-8",
  content,
  history: [{ sha: "abc1234def", message: "add", author: "admin", at: "2026-10-01T10:00:00Z" }],
};

function show(onDone = vi.fn()) {
  render(
    <QueryClientProvider client={new QueryClient()}>
      <PanelContext.Provider value={{ instanceId: "recipe", panelId: "recipe", visible: false }}>
        <TextEditor project="demo" recipe={recipe} onDone={onDone} />
      </PanelContext.Provider>
    </QueryClientProvider>,
  );
  return onDone;
}

const failure = (message: string, problem: object) => Object.assign(new Error(message), { problem, status: (problem as { status: number }).status });

describe("Recipe text editor", () => {
  beforeEach(() => {
    runCommand.mockReset();
  });
  afterEach(cleanup);

  it("offers no editor for the agent profile's files", () => {
    expect(editable("pipelines/train-stage.yaml")).toBe(true);
    expect(editable("AGENTS.md")).toBe(false);
    expect(editable(".claude/settings.json")).toBe(false);
    expect(defaultMessage("pipelines/x.yaml")).toBe("edit pipelines/x.yaml");
  });

  it("commits on the file's last commit and closes", async () => {
    runCommand.mockResolvedValue({ ...recipe, history: [{ ...recipe.history[0]!, sha: "beef000" }, ...recipe.history] });
    const onDone = show();
    fireEvent.change(screen.getByTestId("recipe-editor-text"), { target: { value: content + "# more\n" } });
    fireEvent.change(screen.getByLabelText("Commit message"), { target: { value: "train longer" } });
    fireEvent.click(screen.getByRole("button", { name: "Commit to main" }));
    await waitFor(() => expect(onDone).toHaveBeenCalled());
    expect(runCommand).toHaveBeenCalledWith("recipes.edit", {
      project: "demo",
      path: recipe.path,
      expect: "abc1234def",
      dryRun: false,
      body: { content: content + "# more\n", message: "train longer" },
    });
  });

  it("lists a pipeline's problems and commits nothing", async () => {
    runCommand.mockRejectedValue(
      failure("invalid", { type: "x", status: 422, title: "Pipeline invalid", detail: "1 problem", errors: [{ path: "steps.train.params.nope", message: "no parameter nope" }] }),
    );
    const onDone = show();
    fireEvent.change(screen.getByTestId("recipe-editor-text"), { target: { value: content + "x: 1\n" } });
    fireEvent.click(screen.getByRole("button", { name: "Check" }));
    expect(await screen.findByText("no parameter nope")).toBeTruthy();
    expect(screen.getByText("steps.train.params.nope")).toBeTruthy();
    expect(runCommand.mock.calls[0]?.[1]).toMatchObject({ dryRun: true });
    expect(onDone).not.toHaveBeenCalled();
  });

  it("says when main moved meanwhile", async () => {
    runCommand.mockRejectedValue(failure("moved", { type: "x", status: 412, title: "Precondition failed" }));
    show();
    fireEvent.change(screen.getByTestId("recipe-editor-text"), { target: { value: "changed\n" } });
    fireEvent.click(screen.getByRole("button", { name: "Commit to main" }));
    expect((await screen.findByRole("alert")).textContent).toContain("changed on main while you edited it");
    expect((screen.getByRole("button", { name: "Commit to main" }) as HTMLButtonElement).disabled).toBe(true);
  });
});
