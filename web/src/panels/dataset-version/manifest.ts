import { Database } from "iconoir-react";
import type { PanelManifest } from "@/shell/panel";
import { DatasetVersionEmpty, DatasetVersionPanel } from "./DatasetVersionPanel";

// Centre of the Data workspace (docs/spec/11-ui-panels.md "Default workspaces"); opens from the Library, a Source's
// ingest history, a Mix and links.
const manifest: PanelManifest = {
  id: "dataset-version",
  kind: "document",
  title: "Dataset version",
  icon: Database,
  singleton: false,
  defaultSize: { w: 820, h: 640 },
  defaultLocation: "centre",
  entity: "dataset_version",
  help: "panels.dataset-version",
  commands: ["datasets.freeze", "datasets.preview", "datasets.export", "projects.adopt", "versions.archive"],
  empty: DatasetVersionEmpty,
  component: DatasetVersionPanel,
};
export default manifest;
