import { Folder } from "iconoir-react";
import type { PanelManifest } from "@/shell/panel";
import { ProjectEmpty, ProjectPanel } from "./ProjectPanel";

const manifest: PanelManifest = {
  id: "project",
  kind: "document",
  title: "Project",
  icon: Folder,
  singleton: false,
  defaultSize: { w: 720, h: 480 },
  defaultLocation: "centre",
  entity: "project",
  help: "panels.project",
  commands: ["projects.edit", "projects.note", "projects.sync", "projects.archive"],
  empty: ProjectEmpty,
  component: ProjectPanel,
};
export default manifest;
