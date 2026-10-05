import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, render, screen } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { TooltipProvider } from "@/components/ui/tooltip";
import { useDiffSegment } from "@/shell/diff/segment";
import { PanelContext } from "@/shell/panel/context";
import { useSelection } from "@/shell/selection/store";
import { DiffPanel } from "./DiffPanel";

// The audio view needs a media element and the API; the segment's text alignment is what this test checks.
vi.mock("@/shell/audio", async (orig) => ({ ...(await orig<object>()), AudioView: ({ title }: { title: string }) => <div data-testid="audio">{title}</div> }));

afterEach(() => {
  cleanup();
  useDiffSegment.getState().set(null);
});

describe("Diff of a shadow segment", () => {
  it("aligns the comparison model's transcript over this deployment's", () => {
    useSelection.setState({ activeDoc: null, selections: {}, pins: {} });
    useDiffSegment.getState().set({ audio: "b3:" + "a".repeat(64), ref: "Shalom haolam", hyp: "shalom olam", refLabel: "production", hypLabel: "candidate", label: "c7.wav · 2026-11-02", wer: 0.5 });
    render(
      <QueryClientProvider client={new QueryClient()}>
        <TooltipProvider>
          <PanelContext.Provider value={{ instanceId: "diff", panelId: "diff", visible: true }}>
            <DiffPanel panelId="diff" instanceId="diff" />
          </PanelContext.Provider>
        </TooltipProvider>
      </QueryClientProvider>,
    );
    const items = [...screen.getByRole("list", { name: "Alignment, reference over hypothesis" }).querySelectorAll("li")];
    expect(items.map((i) => i.getAttribute("data-op"))).toEqual(["=", "S"]);
    expect(screen.getByTestId("audio").textContent).toBe("Audio of c7.wav · 2026-11-02");
    expect(screen.getByText(/Upper line: production; lower line: candidate/)).toBeTruthy();
  });
});
