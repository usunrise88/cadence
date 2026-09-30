import { agentLabel, openToolCall } from "@/shell/agents/attribution";
import { notify } from "@/shell/notifications/store";
import type { EntityData } from "./manifest";

/**
 * Marks changes an agent made ("opencode · session 9", accent alpha 3 fill, accent-11 text); a click jumps to the
 * tool call in Chat once the agent-session stream installs its resolver (shell/agents/attribution.ts). People show
 * as their name.
 */
export function ActorBadge({ actor, toolCallId }: { actor?: EntityData["actor"]; toolCallId?: string }) {
  if (!actor) return null;
  if (actor.kind !== "agent") {
    return (
      <span data-slot="actor-badge" className="inline-flex h-5 items-center rounded px-1.5 text-xs text-muted-foreground">
        {actor.name ?? actor.id}
      </span>
    );
  }
  const label = agentLabel(actor);
  const open = () => {
    if (!openToolCall({ actor, toolCallId })) {
      notify({ level: "info", title: `Changed by ${label}`, detail: toolCallId ? `Tool call ${toolCallId}; the Chat panel shows it once agent sessions land.` : "The Chat panel shows the session once agent sessions land." });
    }
  };
  return (
    <button
      type="button"
      data-slot="actor-badge"
      data-agent-session={actor.sessionId}
      data-tool-call={toolCallId}
      onClick={open}
      title={toolCallId ? `${label} — open tool call ${toolCallId} in Chat` : `${label} — open the session in Chat`}
      className="inline-flex h-6 items-center rounded bg-agent px-1.5 text-xs font-medium whitespace-nowrap text-agent-foreground hover:underline"
    >
      {label}
    </button>
  );
}
