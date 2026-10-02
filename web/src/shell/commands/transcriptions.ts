import { Microphone } from "iconoir-react";
import { commandHeaders } from "@/api/client";
import { transcriptionsNew } from "@/api/gen/sdk.gen";
import type { TranscriptionNew, TranscriptionSession } from "@/api/gen/types.gen";
import { commands } from "@/shell/registries";
import type { Command } from "./registry";

// transcriptions.new (tag media, R47): the Transcription panel's one API operation. Not an MCP tool and not in the
// palette (it needs the panel's form); the answer carries the live socket's URL and single-use ticket, which the
// panel spends at once.

export type TranscriptionNewArgs = { project: string; body: TranscriptionNew; dryRun?: boolean };

export type TranscriptionCommands = {
  "transcriptions.new": { args: TranscriptionNewArgs; result: TranscriptionSession };
};

export function registerTranscriptionCommands(): void {
  const list: Command[] = [
    {
      id: "transcriptions.new",
      operation: "transcriptions.new",
      title: "Open a transcription test",
      group: "Project",
      icon: Microphone,
      hidden: true,
      run: async (_ctx, args) => {
        if (!args) throw new Error("Open a transcription test: run it from the Transcription panel");
        const a = args as TranscriptionNewArgs;
        const { data } = await transcriptionsNew({
          path: { p: a.project },
          body: a.body,
          query: a.dryRun ? { dryRun: true } : undefined,
          headers: commandHeaders(),
          throwOnError: true,
        });
        return data;
      },
    },
  ];
  for (const c of list) commands.register(c);
}
