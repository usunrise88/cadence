import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, cleanup, render, screen, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import type { GoldenSetVersion } from "@/api/gen/types.gen";
import { TooltipProvider } from "@/components/ui/tooltip";
import { goldenSetToEntity } from "@/entities/registry-eval";
import { useEditRequests } from "@/shell/entity/edits";
import { PanelContext } from "@/shell/panel/context";
import { GoldenSetPanel } from "./GoldenSetPanel";

const runCommand = vi.fn();
vi.mock("@/shell/panel/commands", async (orig) => ({ ...(await orig<object>()), runCommand: (...a: unknown[]) => runCommand(...a) }));
vi.mock("@/shell/panel/actions", async (orig) => ({ ...(await orig<object>()), useProject: () => "demo" }));

const g = {
  id: "ver_g",
  kind: "golden_set",
  name: "golden-set/he-calls",
  version: "2026-10-01.abc",
  state: "frozen",
  tags: [],
  usedBy: [],
  goldenSet: { datasetVersionId: "ver_d", datasetHash: "b3:x", normalizerVersionId: "ver_n", locale: "he-IL", utterances: 10, hours: 0.1, fingerprint: "f", groups: "call" },
} as unknown as GoldenSetVersion;

let qc: QueryClient;
beforeEach(() => {
  qc = new QueryClient({ defaultOptions: { queries: { retry: false, staleTime: Infinity } } });
  runCommand.mockReset();
});
afterEach(() => cleanup());

function wrap(v: GoldenSetVersion = g) {
  return render(
    <QueryClientProvider client={qc}>
      <TooltipProvider>
        <PanelContext.Provider value={{ instanceId: "golden-set:golden_set:ver_g", panelId: "golden-set", visible: false }}>
          <GoldenSetPanel panelId="golden-set" instanceId="golden-set:golden_set:ver_g" doc="golden_set:ver_g" entity={goldenSetToEntity(v)} />
        </PanelContext.Provider>
      </TooltipProvider>
    </QueryClientProvider>,
  );
}

describe("Golden set: adopt", () => {
  it("dry-runs the adoption and adopts", async () => {
    runCommand.mockResolvedValue({ projectId: "prj_1" });
    wrap();
    act(() => useEditRequests.getState().request("adopt:golden_set:ver_g"));
    expect(await screen.findByText(/no training data of the project overlaps it/)).toBeTruthy();
    expect(runCommand).toHaveBeenCalledWith("projects.adopt", { project: "demo", version: "ver_g", dryRun: true });
    screen.getByRole("button", { name: "Adopt" }).click();
    expect(await screen.findByText(/Adopted\./)).toBeTruthy();
    expect(runCommand).toHaveBeenLastCalledWith("projects.adopt", { project: "demo", version: "ver_g", dryRun: false });
  });

  it("shows the leakage refusal with the overlapping dataset versions", async () => {
    const { ProblemError } = await import("@/api/client");
    runCommand.mockRejectedValue(
      new ProblemError({
        type: "https://cadence.local/help/errors/golden-set-leakage",
        title: "Golden set leakage",
        status: 422,
        detail: "the project trained on 3 of its utterances",
        errors: [{ path: "/overlaps/0", message: "dataset/he-calls 2026-09-30.def holds 3 of its utterances" }],
      }),
    );
    wrap();
    screen.getByRole("button", { name: /Adopt into demo/ }).click();
    await waitFor(() => expect(document.querySelector('[data-slot="adopt-problem"]')?.getAttribute("data-leakage")).toBe("true"));
    expect(screen.getByText(/holds 3 of its utterances/)).toBeTruthy();
    expect((screen.getByRole("button", { name: "Adopt" }) as HTMLButtonElement).disabled).toBe(true);
  });
});

describe("Golden set: word timings", () => {
  it("says emission delay is n/a until the references are aligned", () => {
    wrap();
    expect(document.querySelector('[data-slot="gs-alignment"]')?.textContent).toMatch(/Not aligned: emission delay is n\/a/);
  });

  it("shows the alignment the golden set carries, with why utterances stayed unaligned", () => {
    const aligned = {
      ...g,
      alignment: { id: "aln_1", artifact: "b3:a", aligner: "auxiliary/omniasr-ctc-1b", utterances: 10, aligned: 9, words: 120, reasons: ["longer than 60 s"], createdAt: "2026-10-04T00:00:00Z" },
    } as GoldenSetVersion;
    wrap(aligned);
    const text = document.querySelector('[data-slot="gs-alignment"]')?.textContent ?? "";
    expect(text).toContain("9 of 10 utterances · 120 words");
    expect(text).toContain("auxiliary/omniasr-ctc-1b");
    expect(text).toContain("Unaligned: longer than 60 s");
  });
});
