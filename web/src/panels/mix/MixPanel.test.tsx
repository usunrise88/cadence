import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { datasetsListQueryKey, defaultsGetQueryKey } from "@/api/gen/@tanstack/react-query.gen";
import type { Defaults, Mix, MixPreview } from "@/api/gen/types.gen";
import { TooltipProvider } from "@/components/ui/tooltip";
import { PanelContext } from "@/shell/panel/context";
import { MixPanel, previewRows } from "./MixPanel";

const runCommand = vi.fn();
vi.mock("@/shell/panel/commands", async (orig) => ({ ...(await orig<object>()), runCommand: (...a: unknown[]) => runCommand(...a) }));
vi.mock("@/shell/panel/actions", async (orig) => ({ ...(await orig<object>()), useProject: () => "demo" }));

const preview: MixPreview = {
  totalHours: 12,
  basis: "metadata",
  warnings: [],
  languages: [
    { locale: "he-IL", hours: 10, share: 0.85 },
    { locale: "en-US", hours: 2, share: 0.15 },
  ],
  groups: [
    { name: "target", replay: false, weight: 1, hours: 10, share: 0.85, locales: ["he-IL"] },
    { name: "replay", replay: true, weight: 1, hours: 2, share: 0.15, locales: ["en-US"] },
  ],
  datasets: [
    { id: "ver_he", name: "dataset/he-calls", version: "2026-09-30.abc", locales: ["he-IL"], hours: 10, adopted: true },
    { id: "ver_en", name: "dataset/replay-base", version: "2026-09-30.def", locales: ["en-US"], hours: 2, adopted: false },
  ],
};

const mix = {
  id: "mix_1",
  name: "he-first",
  rev: 1,
  groups: [
    { name: "target", weight: 1, replay: false, datasets: ["ver_he"] },
    { name: "replay", weight: 1, replay: true, datasets: ["ver_en"] },
  ],
  temperature: 1,
  replayShare: 0.15,
  preview,
  presence: [],
  createdBy: { kind: "user", id: "usr_admin" },
  updatedBy: { kind: "user", id: "usr_admin" },
  createdAt: "2026-09-30T10:00:00Z",
  updatedAt: "2026-09-30T10:00:00Z",
} as unknown as Mix;

let qc: QueryClient;
beforeEach(() => {
  qc = new QueryClient({ defaultOptions: { queries: { retry: false, staleTime: Infinity } } });
  qc.setQueryData(datasetsListQueryKey({ query: { state: "frozen" } }), { items: [] });
  qc.setQueryData(defaultsGetQueryKey(), { mix: { replay_share: { value: 0.15, unit: "fraction", description: "Replay share", source: "docs/spec/03", range: { min: 0, max: 0.9 } } } } as unknown as Defaults);
  runCommand.mockReset();
});
afterEach(() => cleanup());

function wrap() {
  return render(
    <QueryClientProvider client={qc}>
      <TooltipProvider>
        <PanelContext.Provider value={{ instanceId: "mix:mix:mix_1", panelId: "mix", doc: "mix:mix_1", visible: false }}>
          <MixPanel panelId="mix" instanceId="mix:mix:mix_1" doc="mix:mix_1" tab="overview" entity={{ kind: "mix", id: "mix_1", rev: 1, name: "he-first", mix } as never} />
        </PanelContext.Provider>
      </TooltipProvider>
    </QueryClientProvider>,
  );
}

describe("mix preview", () => {
  it("rows by language, source and group", () => {
    expect(previewRows(preview, "language").map((r) => [r.label, r.hours, r.share])).toEqual([
      ["he-IL", 10, 0.85],
      ["en-US", 2, 0.15],
    ]);
    expect(previewRows(preview, "source").map((r) => [r.label, r.note])).toEqual([
      ["he-calls · 2026-09-30.abc", undefined],
      ["replay-base · 2026-09-30.def", "not adopted"],
    ]);
    expect(previewRows(preview, "group").map((r) => r.note)).toEqual([undefined, "replay"]);
  });

  it("switches the grouping and recomputes the preview for unsaved edits with mixes.preview", async () => {
    runCommand.mockResolvedValue({ ...preview, languages: [{ locale: "he-IL", hours: 10, share: 0.7 }], totalHours: 10 });
    wrap();
    const table = () => document.querySelector('[data-slot="mix-preview"]')!;
    expect(table().textContent).toContain("he-IL");
    fireEvent.click(screen.getByRole("radio", { name: "Source" }));
    expect(table().textContent).toContain("not adopted");
    fireEvent.click(screen.getByRole("radio", { name: "Language" }));

    fireEvent.change(screen.getByLabelText("Replay share slider"), { target: { value: "0.3" } });
    await waitFor(() => expect(runCommand).toHaveBeenCalledWith("mixes.preview", { project: "demo", body: expect.objectContaining({ name: "he-first", replayShare: 0.3 }) }), { timeout: 2000 });
    await waitFor(() => expect(document.querySelector("[data-preview]")!.getAttribute("data-preview")).toBe("unsaved"));
    expect(table().textContent).toContain("70 %");
    expect(screen.getByText(/Departs from the default/)).toBeTruthy();
  });

  it("waits to launch a run until runs arrive", () => {
    wrap();
    const launch = screen.getByRole("button", { name: "Launch a run with this mix" });
    expect(launch).toHaveProperty("disabled", true);
    expect(document.querySelector('[data-slot="launch-run"]')!.getAttribute("aria-label")).toContain("Arrives with runs");
  });
});
