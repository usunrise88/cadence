import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { evalsGetQueryKey } from "@/api/gen/@tanstack/react-query.gen";
import type { Eval } from "@/api/gen/types.gen";
import { TooltipProvider } from "@/components/ui/tooltip";
import { PanelContext } from "@/shell/panel/context";
import { useSelection } from "@/shell/selection/store";
import { WORST_N } from "@/shell/evaluation/format";
import { DiffPanel } from "./DiffPanel";

const ev = {
  id: "evl_1",
  goldenSets: [{ versionId: "ver_gs", name: "golden-set/fleurs-he", locale: "he-IL" }],
  subject: { label: "step 200" },
  cells: [
    {
      id: "evc_1",
      role: "subject",
      goldenSetVersionId: "ver_gs",
      profile: "160ms",
      worst: [
        {
          index: 4,
          ref: "שלום עולם גדול",
          hyp: "שלום עולמי מאוד",
          refWords: 3,
          sub: 1,
          del: 1,
          ins: 1,
          errors: 3,
          wer: 1,
          ops: [
            ["=", "שלום", "שלום"],
            ["S", "עולם", "עולמי"],
            ["D", "גדול", ""],
            ["I", "", "מאוד"],
          ],
        },
        { index: 1, ref: "בוקר טוב", hyp: "בוקר", refWords: 2, sub: 0, del: 1, ins: 0, errors: 1, wer: 0.5, ops: [["=", "בוקר", "בוקר"], ["D", "טוב", ""]] },
      ],
    },
  ],
} as unknown as Eval;

let qc: QueryClient;
beforeEach(() => {
  qc = new QueryClient({ defaultOptions: { queries: { retry: false, staleTime: Infinity } } });
  qc.setQueryData(evalsGetQueryKey({ path: { id: "evl_1" }, query: { worst: WORST_N, cell: "evc_1" } }), ev);
  useSelection.setState({ activeDoc: "eval:evl_1", selections: { "eval:evl_1": "cell:evc_1/utt:4" }, pins: {} });
});
afterEach(() => cleanup());

function wrap() {
  return render(
    <QueryClientProvider client={qc}>
      <TooltipProvider>
        <PanelContext.Provider value={{ instanceId: "diff", panelId: "diff", visible: true }}>
          <DiffPanel panelId="diff" instanceId="diff" />
        </PanelContext.Provider>
      </TooltipProvider>
    </QueryClientProvider>,
  );
}

describe("Diff", () => {
  it("aligns reference over hypothesis with S/D/I by glyph, word and colour, bidi-isolated and right to left", () => {
    wrap();
    const list = screen.getByRole("list", { name: "Alignment, reference over hypothesis" });
    expect(list.getAttribute("dir")).toBe("rtl");
    const items = [...list.querySelectorAll("li")];
    expect(items.map((i) => i.getAttribute("data-op"))).toEqual(["=", "S", "D", "I"]);
    expect(items[1]!.getAttribute("aria-label")).toBe("substitution: עולם → עולמי");
    expect(items[2]!.getAttribute("aria-label")).toBe("deletion: גדול");
    expect(items[3]!.getAttribute("aria-label")).toBe("insertion: מאוד");
    // Not colour alone: each error carries its glyph and letter.
    expect(items[1]!.textContent).toContain("≠ S");
    expect(items[2]!.textContent).toContain("− D");
    expect(items[3]!.textContent).toContain("+ I");
    expect(items[0]!.querySelectorAll("bdi")).toHaveLength(2);
    expect(screen.getByLabelText("Utterance numbers").textContent).toContain("WER100.0 %");
  });

  it("steps to the next utterance through the selection bus", () => {
    wrap();
    expect(screen.getByText("1 of 2")).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: /Next utterance/ }));
    expect(useSelection.getState().selections["eval:evl_1"]).toBe("cell:evc_1/utt:1");
  });

  it("shows the texts on request", () => {
    wrap();
    fireEvent.click(screen.getByRole("button", { name: "Show texts" }));
    expect(document.querySelector('[data-slot="diff-texts"]')!.textContent).toContain("שלום עולמי מאוד");
  });

  it("is empty without a selected cell", () => {
    useSelection.setState({ activeDoc: "run:run_1", selections: {}, pins: {} });
    wrap();
    expect(screen.getByText("No utterance selected")).toBeTruthy();
  });
});
