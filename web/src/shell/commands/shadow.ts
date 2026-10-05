import { Play } from "iconoir-react";
import { commandHeaders } from "@/api/client";
import { shadowReplaysNew } from "@/api/gen/sdk.gen";
import type { ApprovalAccepted, ShadowReplay } from "@/api/gen/types.gen";
import { commands } from "@/shell/registries";
import type { Command } from "./registry";

// The Shadow panel's command (phase 5 · stream D4): shadowReplays.new replays a shadow deployment's newest calls not
// replayed yet now, instead of waiting for the night. GPU spend under the usual policy: the dry run answers the calls
// and the estimate, the real call the replay or an approval. Hidden from the palette: it needs the panel's deployment.

export type ShadowReplayArgs = { deploymentId: string; dryRun?: boolean };

export type ShadowCommands = {
  "shadowReplays.new": { args: ShadowReplayArgs; result: ShadowReplay | ApprovalAccepted };
};

export function registerShadowCommands(): void {
  const list: Command[] = [
    {
      id: "shadowReplays.new",
      operation: "shadowReplays.new",
      title: "Replay calls now",
      group: "Edit",
      icon: Play,
      hidden: true,
      run: async (_ctx, args) => {
        if (!args) throw new Error("Replay calls now: run it from the Shadow panel");
        const a = args as ShadowReplayArgs;
        const { data } = await shadowReplaysNew({
          path: { id: a.deploymentId },
          query: a.dryRun ? { dryRun: true } : undefined,
          headers: commandHeaders(),
          throwOnError: true,
        });
        return data as ShadowReplay | ApprovalAccepted;
      },
    },
  ];
  for (const c of list) commands.register(c);
}
