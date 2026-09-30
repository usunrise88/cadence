import { sessionIdOfDoc, sessionLabel, useAgentSession, useSelection, useUnread, type PanelTabProps } from "@/shell/panel";
import { tabLabel } from "./model";

// The Chat's tab names its session the short way ("CC · S4": Claude Code, session 4; "OC" for opencode) and
// carries a dot while the session has news nobody has seen (shell/agents/unread.ts).

export function ChatTab({ instanceId, doc, title, icon: Icon }: PanelTabProps) {
  const pinned = useSelection((s) => s.pins[instanceId]);
  const sessionId = sessionIdOfDoc(doc) ?? sessionIdOfDoc(pinned);
  const session = useAgentSession(sessionId).data;
  const unread = useUnread((s) => !!sessionId && !!s.unread[sessionId]);
  return (
    <>
      <Icon aria-hidden className="size-3.5 shrink-0 opacity-80" />
      <span className="max-w-48 truncate" title={session ? sessionLabel(session) : undefined} data-slot="chat-tab-label">
        {session ? tabLabel(session) : title}
      </span>
      {unread ? (
        <span className="size-1.5 shrink-0 rounded-full bg-accent-line" data-slot="chat-tab-unread">
          <span className="sr-only">, unread</span>
        </span>
      ) : null}
    </>
  );
}
