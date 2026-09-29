import { Database } from "iconoir-react";
import type { PanelManifest } from "@/shell/panel";
import { LibraryEmpty, LibraryPanel } from "./LibraryPanel";

const manifest: PanelManifest = {
  id: "library",
  kind: "tool",
  title: "Library",
  icon: Database,
  singleton: true,
  defaultSize: { w: 300, h: 480 },
  defaultLocation: "left",
  help: "panels.library",
  empty: LibraryEmpty,
  component: LibraryPanel,
};
export default manifest;
