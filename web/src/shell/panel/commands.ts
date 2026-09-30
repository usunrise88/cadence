import { useSyncExternalStore } from "react";
import { ProblemError } from "@/api/client";
import type { Problem } from "@/api/gen/types.gen";
import { closePanel } from "@/shell/dock/layout";
import { commands } from "@/shell/registries";
import { commandContext } from "@/shell/state";

// Commands and errors for panels: panels change things only by running registered commands (one API operation
// each), and read a failed command's problem+json through problemOf.

export { runCommand, type ApiCommands, type ApiCommandId } from "@/shell/commands/api";

export type CommandView = { id: string; title: string; enabled: true | string; run: () => Promise<unknown> };

/** A registered command as a button needs it: title, enabled or the reason, and run. Undefined until it exists. */
export function useCommand(id: string): CommandView | undefined {
  useSyncExternalStore(
    (fn) => commands.subscribe(fn),
    () => commands.get(id),
  );
  const cmd = commands.get(id);
  if (!cmd) return undefined;
  return { id, title: cmd.title, enabled: commands.isEnabled(cmd, commandContext()), run: () => commands.run(id, commandContext()) };
}

/** The problem+json body of a failed command, if it was an API error. */
export function problemOf(err: unknown): Problem | undefined {
  return err instanceof ProblemError ? err.problem : undefined;
}

/** A readable one-line message for any error. */
export function errorMessage(err: unknown): string {
  const p = problemOf(err);
  if (p) return p.detail ?? p.title;
  return err instanceof Error ? err.message : String(err);
}

/** Closes a panel instance (e.g. a panel dismissing itself). */
export function closePanelInstance(instanceId: string): void {
  closePanel(instanceId);
}
