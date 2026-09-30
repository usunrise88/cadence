// The agent evals' vocabulary: an eval (fixture, prompt, budget, graders, the offline agent's reference policy), the
// observation the harness collects through the Cadence API after a session, and what a grader answers.

import type {
  Alias,
  AgentMessage,
  AgentSession,
  AgentSessionKind,
  Approval,
  AuditEntry,
  Draft,
  Mix,
} from "../src/api/gen/types.gen.ts";

export type { Alias, AgentMessage, AgentSession, Approval, AuditEntry, Draft, Mix };

/** The drivers of the matrix, as the host names them (the contract says claude-code for Claude). */
export type EvalDriver = "claude" | "opencode";

/** Most turns, input + output tokens and wall time a run may use; the harness stops waiting after wallSeconds. */
export interface Budget {
  turns: number;
  tokens: number;
  wallSeconds: number;
}

/** A mix the fixture project gets before the session starts (created with mixes.new as the person). */
export interface FixtureMix {
  name: string;
  datasets: string[];
  temperature?: number;
}

/** The result envelope of a Cadence tool call (control-plane/internal/mcp). */
export interface ToolResult {
  operation?: string;
  status: number;
  etag?: string;
  data?: unknown;
  error?: unknown;
  help?: string;
  dryRun?: boolean;
}

/**
 * What the offline agent can do: call a Cadence tool through MCP (asking permission first, as Claude does for every
 * call) and say something. A call the preset rejects returns `rejected` and never reaches Cadence.
 */
export interface ScriptedTools {
  project: string;
  call(operation: string, args: Record<string, unknown>): Promise<{ rejected: true } | { rejected: false; result: ToolResult }>;
  say(text: string): Promise<void>;
}

export interface Eval {
  id: string;
  title: string;
  kind: AgentSessionKind;
  /** The person's first message, as the Chat would send it. */
  prompt: string;
  fixture: { mixes: FixtureMix[] };
  /** What the collector reads after the session besides the session itself: these mixes and aliases. */
  observe: { mixes: string[]; aliases: string[] };
  budget: Budget;
  graders: Grader[];
  /** The scripted agent's policy for this prompt in offline mode: a correct reference answer. */
  offline(tools: ScriptedTools): Promise<void>;
}

/** A mix as the collector saw it: the current revision and every draft of it (open, accepted, reverted). */
export interface ObservedMix {
  mix: Mix;
  drafts: Draft[];
}

/** Everything a grader may look at, read through the Cadence API once the session ended. */
export interface Observation {
  eval: string;
  driver: EvalDriver;
  project: { slug: string; id: string };
  session: AgentSession;
  /** The transcript (agentMessages.list), oldest first. */
  transcript: AgentMessage[];
  /** The project's audit rows written by this session (actor.sessionId), oldest first. */
  audit: AuditEntry[];
  /** The project's approvals, read before the session ended (ending denies open permission requests). */
  approvals: Approval[];
  mixes: Record<string, ObservedMix | null>;
  /** null: the alias does not exist. */
  aliases: Record<string, Alias | null>;
  wallMs: number;
}

export interface GraderResult {
  pass: boolean;
  detail: string;
}

export interface Grader {
  id: string;
  grade(o: Observation): GraderResult;
}

export interface RunMetrics {
  turns: number;
  inputTokens: number;
  outputTokens: number;
  cachedReadTokens: number;
  wallMs: number;
  toolCalls: number;
  mcpCalls: number;
  permissionRequests: number;
}

/** One eval × driver. */
export interface RunResult {
  eval: string;
  driver: EvalDriver;
  model: string;
  mode: "offline" | "live";
  pass: boolean;
  graders: Array<{ id: string } & GraderResult>;
  metrics: RunMetrics;
  session?: { id: string; state: string; project: string };
  /** The harness could not run it (setup failed, the session never finished): every grader counts as failed. */
  error?: string;
}

export interface EvalReport {
  startedAt: string;
  finishedAt: string;
  mode: "offline" | "live";
  controlPlane: string;
  runs: RunResult[];
  summary: { runs: number; passed: number; failed: number };
}
