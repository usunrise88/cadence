import { PercentageCircle } from "iconoir-react";
import type { PanelManifest } from "@/shell/panel";
import { MixEmpty, MixPanel } from "./MixPanel";

const manifest: PanelManifest = {
  id: "mix",
  kind: "document",
  title: "Mix",
  icon: PercentageCircle,
  singleton: false,
  defaultSize: { w: 760, h: 560 },
  defaultLocation: "centre",
  entity: "mix",
  help: "panels.mix",
  commands: ["mixes.new", "mixes.edit", "drafts.accept", "drafts.revert"],
  acceptsDrafts: ["mix"],
  empty: MixEmpty,
  component: MixPanel,
};
export default manifest;
