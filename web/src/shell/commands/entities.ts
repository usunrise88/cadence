import { Check, EditPencil, Plus, UndoAction } from "iconoir-react";
import { commandHeaders } from "@/api/client";
import { draftsAccept, draftsRevert, mixesEdit } from "@/api/gen/sdk.gen";
import type { Draft, MixEdit, MixEditResult } from "@/api/gen/types.gen";
import { useDialogs } from "@/shell/chrome/dialogs";
import { docRef, type EntityData } from "@/shell/entity/manifest";
import { useEditRequests } from "@/shell/entity/edits";
import { openDocument } from "@/shell/panel/actions";
import { notify } from "@/shell/notifications/store";
import { commands } from "@/shell/registries";
import { agentLabel } from "@/shell/agents/attribution";
import type { Command } from "./registry";

// Commands of work entities (mixes) and of drafts. Each calls exactly one API operation; panels run them through
// the panel SDK (runCommand) and show their errors in place (a 412 becomes the conflict notice).

const needProject = (ctx: { project?: string }): true | string => (ctx.project ? true : "Open a project first");

/** mixes.edit arguments: a patch against a revision; without a patch the command asks the open Mix to edit. */
export type MixEditArgs = { entity?: EntityData; mix?: { id: string; rev: number }; patch?: MixEdit };

export type DraftArgs = { draft: Draft };

function draftOf(args: unknown): Draft {
  const d = (args as Partial<DraftArgs> | undefined)?.draft;
  if (!d) throw new Error("no draft given; use Accept or Revert on the draft's outline");
  return d;
}

export function registerEntityCommands(): void {
  const list: Command[] = [
    {
      id: "mixes.new",
      operation: "mixes.new",
      title: "New mix…",
      group: "Project",
      icon: Plus,
      enabled: needProject,
      run: () => useDialogs.getState().show({ kind: "newMix" }),
    },
    {
      id: "mixes.edit",
      operation: "mixes.edit",
      title: "Edit mix",
      group: "Edit",
      icon: EditPencil,
      run: async (_ctx, args): Promise<MixEditResult | undefined> => {
        const a = (args ?? {}) as MixEditArgs;
        const target = a.mix ?? (a.entity ? { id: a.entity.id, rev: a.entity.rev ?? 0 } : undefined);
        if (!target) throw new Error("open a mix first");
        if (!a.patch) {
          const doc = docRef("mix", target.id);
          openDocument(doc);
          useEditRequests.getState().request(doc);
          return undefined;
        }
        const { data } = await mixesEdit({ path: { id: target.id }, body: a.patch, headers: commandHeaders(target.rev), throwOnError: true });
        if (data.draft) notify({ level: "info", title: "Your edit landed as a draft", detail: "The project's draft policy sends this edit to review." });
        return data;
      },
    },
    {
      id: "drafts.accept",
      operation: "drafts.accept",
      title: "Accept draft",
      group: "Edit",
      icon: Check,
      hidden: true,
      run: async (_ctx, args) => {
        const d = draftOf(args);
        const { data } = await draftsAccept({ path: { id: d.id }, headers: commandHeaders(d.rev), throwOnError: true });
        notify({ level: "success", title: `Draft by ${agentLabel(d.author)} accepted`, detail: `The ${d.entityKind} is at rev ${data.draft.appliedRev ?? "?"}.` });
        return data;
      },
    },
    {
      id: "drafts.revert",
      operation: "drafts.revert",
      title: "Revert draft",
      group: "Edit",
      icon: UndoAction,
      hidden: true,
      run: async (_ctx, args) => {
        const d = draftOf(args);
        const { data } = await draftsRevert({ path: { id: d.id }, headers: commandHeaders(d.rev), throwOnError: true });
        notify({ level: "info", title: `Draft by ${agentLabel(d.author)} reverted` });
        return data;
      },
    },
  ];
  for (const c of list) commands.register(c);
}
