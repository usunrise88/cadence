import { Filter } from "iconoir-react";
import type { PanelManifest } from "@/shell/panel";
import { EvalEmpty, EvalPanel } from "./EvalPanel";

// Centre of the Eval workspace (docs/spec/11-ui-panels.md "Default workspaces"); opens from links, the Library and
// Checkpoints' Evaluate.
const manifest: PanelManifest = {
  id: "eval",
  kind: "document",
  title: "Eval report",
  icon: Filter,
  singleton: false,
  defaultSize: { w: 900, h: 680 },
  defaultLocation: "centre",
  entity: "eval",
  help: "panels.eval",
  commands: ["evals.gate", "models.register", "evals.new", "gates.edit"],
  empty: EvalEmpty,
  component: EvalPanel,
};
export default manifest;
