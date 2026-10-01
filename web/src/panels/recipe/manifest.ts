import { Page } from "iconoir-react";
import type { PanelManifest } from "@/shell/panel";
import { RecipeEmpty, RecipePanel } from "./RecipePanel";

const manifest: PanelManifest = {
  id: "recipe",
  kind: "document",
  title: "Recipe",
  icon: Page,
  singleton: false,
  defaultSize: { w: 820, h: 560 },
  defaultLocation: "centre",
  entity: "recipe",
  help: "panels.recipe",
  commands: ["projects.sync", "branches.accept", "branches.revert", "recipes.new", "recipes.edit"],
  empty: RecipeEmpty,
  component: RecipePanel,
};
export default manifest;
