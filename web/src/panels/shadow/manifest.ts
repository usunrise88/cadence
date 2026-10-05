import { GitCompare } from "iconoir-react";
import type { PanelManifest } from "@/shell/panel";
import { ShadowEmpty, ShadowPanel } from "./ShadowPanel";

// Ops workspace, bottom (docs/spec/11-ui-panels.md "Default workspaces"): a shadow deployment's nightly replays.
const manifest: PanelManifest = {
  id: "shadow",
  kind: "tool",
  title: "Shadow",
  icon: GitCompare,
  singleton: true,
  defaultSize: { w: 760, h: 420 },
  defaultLocation: "bottom",
  help: "panels.shadow",
  commands: ["shadowReplays.new"],
  empty: ShadowEmpty,
  component: ShadowPanel,
};
export default manifest;
