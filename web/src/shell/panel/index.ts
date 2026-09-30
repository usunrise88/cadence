// The panel SDK: the only shell surface panels import (besides the entity primitives and components/ui).
export { usePanel, useTopic } from "./context";
export { useFollowedDoc, useCurrentSelection, useSelection } from "@/shell/selection/store";
export type { PanelManifest, PanelProps, PanelTabProps } from "@/shell/registry/panels";
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
export { useEditRequest } from "@/shell/entity/edits";
// Agents: sessions and transcripts (live through useTopic + useAgentPatcher), the context bridge (which Chat shows
// which session, composer drafts, Ask agent, badge → tool call) and references as links.
export { isLive, sessionTopic, SESSIONS_TOPIC, useAgentPatcher, useAgentSession, useAgentSessions, useTranscript } from "@/shell/agents/sessions";
export { isAsleep, sessionLabel, sessionStateLabel } from "@/shell/agents/labels";
export { useUnread, viewSession } from "@/shell/agents/unread";
export {
  askAgent,
  CHAT_PANEL,
  currentSelectionReferences,
  openBranch,
  openChat,
  openReference,
  pinChat,
  sessionDoc,
  sessionIdOfDoc,
  useChatBridge,
  useChatDraft,
} from "@/shell/agents/bridge";
export { formatReference, linkifyReferences, parseReference, referenceChipLabel, referenceFromHref } from "@/shell/agents/references";
// Branches: the three-way view of conflicting files (branches.compare) that Session changes and the Recipe document share.
export { BranchConflicts, ThreeWayDiff, type BranchConflictsProps, type ThreeWayDiffProps } from "@/shell/diff/ThreeWayDiff";
