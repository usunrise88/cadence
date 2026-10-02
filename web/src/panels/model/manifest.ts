import { BookmarkBook } from "iconoir-react";
import type { PanelManifest } from "@/shell/panel";
import { ModelEmpty, ModelPanel } from "./ModelPanel";

// Centre of the Ops workspace (docs/spec/11-ui-panels.md "Default workspaces"); opens from the Library, links and
// a registration.
const manifest: PanelManifest = {
  id: "model",
  kind: "document",
  title: "Model",
  icon: BookmarkBook,
  singleton: false,
  defaultSize: { w: 760, h: 620 },
  defaultLocation: "centre",
  entity: "model",
  help: "panels.model",
  commands: ["aliases.set"],
  empty: ModelEmpty,
  component: ModelPanel,
};
export default manifest;
