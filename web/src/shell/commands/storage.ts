import { Download, Plus, ScanBarcode, ShieldCheck, Upload } from "iconoir-react";
import { commandHeaders } from "@/api/client";
import { datasetsEvict, datasetsMaterialize, mountsNew, mountsScan, mountsVerify } from "@/api/gen/sdk.gen";
import type { ApprovalAccepted, DatasetCachePlan, JobAccepted, Mount, MountNew } from "@/api/gen/types.gen";
import { commands } from "@/shell/registries";
import type { Command } from "./registry";

// The Storage panel's commands (phase 4 · stream M): one API operation each. mounts.new always answers an approval
// (the admin decides, for people too); scans and health checks queue jobs; datasets.evict and datasets.materialize
// move a dataset version's shards out of the local cache and back. Hidden from the palette: they need the panel's
// selection (a mount, a dataset version).

export type MountArgs = { mount: Mount; path?: string; dryRun?: boolean };
export type DatasetCacheArgs = { versionId: string; dryRun?: boolean };

export type StorageCommands = {
  "mounts.new": { args: { body: MountNew; dryRun?: boolean }; result: Mount | ApprovalAccepted };
  "mounts.scan": { args: MountArgs; result: Mount | JobAccepted };
  "mounts.verify": { args: MountArgs; result: Mount | JobAccepted };
  "datasets.evict": { args: DatasetCacheArgs; result: DatasetCachePlan | JobAccepted };
  "datasets.materialize": { args: DatasetCacheArgs; result: DatasetCachePlan | JobAccepted };
};

function need<T>(args: unknown, what: string): T {
  if (!args) throw new Error(`${what}: run it from the Storage panel`);
  return args as T;
}

const dry = (d?: boolean) => (d ? { dryRun: true } : undefined);

export function registerStorageCommands(): void {
  const list: Command[] = [
    {
      id: "mounts.new",
      operation: "mounts.new",
      title: "Add mount",
      group: "Edit",
      icon: Plus,
      hidden: true,
      run: async (_ctx, args) => {
        const a = need<StorageCommands["mounts.new"]["args"]>(args, "Add mount");
        const { data } = await mountsNew({ body: a.body, query: dry(a.dryRun), headers: commandHeaders(), throwOnError: true });
        return data as Mount | ApprovalAccepted;
      },
    },
    {
      id: "mounts.scan",
      operation: "mounts.scan",
      title: "Rescan mount",
      group: "Edit",
      icon: ScanBarcode,
      hidden: true,
      run: async (_ctx, args) => {
        const a = need<MountArgs>(args, "Rescan mount");
        const { data } = await mountsScan({
          path: { id: a.mount.id },
          body: a.path ? { path: a.path } : {},
          query: dry(a.dryRun),
          headers: commandHeaders(a.mount.rev),
          throwOnError: true,
        });
        return data as Mount | JobAccepted;
      },
    },
    {
      id: "mounts.verify",
      operation: "mounts.verify",
      title: "Check mount health",
      group: "Edit",
      icon: ShieldCheck,
      hidden: true,
      run: async (_ctx, args) => {
        const a = need<MountArgs>(args, "Check mount health");
        const { data } = await mountsVerify({ path: { id: a.mount.id }, query: dry(a.dryRun), headers: commandHeaders(a.mount.rev), throwOnError: true });
        return data as Mount | JobAccepted;
      },
    },
    {
      id: "datasets.evict",
      operation: "datasets.evict",
      title: "Evict dataset from the cache",
      group: "Edit",
      icon: Upload,
      hidden: true,
      run: async (_ctx, args) => {
        const a = need<DatasetCacheArgs>(args, "Evict dataset");
        const { data } = await datasetsEvict({ body: { versionId: a.versionId }, query: dry(a.dryRun), headers: commandHeaders(), throwOnError: true });
        return data as DatasetCachePlan | JobAccepted;
      },
    },
    {
      id: "datasets.materialize",
      operation: "datasets.materialize",
      title: "Materialize dataset into the cache",
      group: "Edit",
      icon: Download,
      hidden: true,
      run: async (_ctx, args) => {
        const a = need<DatasetCacheArgs>(args, "Materialize dataset");
        const { data } = await datasetsMaterialize({ body: { versionId: a.versionId }, query: dry(a.dryRun), headers: commandHeaders(), throwOnError: true });
        return data as DatasetCachePlan | JobAccepted;
      },
    },
  ];
  for (const c of list) commands.register(c);
}
