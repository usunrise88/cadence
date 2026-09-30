import { ChatBubble, Check, Pause, Play, Plus, SendDiagonal, UndoAction, XmarkCircle } from "iconoir-react";
import type { QueryClient } from "@tanstack/react-query";
import { commandHeaders, commandHeadersAt } from "@/api/client";
import {
  agentMessagesNew,
  agentSessionsAccept,
  agentSessionsCancel,
  agentSessionsGet,
  agentSessionsNew,
  agentSessionsPause,
  agentSessionsResume,
  agentSessionsRevert,
  playbooksRun,
} from "@/api/gen/sdk.gen";
import type { AgentMessage, AgentReference, AgentSession, AgentSessionNew, PlaybookRunNew, PlaybookRunResult } from "@/api/gen/types.gen";
import { attachSelectionToChat, currentChatSession, openChat, SESSIONS_PANEL, useChatBridge } from "@/shell/agents/bridge";
import { patchSessions } from "@/shell/agents/sessions";
import { openPanel } from "@/shell/dock/layout";
import { commands } from "@/shell/registries";
import type { Command, CommandContext } from "./registry";

// Agent-session commands (docs/spec/11-ui-panels.md "Commands"): each calls exactly one API operation and carries
// its operationId; Chat and Agent sessions run them through runCommand with the session they act on. From a key or
// the palette they act on the session the current Chat shows. Ctrl/Cmd+I (Ask agent about the selection) is the
// client-only half of the bridge: it focuses Chat and attaches the selection; the send is agentMessages.new.

export type SessionArgs = { session: AgentSession };
export type SessionNewArgs = { project?: string; body: AgentSessionNew; /** Open the new session's Chat. */ open?: boolean };
export type SessionCancelArgs = SessionArgs & { end?: boolean };
export type MessageNewArgs = { sessionId: string; text: string; references?: AgentReference[] };
/** playbooks.run: version is the playbook's (the ETag of playbooks.get; "*" when absent); dryRun answers the estimate and plan. */
export type PlaybookRunArgs = { project?: string; name: string; version?: string; body: PlaybookRunNew; dryRun?: boolean; open?: boolean };

let queryClient: QueryClient | null = null;
/** The shell hands the query client over so command results patch the session caches at once. */
export function setAgentCommandsQueryClient(qc: QueryClient): void {
  queryClient = qc;
}
function patched(s: AgentSession | undefined): AgentSession | undefined {
  if (s && queryClient) patchSessions(queryClient, [s]);
  return s;
}

const needProject = (ctx: CommandContext): true | string => (ctx.project ? true : "Open a project first");
const needChatSession = (): true | string => (currentChatSession() ? true : "No agent session in Chat");

/** The session a key press acts on: the one the current Chat shows, read fresh for its revision. */
async function chatSession(): Promise<AgentSession> {
  const id = currentChatSession();
  if (!id) throw new Error("No agent session in Chat");
  const { data } = await agentSessionsGet({ path: { id }, throwOnError: true });
  return data;
}

async function sessionOf(args: unknown): Promise<AgentSession> {
  const s = (args as Partial<SessionArgs> | undefined)?.session;
  return s ?? chatSession();
}

export function registerAgentCommands(): void {
  const list: Command[] = [
    {
      id: "agentSessions.new",
      operation: "agentSessions.new",
      title: "New agent session…",
      group: "Project",
      icon: Plus,
      enabled: needProject,
      run: async (ctx, raw): Promise<AgentSession | undefined> => {
        const args = raw as SessionNewArgs | undefined;
        const project = args?.project ?? ctx.project;
        if (!project) return undefined;
        if (!args?.body) {
          openPanel(SESSIONS_PANEL, { location: "floating" });
          useChatBridge.getState().openNewSessionForm();
          return undefined;
        }
        const { data } = await agentSessionsNew({ path: { p: project }, body: args.body, headers: commandHeaders(), throwOnError: true });
        patched(data);
        if (args.open) openChat(data.id);
        return data;
      },
    },
    {
      id: "playbooks.run",
      operation: "playbooks.run",
      title: "Start a playbook…",
      group: "Project",
      icon: Play,
      enabled: needProject,
      run: async (ctx, raw): Promise<PlaybookRunResult | undefined> => {
        const args = raw as PlaybookRunArgs | undefined;
        const project = args?.project ?? ctx.project;
        if (!project) return undefined;
        if (!args?.name) {
          openPanel(SESSIONS_PANEL, { location: "floating" });
          useChatBridge.getState().openNewSessionForm("playbook");
          return undefined;
        }
        const headers = args.version ? commandHeadersAt(args.version) : { ...commandHeaders(), "If-Match": "*" };
        const { data } = await playbooksRun({
          path: { p: project, name: args.name },
          ...(args.dryRun ? { query: { dryRun: true } } : {}),
          body: args.body,
          headers,
          throwOnError: true,
        });
        if (data.session) {
          patched(data.session);
          if (args.open) openChat(data.session.id);
        }
        return data;
      },
    },
    {
      id: "agentSessions.cancel",
      operation: "agentSessions.cancel",
      title: "Stop the agent's turn",
      group: "Edit",
      icon: XmarkCircle,
      keys: ["Mod+."],
      allowInInput: true,
      enabled: (ctx) => (ctx.project ? needChatSession() : "Open a project first"),
      run: async (_ctx, raw): Promise<AgentSession | undefined> => {
        const s = await sessionOf(raw);
        const end = (raw as Partial<SessionCancelArgs> | undefined)?.end ?? false;
        const { data } = await agentSessionsCancel({ path: { id: s.id }, body: { end }, headers: commandHeaders(s.rev), throwOnError: true });
        return patched(data);
      },
    },
    {
      id: "agentSessions.pause",
      operation: "agentSessions.pause",
      title: "Pause agent session",
      group: "Edit",
      icon: Pause,
      enabled: needChatSession,
      run: async (_ctx, raw) => {
        const s = await sessionOf(raw);
        const { data } = await agentSessionsPause({ path: { id: s.id }, headers: commandHeaders(s.rev), throwOnError: true });
        return patched(data);
      },
    },
    {
      id: "agentSessions.resume",
      operation: "agentSessions.resume",
      title: "Resume agent session",
      group: "Edit",
      icon: Play,
      enabled: needChatSession,
      run: async (_ctx, raw) => {
        const s = await sessionOf(raw);
        const { data } = await agentSessionsResume({ path: { id: s.id }, body: {}, headers: commandHeaders(s.rev), throwOnError: true });
        return patched(data);
      },
    },
    {
      id: "agentSessions.accept",
      operation: "agentSessions.accept",
      title: "Accept session changes into main",
      group: "Edit",
      icon: Check,
      hidden: true,
      run: async (_ctx, raw) => {
        const s = await sessionOf(raw);
        const { data } = await agentSessionsAccept({ path: { id: s.id }, headers: commandHeaders(s.rev), throwOnError: true });
        return patched(data);
      },
    },
    {
      id: "agentSessions.revert",
      operation: "agentSessions.revert",
      title: "Discard session changes",
      group: "Edit",
      icon: UndoAction,
      hidden: true,
      run: async (_ctx, raw) => {
        const s = await sessionOf(raw);
        const { data } = await agentSessionsRevert({ path: { id: s.id }, headers: commandHeaders(s.rev), throwOnError: true });
        return patched(data);
      },
    },
    {
      id: "agentMessages.new",
      operation: "agentMessages.new",
      title: "Send message to the agent",
      group: "Edit",
      icon: SendDiagonal,
      hidden: true,
      run: async (_ctx, raw): Promise<AgentMessage> => {
        const args = raw as MessageNewArgs | undefined;
        if (!args?.text) throw new Error("Write a message in Chat first");
        const { data } = await agentMessagesNew({
          path: { id: args.sessionId },
          body: { text: args.text, ...(args.references?.length ? { references: args.references } : {}) },
          headers: commandHeaders(),
          throwOnError: true,
        });
        return data;
      },
    },
    {
      id: "view.askAgent",
      title: "Ask agent about the selection",
      group: "Go",
      icon: ChatBubble,
      keys: ["Mod+I"],
      allowInInput: true,
      enabled: needProject,
      run: () => attachSelectionToChat(),
    },
  ];
  for (const c of list) commands.register(c);
}
