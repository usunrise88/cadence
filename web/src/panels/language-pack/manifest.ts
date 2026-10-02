import { Language } from "iconoir-react";
import type { PanelManifest } from "@/shell/panel";
import { LanguagePackEmpty, LanguagePackPanel } from "./LanguagePackPanel";

// Centre of the Data workspace (docs/spec/11-ui-panels.md "Default workspaces"); opens from the Library's project work.
const manifest: PanelManifest = {
  id: "language-pack",
  kind: "document",
  title: "Language pack",
  icon: Language,
  singleton: false,
  defaultSize: { w: 820, h: 640 },
  defaultLocation: "centre",
  entity: "language_pack",
  help: "panels.language-pack",
  commands: ["langpacks.edit", "boost.edit"],
  empty: LanguagePackEmpty,
  component: LanguagePackPanel,
};
export default manifest;
