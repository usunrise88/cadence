// The panel SDK: the only shell surface panels import (besides the entity primitives and components/ui).
export { usePanel, useTopic } from "./context";
export { useFollowedDoc, useCurrentSelection, useSelection } from "@/shell/selection/store";
export type { PanelManifest, PanelProps } from "@/shell/registry/panels";
export type { DocTab } from "@/shell/entity/primitives";
export { openDocument, openPanelById, runCommand, useProject } from "./actions";
export { openInLibrary, openRef, previewRef } from "@/shell/search/actions";
export { chipLabel, kindLabel, kindNoun, scopeOf, withScope } from "@/shell/search/hits";
export { useSearch } from "@/shell/search/store";
