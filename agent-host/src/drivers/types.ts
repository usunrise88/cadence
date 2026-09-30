// The ACP-shaped driver interface and the normalised update stream the rest of the host consumes (session
// manager, transcript events on `agent.session.{id}`, the Chat panel's rendering table in docs/spec/05-agents.md).
//
// A driver knows only what differs between agents: how to launch the process, which `_meta` to send on
// session/new, and how to read its tool calls (which ones are MCP, edits, shell, and where their command, exit
// code and output live). Everything else is plain ACP handled by src/acp and src/drivers/agent.ts.

import type * as acp from "@agentclientprotocol/sdk";
import type { LaunchSpec } from "../acp/transport.ts";
import type { HostCredentialTask } from "../api/gen/types.gen.ts";

export type DriverName = "claude" | "opencode";

export interface LaunchOptions {
  cwd: string;
  // Extra environment for the agent process (e.g. a per-session CLAUDE_CONFIG_DIR, XDG dirs for opencode).
  env?: NodeJS.ProcessEnv;
  // The environment the agent starts from; process.env when absent. The session manager passes a clean one so
  // nothing of the host's own environment (its token, its HOME) reaches an agent.
  baseEnv?: NodeJS.ProcessEnv;
  // Run the agent as this Unix user (R3: one user per session); the host runs as root to switch.
  user?: { uid: number; gid: number };
  // Replaces the agent command, e.g. a recorded-transcript replayer in tests.
  command?: { command: string; args: readonly string[] };
  // Agent model id in the driver's own naming ("opencode/big-pickle", "claude-sonnet-…"); undefined = agent default.
  model?: string;
  // Stream the model's reasoning as thought chunks when the agent can (Claude: summarized thinking display;
  // recent Claude models omit thinking text by default). opencode streams reasoning whenever the model emits it.
  thoughts?: boolean;
}

// What the Chat panel distinguishes (docs/spec/05-agents.md "What the Chat panel shows").
export type ToolClass = "mcp" | "edit" | "shell" | "read" | "search" | "fetch" | "think" | "other";

export interface FileDiff {
  path: string;
  oldText: string | null; // null = new file
  newText: string;
}

export interface ShellCall {
  command: string;
  exitCode?: number;
  output?: string;
}

// Fold of tool_call + tool_call_update(s): the full current state, emitted after every change.
export interface ToolCallSnapshot {
  id: string;
  title: string;
  acpKind: acp.ToolKind | undefined;
  class: ToolClass;
  status: acp.ToolCallStatus;
  mcp?: { server: string; tool: string };
  shell?: ShellCall;
  diffs: FileDiff[];
  locations: string[];
  text: string; // concatenated text content blocks
  rawInput?: unknown;
  rawOutput?: unknown;
}

// Raw ACP state the driver classifies (after folding updates).
export interface RawToolCall {
  toolCallId: string;
  title: string;
  kind: acp.ToolKind | undefined;
  status: acp.ToolCallStatus;
  content: acp.ToolCallContent[];
  locations: acp.ToolCallLocation[];
  rawInput?: unknown;
  rawOutput?: unknown;
  meta: Record<string, unknown>;
}

export interface ToolReading {
  class: ToolClass;
  mcp?: { server: string; tool: string };
  shell?: ShellCall;
  title?: string; // a better title than the agent's, when the driver has one
  // A todo tool the agent does not report as an ACP plan; the normaliser emits it as a `plan` update.
  plan?: acp.PlanEntry[];
}

export type TurnUsage = {
  inputTokens: number;
  outputTokens: number;
  totalTokens: number;
  thoughtTokens?: number;
  cachedReadTokens?: number;
  cachedWriteTokens?: number;
};

export type PermissionOptionKind = acp.PermissionOptionKind;

export type HostUpdate =
  | { kind: "message"; sessionId: string; text: string; content: acp.ContentBlock; messageId?: string }
  | { kind: "user_message"; sessionId: string; text: string; content: acp.ContentBlock }
  | { kind: "thought"; sessionId: string; text: string }
  | { kind: "plan"; sessionId: string; entries: acp.PlanEntry[] }
  | { kind: "tool_call"; sessionId: string; call: ToolCallSnapshot }
  | { kind: "permission"; sessionId: string; call: ToolCallSnapshot; options: acp.PermissionOption[] }
  | {
      kind: "permission_decided";
      sessionId: string;
      toolCallId: string;
      outcome: { optionId: string; optionKind: PermissionOptionKind | undefined } | "cancelled";
    }
  | {
      kind: "usage";
      sessionId: string;
      context?: { used: number; size: number };
      cost?: { amount: number; currency: string };
      turn?: TurnUsage;
    }
  | { kind: "state"; sessionId: string; state: "turn_started" | "turn_ended"; stopReason?: acp.StopReason }
  // Everything ACP carries that the Chat table does not render (commands, modes, config options, titles, notices,
  // compaction): passed through so the transcript loses nothing.
  | { kind: "info"; sessionId: string; update: acp.SessionUpdate };

export type HostUpdateKind = HostUpdate["kind"];

export interface Driver {
  readonly name: DriverName;
  launch(opts: LaunchOptions): LaunchSpec;
  // `_meta` for session/new, load and resume (agent options that ACP has no field for).
  sessionMeta?(opts: LaunchOptions): Record<string, unknown> | undefined;
  readTool(call: RawToolCall): ToolReading;
  // Fills a session's private HOME with the agent's own login from the agent-credentials volume (R3) and returns
  // the environment the agent finds it by. Without a volume (development) the agent uses its default login.
  prepareHome?(home: string, credentials: string | undefined): Promise<NodeJS.ProcessEnv>;
  // Logs the agent in once and stores what prepareHome copies, under the agent-credentials volume (interactive).
  login?(credentials: string): Promise<void>;
  // Agent credentials configured in Settings → Agents (hostCredentials tasks): write a value (and a custom provider's
  // settings) into the agent-credentials volume root in the files prepareHome reads, 0600; remove them again.
  writeCredential?(root: string, task: CredentialTask): Promise<void>;
  removeCredential?(root: string, task: CredentialTask): Promise<void>;
  // A tiny real request through the agent with the stored credential (and, for opencode, the provider's models).
  // Commands run through ctx.run: as the sandboxed user, in a throwaway directory, through the egress proxy.
  verify?(ctx: VerifyContext): Promise<VerifyResult>;
}

// One agent-credential task from the control plane (hostCredentials.claim). `value` is the secret: write it to the
// volume, never log or report it.
export type CredentialTask = HostCredentialTask;

export interface RunOptions {
  // Extra environment on top of the sandbox's (base environment + the driver's prepareHome environment).
  env?: NodeJS.ProcessEnv;
  timeoutMs?: number;
}

export interface RunResult {
  code: number | null;
  signal: NodeJS.Signals | null;
  stdout: string;
  stderr: string;
  timedOut: boolean;
}

// Runs a command as the verification's sandboxed user in its throwaway working directory.
export type Runner = (command: string, args: readonly string[], opts?: RunOptions) => Promise<RunResult>;

export interface VerifyContext {
  task: CredentialTask;
  // The agent-credentials volume.
  credentials: string;
  // The sandbox's private HOME (prepareHome already filled it); a driver that changes the volume copies again.
  home: string;
  run: Runner;
}

export interface VerifyResult {
  ok: boolean;
  // What happened, for a person; the host redacts anything key-shaped and shortens it before reporting.
  detail: string;
  model?: string;
  models?: string[];
}

// Host answer to a permission request. `select` names an option kind; the host maps it to the agent's option id.
export type PermissionDecision = { select: PermissionOptionKind } | { optionId: string } | "cancelled";

export type PermissionHandler = (
  request: { sessionId: string; call: ToolCallSnapshot; options: acp.PermissionOption[] },
  signal: AbortSignal,
) => Promise<PermissionDecision>;
