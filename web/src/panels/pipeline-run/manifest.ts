import { List } from "iconoir-react";
import type { PanelManifest } from "@/shell/panel";
import { PipelineRunEmpty, PipelineRunPanel } from "./PipelineRunPanel";

// Right column of the Data workspace (docs/spec/11-ui-panels.md "Default workspaces").
const manifest: PanelManifest = {
  id: "pipeline-run",
  kind: "tool",
  title: "Pipeline run",
  icon: List,
  singleton: true,
  defaultSize: { w: 560, h: 640 },
  defaultLocation: "right",
  help: "panels.pipeline-run",
  commands: ["pipelineRuns.retry", "pipelineRuns.cancel"],
  empty: PipelineRunEmpty,
  component: PipelineRunPanel,
};
export default manifest;
