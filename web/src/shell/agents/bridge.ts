import { create } from "zustand";
import type { AgentReference, AgentSession } from "@/api/gen/types.gen";
import { openAudio } from "@/shell/audio/target";
import { openPanel } from "@/shell/dock/layout";
import { dockApi } from "@/shell/dock/store";
import { parseDocRef } from "@/shell/entity/manifest";
import { useHelp } from "@/shell/help/store";
import { panels } from "@/shell/registries";
import type { PanelParams } from "@/shell/registry/panels";
import { openRef } from "@/shell/search/actions";
import { useSelection } from "@/shell/selection/store";
import { setAgentResolver } from "./attribution";
import { dedupeReferences, formatReference, parseReference, selectionReferences } from "./references";
import { knownSession, notifyLabels, sessionLabel } from "./labels";

// The context bridge (docs/spec/05-agents.md "Context bridge"; docs/spec/10-ui-shell.md keyboard map): which Chat
// shows which session, the composer drafts that "Ask agent" and Ctrl/Cmd+I fill, and the jump from an attribution
// badge to the tool call. Chat is one panel per agent session: the workspace's own Chat (instance `chat`) is pinned
// to a session through the selection bus pins, which the workspace stores (`panels.chat.pinnedTo`); further Chats
// open as `chat:agent_session:<id>`.

export const CHAT_PANEL = "chat";
export const SESSIONS_PANEL = "agent-sessions";
export const SESSION_KIND = "agent_session";

export const sessionDoc = (id: string) => `${SESSION_KIND}:${id}`;

export function sessionIdOfDoc(doc: string | undefined | null): string | undefined {
  const d = doc ? parseDocRef(doc) : undefined;
  return d?.kind === SESSION_KIND ? d.id : undefined;
}

export type Draft = { text: string; refs: AgentReference[] };
export type Highlight = { sessionId: string; toolCallId?: string; nonce: number };

type BridgeState = {
  /** Composer drafts per Chat instance. */
  drafts: Record<string, Draft>;
  /** The Chat instance that last had focus (Ctrl/Cmd+I and Ctrl/Cmd+. act on it). */
  lastChat: string | null;
  /** Ask the composer of an instance to take focus (a counter the composer watches). */
  focus: Record<string, number>;
  highlight: Highlight | null;
  /** The Agent sessions panel opens its New session form (a counter), interactive or from a playbook. */
  newSessionForm: number;
  newSessionMode: "interactive" | "playbook";
  setDraft(instanceId: string, d: Partial<Draft>): void;
  addRefs(instanceId: string, refs: AgentReference[]): void;
  removeRef(instanceId: string, ref: string): void;
  clearDraft(instanceId: string): void;
  setLastChat(instanceId: string): void;
  focusComposer(instanceId: string): void;
  setHighlight(h: Omit<Highlight, "nonce"> | null): void;
  openNewSessionForm(mode?: "interactive" | "playbook"): void;
};

const EMPTY: Draft = { text: "", refs: [] };
let nonce = 0;

export const useChatBridge = create<BridgeState>((set) => ({
  drafts: {},
  lastChat: null,
  focus: {},
  highlight: null,
  newSessionForm: 0,
  newSessionMode: "interactive",
  setDraft: (id, d) => set((s) => ({ drafts: { ...s.drafts, [id]: { ...(s.drafts[id] ?? EMPTY), ...d } } })),
  addRefs: (id, refs) =>
    set((s) => {
      const cur = s.drafts[id] ?? EMPTY;
      return { drafts: { ...s.drafts, [id]: { ...cur, refs: dedupeReferences([...cur.refs, ...refs]) } } };
    }),
  removeRef: (id, ref) =>
    set((s) => {
      const cur = s.drafts[id] ?? EMPTY;
      return { drafts: { ...s.drafts, [id]: { ...cur, refs: cur.refs.filter((r) => r.ref !== ref) } } };
    }),
  clearDraft: (id) =>
    set((s) => {
      const drafts = { ...s.drafts };
      delete drafts[id];
      return { drafts };
    }),
  setLastChat: (id) => set((s) => (s.lastChat === id ? s : { lastChat: id })),
  focusComposer: (id) => set((s) => ({ focus: { ...s.focus, [id]: (s.focus[id] ?? 0) + 1 } })),
  setHighlight: (h) => set({ highlight: h ? { ...h, nonce: ++nonce } : null }),
  openNewSessionForm: (mode = "interactive") => set((s) => ({ newSessionForm: s.newSessionForm + 1, newSessionMode: mode })),
}));

export function useChatDraft(instanceId: string): Draft {
  return useChatBridge((s) => s.drafts[instanceId] ?? EMPTY);
}

// ---------------------------------------------------------------- which Chat shows which session

export type ChatInstance = { instanceId: string; sessionId: string | undefined };

/** The session a Chat instance shows: its own document, else the workspace pin. */
export function chatSession(instanceId: string, doc: string | undefined): string | undefined {
  return sessionIdOfDoc(doc) ?? sessionIdOfDoc(useSelection.getState().pins[instanceId]);
}

/** Open Chat instances (in the main window's layout, floats and popouts included). */
export function openChats(): ChatInstance[] {
  const api = dockApi();
  if (!api) return [];
  const out: ChatInstance[] = [];
  for (const p of api.panels) {
    const params = p.params as Partial<PanelParams> | undefined;
    if (params?.panel !== CHAT_PANEL) continue;
    out.push({ instanceId: p.id, sessionId: chatSession(p.id, params.doc) });
  }
  return out;
}

/** Pins the workspace Chat (or any doc-less Chat instance) to a session; the workspace remembers it. */
export function pinChat(instanceId: string, sessionId: string | null): void {
  const sel = useSelection.getState();
  if (sessionId) sel.pin(instanceId, sessionDoc(sessionId));
  else sel.unpin(instanceId);
}

function activate(instanceId: string): void {
  openPanel(CHAT_PANEL, instanceId === CHAT_PANEL ? {} : { doc: instanceId.slice(CHAT_PANEL.length + 1) });
}

/**
 * Shows a session in Chat and returns the instance: the Chat already showing it, else the workspace Chat when it
 * shows nothing yet, else a new Chat for the session next to the others.
 */
export function openChat(sessionId: string, opts: { toolCallId?: string } = {}): string {
  const chats = openChats();
  let target = chats.find((c) => c.sessionId === sessionId)?.instanceId;
  if (!target) {
    const free = chats.find((c) => c.instanceId === CHAT_PANEL && !c.sessionId);
    if (free) {
      pinChat(free.instanceId, sessionId);
      target = free.instanceId;
    } else if (!chats.some((c) => c.instanceId === CHAT_PANEL)) {
      target = openPanel(CHAT_PANEL);
      pinChat(target, sessionId);
    } else {
      target = openPanel(CHAT_PANEL, { doc: sessionDoc(sessionId) });
    }
  }
  activate(target);
  useChatBridge.getState().setLastChat(target);
  if (opts.toolCallId) useChatBridge.getState().setHighlight({ sessionId, toolCallId: opts.toolCallId });
  return target;
}

/** The Chat that Ctrl/Cmd+I and "Ask agent" fill: the last focused one still open, else the workspace Chat. */
export function targetChat(): string {
  const chats = openChats();
  const last = useChatBridge.getState().lastChat;
  if (last && chats.some((c) => c.instanceId === last)) return last;
  const ws = chats.find((c) => c.instanceId === CHAT_PANEL) ?? chats[0];
  return ws?.instanceId ?? openPanel(CHAT_PANEL);
}

/** The session the current Chat shows (Ctrl/Cmd+. stops its turn). */
export function currentChatSession(): string | undefined {
  const chats = openChats();
  const last = useChatBridge.getState().lastChat;
  const c = chats.find((x) => x.instanceId === last) ?? chats.find((x) => x.instanceId === CHAT_PANEL) ?? chats[0];
  return c?.sessionId;
}

/** The references the current selection attaches (the active document and the item selected in it). */
export function currentSelectionReferences(): AgentReference[] {
  const { activeDoc, selections } = useSelection.getState();
  if (!activeDoc) return [];
  return selectionReferences({ doc: activeDoc, item: selections[activeDoc] });
}

/** Ctrl/Cmd+I: focus Chat and attach the current selection. */
export function attachSelectionToChat(): string {
  const target = targetChat();
  const refs = currentSelectionReferences();
  const b = useChatBridge.getState();
  if (refs.length) b.addRefs(target, refs);
  activate(target);
  b.setLastChat(target);
  b.focusComposer(target);
  return target;
}

/** Attaches references (an audio span, say) to the current Chat's composer and focuses it. */
export function attachToChat(refs: AgentReference[]): string {
  const target = targetChat();
  const b = useChatBridge.getState();
  if (refs.length) b.addRefs(target, refs);
  activate(target);
  b.setLastChat(target);
  b.focusComposer(target);
  return target;
}

/**
 * "Ask agent" on an entity or an empty state: Chat opens with the references attached and a prefilled prompt
 * naming the entity and the intent (docs/spec/11-ui-panels.md "Progressive disclosure": never blank).
 */
export function askAgent(opts: { refs: AgentReference[]; intent: string }): string {
  const target = targetChat();
  const b = useChatBridge.getState();
  const names = opts.refs.map((r) => r.ref).join(" ");
  b.addRefs(target, opts.refs);
  b.setDraft(target, { text: names ? `${opts.intent} (${names})` : opts.intent });
  activate(target);
  b.setLastChat(target);
  b.focusComposer(target);
  return target;
}

/** The prompt of "Explain this": what it is, where it sits in the loop and what to do next. */
export function explainPrompt(what: string, article?: string): string {
  const help = article ? ` Start from the help article ${formatReference("help_article", article)}.` : "";
  return `Explain ${what}: what it is, where it sits in the Cadence loop, what its fields mean here and what to do next.${help} This is a read-only session: do not change anything.`;
}

/** The references of "Explain this": the entity (if any) and the help article. */
export function explainReferences(entity: { kind: string; id: string; label?: string } | null, article?: string): AgentReference[] {
  const refs: AgentReference[] = [];
  if (entity) refs.push({ ref: formatReference(entity.kind, entity.id), ...(entity.label ? { label: entity.label } : {}) });
  if (article) refs.push({ ref: formatReference("help_article", article), label: `Help: ${article}` });
  return refs.filter((r) => !!parseReference(r.ref));
}

/** Opens what a reference points at: a session in Chat, a help article in Help, a document, else a preview. */
export function openReference(ref: string): void {
  const p = parseReference(ref);
  if (!p) return;
  if (p.kind === SESSION_KIND) {
    openChat(p.id);
    return;
  }
  if (p.kind === "help_article") {
    useHelp.getState().show(p.id);
    openPanel("help");
    return;
  }
  if (p.kind === "utterance" && p.fragment && /(^|&)t=/.test(p.fragment)) {
    // A span (`@utt:<id>#t=1.20,2.35`, R51) plays in the Audio panel.
    openAudio(`utt:${p.id}#${p.fragment}`);
    return;
  }
  const doc = `${p.kind}:${p.id}`;
  openRef(doc);
  if (p.fragment && panels.all().some((m) => m.kind === "document" && m.entity === p.kind)) useSelection.getState().select(doc, p.fragment);
}

/** Opens a session branch in the Recipe document's branch view (the open recipe file, else project.yaml). */
export function openBranch(branch: string): void {
  const active = useSelection.getState().activeDoc;
  const doc = active && parseDocRef(active)?.kind === "recipe" ? active : "recipe:project.yaml";
  useSelection.getState().select(doc, `branch:${branch}`);
  openRef(doc);
}

// ---------------------------------------------------------------- attribution: badge → tool call

/** Installs the attribution resolver: labels from known sessions, clicks to the tool call in Chat. */
export function installAgentBridge(): void {
  setAgentResolver({
    label: (actor) => {
      const s = knownSession(actor.sessionId);
      return s ? sessionLabel(s) : undefined;
    },
    open: ({ actor, toolCallId }) => {
      if (actor.sessionId) openChat(actor.sessionId, { toolCallId });
    },
  });
  notifyLabels();
}

export type { AgentSession };
