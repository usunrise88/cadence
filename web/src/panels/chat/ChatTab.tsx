import { sessionIdOfDoc, sessionLabel, useAgentSession, useSelection, useUnread, type PanelTabProps } from "@/shell/panel";
import { cn } from "@/lib/utils";
import { tabLabel, tabTone, type TabTone } from "./model";

// The Chat's tab names its session the short way ("CC · S4": Claude Code, session 4; "OC" for opencode) and
// carries a dot while the session has news nobody has seen (shell/agents/unread.ts). The bubble keeps its outline in
// the text colour and is filled with the session's status.

const FILL: Record<TabTone, string> = {
  working: "fill-status-running",
  ready: "fill-status-done",
  attention: "fill-status-warning",
  paused: "fill-muted-foreground/40",
  asleep: "fill-muted-foreground/40", // an idle pause is not a problem: grey like any pause, never a warning
  failed: "fill-status-failed",
  none: "",
};
const TONE_LABEL: Record<TabTone, string> = { working: "working", ready: "waiting for you", attention: "waits for a decision", paused: "paused", asleep: "asleep", failed: "failed", none: "" };

export function ChatTab({ instanceId, doc, title, icon: Icon }: PanelTabProps) {
  const pinned = useSelection((s) => s.pins[instanceId]);
  const sessionId = sessionIdOfDoc(doc) ?? sessionIdOfDoc(pinned);
  const session = useAgentSession(sessionId).data;
  const unread = useUnread((s) => !!sessionId && !!s.unread[sessionId]);
  const tone = session ? tabTone(session) : "none";
  return (
    <>
      <Icon aria-hidden className={cn("size-3.5 shrink-0", session ? `text-foreground ${FILL[tone]}` : "opacity-80")} data-tone={session ? tone : undefined} data-slot="chat-tab-icon" />
      <span className="max-w-48 truncate" title={session ? sessionLabel(session) : undefined} data-slot="chat-tab-label">
        {session ? tabLabel(session) : title}
        {session && tone !== "none" ? <span className="sr-only">, {TONE_LABEL[tone]}</span> : null}
      </span>
      {unread ? (
        <span className="size-1.5 shrink-0 rounded-full bg-accent-line" data-slot="chat-tab-unread">
          <span className="sr-only">, unread</span>
        </span>
      ) : null}
    </>
  );
}
