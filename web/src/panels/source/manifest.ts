import { DatabaseScript } from "iconoir-react";
import type { PanelManifest } from "@/shell/panel";
import { SourceEmpty, SourcePanel } from "./SourcePanel";

// Centre of the Data workspace (docs/spec/11-ui-panels.md "Default workspaces"); opens from the Library and a dataset
// version's sources.
const manifest: PanelManifest = {
  id: "source",
  kind: "document",
  title: "Source",
  icon: DatabaseScript,
  singleton: false,
  defaultSize: { w: 760, h: 600 },
  defaultLocation: "centre",
  entity: "source",
  help: "panels.source",
  commands: ["sources.edit", "sources.archive"],
  empty: SourceEmpty,
  component: SourcePanel,
};
export default manifest;
