import { useParams } from "@tanstack/react-router";
import { openPanel } from "@/shell/dock/layout";
import { commands, panels } from "@/shell/registries";
import { commandContext } from "@/shell/state";
import { parseDocRef } from "@/shell/entity/manifest";

/** Opens (or focuses) the document panel registered for an entity kind. */
export function openDocument(doc: string): void {
  const ref = parseDocRef(doc);
  if (!ref) return;
  const m = panels.all().find((p) => p.kind === "document" && p.entity === ref.kind);
  if (m) openPanel(m.id, { doc });
}

/** Runs a registered command (the only way a panel changes anything); resolves with its result. */
export function runCommand(id: string, args?: unknown): Promise<unknown> {
  return commands.run(id, commandContext(), args);
}

export function openPanelById(panelId: string): void {
  openPanel(panelId);
}

/** The current project slug from the route (`/p/:project/w/:workspace`). */
export function useProject(): string | undefined {
  const params = useParams({ strict: false }) as { project?: string };
  return params.project;
}
