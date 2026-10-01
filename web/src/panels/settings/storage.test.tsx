import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import type { EvictionPlan } from "@/api/gen/types.gen";
import { PanelContext } from "@/shell/panel/context";
import { freeShare, StorageSection } from "./StorageSection";

const runCommand = vi.fn();
vi.mock("@/shell/panel/commands", async (orig) => ({ ...(await orig<object>()), runCommand: (...a: unknown[]) => runCommand(...a) }));

const plan: EvictionPlan = {
  artifacts: [
    { hash: "b3:aa", type: "training-state", size: 7_300_000_000, runId: "run_1", reason: "its run finished", createdAt: "2026-10-01T12:00:00Z" },
    { hash: "b3:bb", type: "training-state", size: 7_300_000_000, runId: "run_2", reason: "its run finished", createdAt: "2026-10-01T13:00:00Z" },
  ],
  kept: [{ hash: "b3:cc", reason: "a waiting step job resumes from it" }],
  bytesFreed: 14_600_000_000,
  blobs: 4,
  permanent: true,
  disk: { totalBytes: 492_000_000_000, freeBytes: 36_000_000_000, lowFreeFraction: 0.15 },
};

describe("Settings → Content store", () => {
  beforeEach(() => {
    runCommand.mockReset();
  });
  afterEach(cleanup);

  it("computes the free share", () => {
    expect(freeShare(plan)).toBeCloseTo(36 / 492);
    expect(freeShare({ ...plan, disk: undefined })).toBeUndefined();
  });

  it("shows the disk and the dry run, and evicts through an approval", async () => {
    runCommand.mockImplementation((_id: string, args: { dryRun?: boolean }) => Promise.resolve(args.dryRun ? plan : { approvalId: "apr_1" }));
    render(
      <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
        <PanelContext.Provider value={{ instanceId: "settings", panelId: "settings", visible: false }}>
          <StorageSection />
        </PanelContext.Provider>
      </QueryClientProvider>,
    );
    expect((await screen.findByTestId("store-disk")).textContent).toContain("below 15 %");
    expect(screen.getByText(/2 training states can be evicted/)).toBeTruthy();
    expect(screen.getByText(/cannot be undone/)).toBeTruthy();
    expect(runCommand).toHaveBeenCalledWith("artifacts.evict", { dryRun: true });
    fireEvent.click(screen.getByRole("button", { name: /Evict 2/ }));
    await waitFor(() => expect(runCommand).toHaveBeenCalledWith("artifacts.evict", {}));
    expect(await screen.findByText(/Waiting for approval apr_1/)).toBeTruthy();
  });
});
