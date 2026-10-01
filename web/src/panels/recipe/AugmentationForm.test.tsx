import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { defaultsGetQueryKey } from "@/api/gen/@tanstack/react-query.gen";
import type { Defaults, Recipe } from "@/api/gen/types.gen";
import { TooltipProvider } from "@/components/ui/tooltip";
import { PanelContext } from "@/shell/panel/context";
import { AugmentationForm, NewProfileButton } from "./AugmentationForm";

const runCommand = vi.fn();
const openDocument = vi.fn();
// problemOf reads the problem a failed command carries (a ProblemError in the app).
vi.mock("@/shell/panel/commands", async (orig) => ({
  ...(await orig<object>()),
  runCommand: (...a: unknown[]) => runCommand(...a),
  problemOf: (e: unknown) => (e as { problem?: unknown } | undefined)?.problem,
}));
vi.mock("@/shell/panel/actions", async (orig) => ({ ...(await orig<object>()), openDocument: (d: string) => openDocument(d) }));

const d = (value: unknown, range: object, unit?: string) => ({ value, description: "d", source: "s", range, ...(unit ? { unit } : {}) });
const defaults = {
  augment: {
    seed: d(1234, { min: 0, max: 2147483647 }),
    codec_probability: d(0.5, { min: 0, max: 1 }),
    codecs: d(["g711-ulaw", "g711-alaw", "gsm-fr", "amr-nb", "opus"], { values: ["g711-ulaw", "g711-alaw", "gsm-fr", "amr-nb", "opus"] }),
    band_limit_probability: d(0.5, { min: 0, max: 1 }),
    band_limit_hz: d(3400, { min: 3000, max: 8000 }, "Hz"),
    level_probability: d(0.3, { min: 0, max: 1 }),
    level_gain_db: d([-10, 6], { min: -30, max: 20 }, "dB"),
    speed_probability: d(0.3, { min: 0, max: 1 }),
    speed_factor: d([0.9, 1.1], { min: 0.8, max: 1.2 }),
  },
} as unknown as Defaults;

const content = "# calls from the Haifa office\nname: telephony\nseed: 7\ntransforms:\n  codec:\n    probability: 0.2\n  speed:\n    factor: [0.95, 1.05]\n";
const recipe: Recipe = {
  path: "augment/telephony.yaml",
  ref: "main",
  commit: "fff0000",
  bytes: content.length,
  encoding: "utf-8",
  content,
  history: [{ sha: "abc1234def", message: "augment: add", author: "admin", at: "2026-09-30T10:00:00Z" }],
};

let qc: QueryClient;
beforeEach(() => {
  qc = new QueryClient({ defaultOptions: { queries: { retry: false, staleTime: Infinity } } });
  qc.setQueryData(defaultsGetQueryKey(), defaults);
  runCommand.mockReset();
  openDocument.mockReset();
});
afterEach(() => cleanup());

function wrap(ui: React.ReactNode) {
  return render(
    <QueryClientProvider client={qc}>
      <TooltipProvider>
        <PanelContext.Provider value={{ instanceId: "recipe:x", panelId: "recipe", visible: false }}>{ui}</PanelContext.Provider>
      </TooltipProvider>
    </QueryClientProvider>,
  );
}

describe("augmentation profile form", () => {
  it("renders the transforms with departures from the recommended values", () => {
    const { container } = wrap(<AugmentationForm project="demo" recipe={recipe} />);
    expect(screen.getByText("3 departures from recommended")).toBeTruthy();
    for (const f of ["seed", "transforms.codec.probability", "transforms.speed.factor"]) expect(container.querySelector(`[data-field="${f}"]`)!.getAttribute("data-departs")).toBe("true");
    expect(container.querySelector('[data-field="transforms.band_limit.cutoff_hz"]')!.getAttribute("data-departs")).toBeNull();
    expect((screen.getByLabelText("Cut-off") as HTMLInputElement).value).toBe("3400");
    expect(screen.getByRole("button", { name: "Save" })).toHaveProperty("disabled", true);
  });

  it("warns outside the safe range, resets to recommended and commits with the file's last commit", async () => {
    runCommand.mockResolvedValue({ ...recipe, history: [{ sha: "0123456789", message: "m", author: "a", at: "" }, ...recipe.history] });
    wrap(<AugmentationForm project="demo" recipe={recipe} />);
    fireEvent.change(screen.getByLabelText("Cut-off"), { target: { value: "9000" } });
    expect(screen.getByText(/Above the safe range/)).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: "Reset to recommended" }));
    expect(screen.getByText("at recommended values")).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(runCommand).toHaveBeenCalled());
    const [id, args] = runCommand.mock.calls[0]! as [string, { project: string; path: string; expect: string; body: { content: string } }];
    expect(id).toBe("recipes.edit");
    expect(args).toMatchObject({ project: "demo", path: "augment/telephony.yaml", expect: "abc1234def" });
    expect(args.body.content).toContain("# calls from the Haifa office");
    expect(args.body.content).toContain("seed: 1234");
    expect(args.body.content).toContain("cutoff_hz: 3400");
    expect(args.body.content).toContain("factor: [ 0.9, 1.1 ]");
    expect(await screen.findByText(/Committed 0123456 to main/)).toBeTruthy();
  });

  it("offers Reload when the file moved on", async () => {
    runCommand.mockRejectedValue(Object.assign(new Error("moved"), { problem: { type: "x", title: "Precondition failed", status: 412 }, status: 412 }));
    wrap(<AugmentationForm project="demo" recipe={recipe} />);
    fireEvent.change(screen.getByLabelText("Seed"), { target: { value: "8" } });
    fireEvent.click(screen.getByRole("button", { name: "Save" }));
    expect((await screen.findByRole("alert")).textContent).toContain("changed on main");
    fireEvent.click(screen.getByRole("button", { name: "Reload" }));
    expect(screen.queryByRole("alert")).toBeNull();
    expect((screen.getByLabelText("Seed") as HTMLInputElement).value).toBe("7");
  });

  it("does not render a form for a file that does not parse", () => {
    wrap(<AugmentationForm project="demo" recipe={{ ...recipe, content: "a: [" }} />);
    expect(screen.getByRole("alert").textContent).toMatch(/does not parse/);
  });

  it("creates a new profile at the recommended values and opens it", async () => {
    runCommand.mockResolvedValue(recipe);
    wrap(<NewProfileButton project="demo" existing={["augment/telephony.yaml"]} />);
    fireEvent.click(screen.getByRole("button", { name: "New augmentation profile" }));
    await waitFor(() => expect(openDocument).toHaveBeenCalledWith("recipe:augment/telephony-2.yaml"));
    const [id, args] = runCommand.mock.calls[0]! as [string, { body: { path: string; content: string } }];
    expect(id).toBe("recipes.new");
    expect(args.body.path).toBe("augment/telephony-2.yaml");
    expect(args.body.content).toContain("name: telephony-2");
    expect(args.body.content).toContain("probability: 0.5");
  });
});
