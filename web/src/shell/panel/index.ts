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
  attachToChat,
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
// Playbooks: the start form (inputs from the playbook's schema, the estimate before starting) that Agent sessions and
// the Project home share, and a playbook session's plan as the server ticks it.
export { PlaybookLauncher, type PlaybookLauncherProps } from "@/shell/agents/PlaybookLauncher";
export { budgetNote, estimateLine, planProgress, playbookStateLabel } from "@/shell/agents/playbooks";
// Training (phase 2): the log view Logs shows and Pipeline run embeds; forms rendered from a parameter schema
// (x-cadence); the active job and pipeline run that Logs and Pipeline run follow.
export { LogView, LOG_ROW_HEIGHT, type LogViewProps } from "@/shell/logs/LogView";
export { SchemaForm, type SchemaFormProps } from "@/shell/forms/SchemaForm";
export { departuresOf, recommended, type ParamSchema, type Values } from "@/shell/forms/schema";
export { AUGMENT_DIR, AUGMENTATION_PROFILE_SCHEMA, isAugmentationProfile, newProfile, parseProfile, writeProfile } from "@/shell/forms/augmentation";
export { focusJob, focusPipelineRun, useFocusedJob, useFocusedPipelineRun, type FocusedJob } from "@/shell/training/focus";
export { DAY_LABEL, DAYS, formatDays, formatWindows, windowError } from "@/shell/training/windows";
export { runIdOfDoc, runLabel } from "@/shell/training/runs";
export { sortEntries } from "@/shell/training/queue";
export { useActiveRun } from "@/shell/training/useActiveRun";
export { useKeyedEstimate, type KeyedEstimate } from "@/shell/training/estimate";
// Evaluation (phase 3): rates, deltas, gate and alignment glyphs, writing direction, and the selection items an Eval
// report shares with Diff (cell:<id>/utt:<n>); live invalidation of eval and language pack reads.
export {
  alignedWords,
  alignmentCounts,
  deltaTone,
  evalIdOfDoc,
  evalItem,
  formatDelta,
  formatInterval,
  formatRate,
  GATE_CLASS,
  GATE_GLYPH,
  OP_GLYPH,
  OP_LABEL,
  parseEvalItem,
  registrable,
  textDirection,
  TONE_CLASS,
  TONE_GLYPH,
  TONE_LABEL,
  WORST_N,
  type AlignedWord,
  type AlignOp,
  type DeltaTone,
  type GateState,
} from "@/shell/evaluation/format";
export { invalidateEval, invalidateLangpacks } from "@/shell/evaluation/cache";
