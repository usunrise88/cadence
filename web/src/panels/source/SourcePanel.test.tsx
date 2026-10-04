import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, cleanup, render, screen } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import type { Source } from "@/api/gen/types.gen";
import { TooltipProvider } from "@/components/ui/tooltip";
import { sourceToEntity } from "@/entities/data";
import { useEditRequests } from "@/shell/entity/edits";
import { PanelContext } from "@/shell/panel/context";
import type { DocTab } from "@/shell/panel";
import { SourcePanel } from "./SourcePanel";

const runCommand = vi.fn();
const openDocument = vi.fn();
vi.mock("@/shell/panel/commands", async (orig) => ({ ...(await orig<object>()), runCommand: (...a: unknown[]) => runCommand(...a) }));
vi.mock("@/shell/panel/actions", async (orig) => ({ ...(await orig<object>()), useProject: () => "demo", openDocument: (...a: unknown[]) => openDocument(...a) }));
vi.mock("@/shell/data/UtteranceSearch", () => ({ UtteranceSearch: ({ source }: { source?: string }) => <div data-utterances={source} />, searchQuery: () => ({}) }));

const admin = { kind: "user", id: "usr_admin", name: "admin" };
const src = {
  id: "src_1",
  name: "parlaspeech-sr",
  description: "Serbian parliament",
  licence: "CC-BY-SA-4.0",
  kind: "public",
  languages: ["sr-RS"],
  url: "mount://corpora/parlaspeech-sr",
  trainingCleared: false,
  archived: false,
  rev: 2,
  utterances: 900,
  hours: 9,
  datasets: ["ver_d"],
  clearances: [
    { change: "created", licence: "unknown", trainingCleared: false, actor: admin, at: "2026-10-01T10:00:00Z" },
    { change: "licence", licence: "CC-BY-SA-4.0", trainingCleared: false, actor: admin, at: "2026-10-02T10:00:00Z" },
  ],
  ingests: [{ datasetVersionId: "ver_d", stepKind: "dataset_freeze@1", frozen: false, utterances: 900, hours: 9, at: "2026-10-03T10:00:00Z" }],
  createdBy: admin,
  createdAt: "2026-10-01T10:00:00Z",
  updatedAt: "2026-10-02T10:00:00Z",
} as unknown as Source;

let qc: QueryClient;
beforeEach(() => {
  qc = new QueryClient({ defaultOptions: { queries: { retry: false, staleTime: Infinity } } });
  runCommand.mockReset();
  openDocument.mockReset();
});
afterEach(() => cleanup());

function wrap(s: Source, tab?: DocTab) {
  return render(
    <QueryClientProvider client={qc}>
      <TooltipProvider>
        <PanelContext.Provider value={{ instanceId: "source:source:src_1", panelId: "source", visible: false }}>
          <SourcePanel panelId="source" instanceId="source:source:src_1" doc="source:src_1" tab={tab} entity={sourceToEntity(s)} />
        </PanelContext.Provider>
      </TooltipProvider>
    </QueryClientProvider>,
  );
}

describe("Source", () => {
  it("lists the ingests and opens a dataset version", () => {
    wrap(src);
    expect(screen.getByText(/eval-only: its dataset versions are evaluated on/)).toBeTruthy();
    expect(screen.getByText("dataset_freeze@1")).toBeTruthy();
    screen.getByRole("button", { name: "ver_d" }).click();
    expect(openDocument).toHaveBeenCalledWith("dataset_version:ver_d");
    expect(document.querySelector("[data-utterances]")?.getAttribute("data-utterances")).toBe("src_1");
  });

  it("clears it for training through sources.edit; an agent's call waits for an approval", async () => {
    runCommand.mockResolvedValue({ approvalId: "apr_1" });
    wrap(src);
    act(() => useEditRequests.getState().request("clear:source:src_1"));
    (await screen.findByRole("button", { name: "Clear for training" })).click();
    expect(await screen.findByText(/Waits for an approval \(apr_1\)/)).toBeTruthy();
    expect(runCommand).toHaveBeenCalledWith("sources.edit", { source: src, body: { trainingCleared: true } });
  });

  it("shows the clearing history on the Activity tab", () => {
    wrap(src, "activity");
    expect(screen.getByText("registered")).toBeTruthy();
    expect(screen.getByText("licence changed")).toBeTruthy();
  });
});
