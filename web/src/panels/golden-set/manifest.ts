import { Medal } from "iconoir-react";
import type { PanelManifest } from "@/shell/panel";
import { GoldenSetEmpty, GoldenSetPanel } from "./GoldenSetPanel";

// Centre of the Eval workspace (docs/spec/11-ui-panels.md "Default workspaces"); opens from the Library and links.
const manifest: PanelManifest = {
  id: "golden-set",
  kind: "document",
  title: "Golden set",
  icon: Medal,
  singleton: false,
  defaultSize: { w: 760, h: 600 },
  defaultLocation: "centre",
  entity: "golden_set",
  help: "panels.golden-set",
  commands: ["projects.adopt", "goldenSets.freeze"],
  empty: GoldenSetEmpty,
  component: GoldenSetPanel,
};
export default manifest;
