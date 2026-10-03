import { Database } from "iconoir-react";
import type { PanelManifest } from "@/shell/panel";
import { StorageEmpty, StoragePanel } from "./StoragePanel";

// Data and Ops tool (docs/help/panels/storage.md; phase 4 · stream M): mounts, health, cache use, pinned versions,
// quotas.
const manifest: PanelManifest = {
  id: "storage",
  kind: "tool",
  title: "Storage",
  icon: Database,
  singleton: true,
  defaultSize: { w: 720, h: 640 },
  defaultLocation: "centre",
  help: "panels.storage",
  commands: ["mounts.new", "mounts.scan", "mounts.verify", "datasets.evict", "datasets.materialize"],
  empty: StorageEmpty,
  component: StoragePanel,
};
export default manifest;
