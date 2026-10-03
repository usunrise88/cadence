import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { mountsListQueryKey, storageGetQueryKey } from "@/api/gen/@tanstack/react-query.gen";
import type { Mount, StorageDataset, StorageUse } from "@/api/gen/types.gen";
import { TooltipProvider } from "@/components/ui/tooltip";
import { PanelContext } from "@/shell/panel/context";
import { StoragePanel } from "./StoragePanel";
import { cacheLevel, datasetAction, formatBytes, healthLine, mountBody, EMPTY_FORM } from "./model";

const runCommand = vi.fn();
vi.mock("@/shell/panel/commands", async (orig) => ({ ...(await orig<object>()), runCommand: (...a: unknown[]) => runCommand(...a) }));

const mount: Mount = {
  id: "mnt_1",
  name: "corpora",
  kind: "local",
  root: "/mnt/corpora",
  readOnly: true,
  licenceHint: "",
  description: "",
  health: { state: "healthy", freeBytes: 812e9, totalBytes: 1.2e12, throughputMBps: 410, host: "staging" },
  inventory: { scannedAt: "2026-10-03T10:00:00Z", files: 3412, bytes: 81.2e9, entries: [], blobs: 2, blobBytes: 10 },
  utterances: 12,
  copies: 2,
  copyBytes: 10,
  rev: 1,
  createdBy: { kind: "user", id: "usr_admin" },
  createdAt: "",
  updatedAt: "",
};

const ds = (over: Partial<StorageDataset>): StorageDataset => ({
  versionId: "ver_1",
  name: "dataset/fleurs-sr",
  version: "2026-10-03.abcdefabcdef",
  artifact: "b3:" + "a".repeat(64),
  bytes: 2e9,
  state: "cached",
  pinned: [],
  copies: 4,
  shards: 4,
  evictable: true,
  ...over,
});

const use: StorageUse = {
  totalBytes: 1e12,
  freeBytes: 1e11,
  usedPct: 90,
  highWaterPct: 85,
  lowWaterPct: 70,
  artifactBytes: 5e11,
  datasetBytes: 2e9,
  evictableBytes: 2e9,
  projects: [{ projectId: "prj_1", slug: "sr", datasetBytes: 2e9, quotaBytes: 200e9, over: false }],
  datasets: [ds({}), ds({ versionId: "ver_2", name: "dataset/replay", state: "evicted" }), ds({ versionId: "ver_3", name: "dataset/pinned", pinned: ["job job_1 is queued or running on it"] })],
};

describe("storage model", () => {
  it("formats bytes, health and the cache level", () => {
    expect(formatBytes(1_500_000)).toBe("1.5 MB");
    expect(formatBytes(999)).toBe("999 B");
    expect(healthLine(mount)).toBe("free 812.0 GB of 1.2 TB · 410 MB/s · from staging");
    expect(healthLine({ ...mount, health: { state: "unhealthy", detail: "not a directory" } })).toBe("not a directory");
    expect(healthLine({ ...mount, health: { state: "unknown" } })).toBe("not checked yet");
    expect(cacheLevel(use)).toBe("over");
    expect(cacheLevel({ ...use, usedPct: 75 })).toBe("between");
  });

  it("offers evict or materialise, with the reason when it cannot", () => {
    expect(datasetAction(ds({}))).toEqual({ command: "datasets.evict", label: "Evict", enabled: true });
    expect(datasetAction(ds({ state: "evicted" })).command).toBe("datasets.materialize");
    expect(datasetAction(ds({ evictable: false, copies: 1 })).enabled).toBe("3 of 4 shards exist on no mount");
    expect(datasetAction(ds({ pinned: ["golden set ver_9 is built on it"] })).enabled).toContain("golden set");
  });

  it("sends only the fields a kind takes", () => {
    expect(mountBody({ ...EMPTY_FORM, name: " corpora ", root: "/mnt/corpora", endpoint: "x" })).toEqual({ name: "corpora", kind: "local", root: "/mnt/corpora", readOnly: true });
    expect(mountBody({ ...EMPTY_FORM, name: "b", kind: "s3", root: "bucket/p", endpoint: "http://minio:9000", credentials: "s3", readOnly: false })).toEqual({
      name: "b",
      kind: "s3",
      root: "bucket/p",
      endpoint: "http://minio:9000",
      credentials: "s3",
      readOnly: false,
    });
    expect(mountBody({ ...EMPTY_FORM, name: "h", kind: "hf", root: "datasets/o/n", revision: "a".repeat(40), readOnly: false }).readOnly).toBe(true);
  });
});

describe("StoragePanel", () => {
  let qc: QueryClient;
  beforeEach(() => {
    qc = new QueryClient({ defaultOptions: { queries: { retry: false, staleTime: Infinity } } });
    qc.setQueryData(mountsListQueryKey(), { items: [mount] });
    qc.setQueryData(storageGetQueryKey(), use);
    runCommand.mockReset();
  });
  afterEach(cleanup);

  const show = () =>
    render(
      <QueryClientProvider client={qc}>
        <TooltipProvider>
          <PanelContext.Provider value={{ instanceId: "storage", panelId: "storage", visible: false }}>
            <StoragePanel panelId="storage" instanceId="storage" />
          </PanelContext.Provider>
        </TooltipProvider>
      </QueryClientProvider>,
    );

  it("lists mounts with health and runs scan, evict and materialise as commands", async () => {
    show();
    const row = screen.getByRole("table", { name: "Mounts" }).querySelector('[data-mount="corpora"]') as HTMLElement;
    expect(within(row).getByText("healthy")).toBeTruthy();
    expect(within(row).getByText(/3,412 files/)).toBeTruthy();
    runCommand.mockResolvedValue({ jobId: "job_scan" });
    fireEvent.click(within(row).getByText("Rescan"));
    await waitFor(() => expect(runCommand).toHaveBeenCalledWith("mounts.scan", { mount }));
    expect(await screen.findByText(/job job_scan queued/)).toBeTruthy();

    const table = screen.getByRole("table", { name: "Dataset versions" });
    const pinned = table.querySelector('[data-dataset="ver_3"]') as HTMLElement;
    expect((within(pinned).getByText("Evict") as HTMLButtonElement).disabled).toBe(true);
    fireEvent.click(within(table.querySelector('[data-dataset="ver_2"]') as HTMLElement).getByText("Materialize"));
    await waitFor(() => expect(runCommand).toHaveBeenCalledWith("datasets.materialize", { versionId: "ver_2" }));
    fireEvent.click(within(table.querySelector('[data-dataset="ver_1"]') as HTMLElement).getByText("Evict"));
    await waitFor(() => expect(runCommand).toHaveBeenCalledWith("datasets.evict", { versionId: "ver_1" }));
    expect(screen.getByTestId("cache-use").textContent).toContain("90 % used");
  });

  it("asks for a mount through an approval", async () => {
    show();
    runCommand.mockResolvedValue({ approvalId: "apr_1" });
    fireEvent.click(screen.getByText("Add mount…"));
    const form = screen.getByRole("form", { name: "Add mount" });
    fireEvent.change(within(form).getByPlaceholderText("corpora"), { target: { value: "exports" } });
    fireEvent.change(within(form).getByPlaceholderText(/absolute path/), { target: { value: "/mnt/exports" } });
    fireEvent.click(within(form).getByText("Ask for approval"));
    await waitFor(() =>
      expect(runCommand).toHaveBeenCalledWith("mounts.new", { body: { name: "exports", kind: "local", root: "/mnt/exports", readOnly: true } }),
    );
    expect(await screen.findByText(/waiting for approval apr_1/)).toBeTruthy();
  });
});
