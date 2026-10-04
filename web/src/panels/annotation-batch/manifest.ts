import { Label } from "iconoir-react";
import type { PanelManifest } from "@/shell/panel";
import { AnnotationBatchEmpty, AnnotationBatchPanel } from "./AnnotationBatchPanel";

// A document of the Eval workspace (docs/spec/11-ui-panels.md "Default workspaces", phase 4); opens from links, the
// Triage panel and batches.new.
const manifest: PanelManifest = {
  id: "annotation-batch",
  kind: "document",
  title: "Annotation batch",
  icon: Label,
  singleton: false,
  defaultSize: { w: 900, h: 680 },
  defaultLocation: "centre",
  entity: "annotation_batch",
  help: "panels.annotation-batch",
  commands: ["batches.new", "batches.freeze", "invitations.new", "batchItems.accept"],
  empty: AnnotationBatchEmpty,
  component: AnnotationBatchPanel,
};
export default manifest;
