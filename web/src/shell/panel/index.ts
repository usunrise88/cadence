// The panel SDK: the only shell surface panels import (besides the entity primitives and components/ui).
export { usePanel, useTopic } from "./context";
export { useFollowedDoc, useCurrentSelection, useSelection } from "@/shell/selection/store";
export type { PanelManifest, PanelProps } from "@/shell/registry/panels";
export type { DocTab } from "@/shell/entity/primitives";
export { openDocument, openPanelById, useProject } from "./actions";
export { openInLibrary, openRef, previewRef } from "@/shell/search/actions";
export { chipLabel, kindLabel, kindNoun, scopeOf, withScope } from "@/shell/search/hits";
export { useSearch } from "@/shell/search/store";
export { closePanelInstance, errorMessage, problemOf, runCommand, useCommand, type ApiCommandId, type ApiCommands, type CommandView } from "./commands";
// Approvals: the card the Approvals panel lists and the Chat panel embeds, and the live pending / decided lists.
export { ApprovalCard, type ApprovalCardProps } from "@/shell/approvals/ApprovalCard";
export { useDecidedApprovals, usePendingApprovals } from "@/shell/approvals/cache";
// Defaults: "Why this default?" and the safe-range warning for any form field (defaults.yaml via defaults.get).
export { formatDefault, formatRange, lookupDefault, rangeWarning, useDefaults, WhyDefault } from "@/shell/entity/defaults";
