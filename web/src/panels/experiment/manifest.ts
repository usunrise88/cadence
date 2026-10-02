import { Flask } from "iconoir-react";
import type { PanelManifest } from "@/shell/panel";
import { ExperimentEmpty, ExperimentPanel } from "./ExperimentPanel";

// A document of the Training workspace (docs/spec/11-ui-panels.md "Default workspaces"); opens from links.
const manifest: PanelManifest = {
  id: "experiment",
  kind: "document",
  title: "Experiment",
  icon: Flask,
  singleton: false,
  defaultSize: { w: 900, h: 660 },
  defaultLocation: "centre",
  entity: "experiment",
  help: "panels.experiment",
  commands: ["experiments.new", "sweeps.run", "evals.new", "models.register"],
  empty: ExperimentEmpty,
  component: ExperimentPanel,
};
export default manifest;
