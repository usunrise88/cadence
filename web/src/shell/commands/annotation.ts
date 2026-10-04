import { Check, Lock, Plus, Prohibition, TextSquare, UserPlus } from "iconoir-react";
import { commandHeaders } from "@/api/client";
import { annotationsNew, batchesFreeze, batchesNew, batchItemsAccept, invitationsNew, triageAccept, triageCorrect, triageReject } from "@/api/gen/sdk.gen";
import type {
  AnnotationNew,
  ApprovalAccepted,
  Batch,
  BatchFreeze,
  BatchItem,
  BatchItemAccept,
  BatchNew,
  Invitation,
  InvitationCreated,
  InvitationNew,
  TriageAccept,
  TriageCorrect,
  TriageItem,
  TriageReject,
} from "@/api/gen/types.gen";
import { docRef, type EntityData } from "@/shell/entity/manifest";
import { useEditRequests } from "@/shell/entity/edits";
import { openDocument, openPanelById } from "@/shell/panel/actions";
import { commands } from "@/shell/registries";
import type { Command } from "./registry";

// Commands of annotation (phase 4 · stream A; docs/spec/04-blocks.md "Annotation workflow"): each is one API
// operation. batches.new without a body opens the Annotation batch panel's form; batches.freeze always waits for the
// admin's approval (dry run first answers what would freeze); annotations.new and batchItems.accept come from the
// Triage panel's Annotate mode and the Annotation batch document; triage.* from the Triage queue. Hidden from the
// palette when they need a selection the palette cannot give.

export type BatchNewArgs = { project: string; body: BatchNew; dryRun?: boolean };
/** batches.freeze on a batch (its revision is the If-Match); from the header `entity` stands in for it. */
export type BatchFreezeArgs = { batch?: Pick<Batch, "id" | "rev">; entity?: EntityData; dryRun?: boolean };
export type AnnotateArgs = { batch: string; item: string; body: AnnotationNew; dryRun?: boolean };
export type AdjudicateArgs = { batch: string; item: Pick<BatchItem, "id" | "rev">; body: BatchItemAccept; dryRun?: boolean };
export type InviteArgs = { batch: string; body?: InvitationNew; dryRun?: boolean };
export type TriageArgs<B> = { item: Pick<TriageItem, "id" | "rev">; body?: B; dryRun?: boolean };

export type AnnotationCommands = {
  "batches.new": { args: BatchNewArgs | undefined; result: Batch | undefined };
  "batches.freeze": { args: BatchFreezeArgs; result: BatchFreeze | ApprovalAccepted | undefined };
  "annotations.new": { args: AnnotateArgs; result: BatchItem };
  "batchItems.accept": { args: AdjudicateArgs; result: BatchItem };
  "invitations.new": { args: InviteArgs; result: InvitationCreated | Invitation | undefined };
  "triage.accept": { args: TriageArgs<TriageAccept>; result: TriageItem };
  "triage.correct": { args: TriageArgs<TriageCorrect>; result: TriageItem };
  "triage.reject": { args: TriageArgs<TriageReject>; result: TriageItem };
};

/** Requests only the open Annotation batch document completes: its invitation form. */
export const INVITE_REQUEST = "invite:";

const dry = (d?: boolean) => (d ? { dryRun: true } : undefined);

function need<T>(args: unknown, what: string, where: string): T {
  if (!args) throw new Error(`${what}: run it from the ${where}`);
  return args as T;
}

function batchOf(a: BatchFreezeArgs): Pick<Batch, "id" | "rev"> {
  const b = a.batch ?? (a.entity ? { id: a.entity.id, rev: a.entity.rev ?? 0 } : undefined);
  if (!b) throw new Error("Freeze batch: open an annotation batch first");
  return b;
}

export function registerAnnotationCommands(): void {
  const list: Command[] = [
    {
      id: "batches.new",
      operation: "batches.new",
      title: "New annotation batch…",
      group: "Project",
      icon: Plus,
      enabled: (ctx) => (ctx.project ? true : "Open a project first"),
      run: async (_ctx, args) => {
        const a = args as BatchNewArgs | undefined;
        if (!a?.body) {
          openPanelById("annotation-batch"); // its empty state is the New batch form
          return undefined;
        }
        const { data } = await batchesNew({ path: { p: a.project }, body: a.body, query: dry(a.dryRun), headers: commandHeaders(), throwOnError: true });
        return data;
      },
    },
    {
      id: "batches.freeze",
      operation: "batches.freeze",
      title: "Freeze batch",
      group: "Edit",
      icon: Lock,
      hidden: true,
      run: async (_ctx, args) => {
        const a = (args ?? {}) as BatchFreezeArgs;
        const b = batchOf(a);
        const { data } = await batchesFreeze({ path: { id: b.id }, query: dry(a.dryRun), headers: commandHeaders(b.rev), throwOnError: true });
        return data;
      },
    },
    {
      id: "annotations.new",
      operation: "annotations.new",
      title: "Submit annotation",
      group: "Edit",
      icon: TextSquare,
      hidden: true,
      run: async (_ctx, args) => {
        const a = need<AnnotateArgs>(args, "Submit annotation", "Triage panel's Annotate mode");
        const { data } = await annotationsNew({ path: { id: a.batch, item: a.item }, body: a.body, query: dry(a.dryRun), headers: commandHeaders(), throwOnError: true });
        return data;
      },
    },
    {
      id: "batchItems.accept",
      operation: "batchItems.accept",
      title: "Adjudicate item",
      group: "Edit",
      icon: Check,
      hidden: true,
      run: async (_ctx, args) => {
        const a = need<AdjudicateArgs>(args, "Adjudicate item", "Annotation batch document");
        const { data } = await batchItemsAccept({ path: { id: a.batch, item: a.item.id }, body: a.body, query: dry(a.dryRun), headers: commandHeaders(a.item.rev), throwOnError: true });
        return data;
      },
    },
    {
      id: "invitations.new",
      operation: "invitations.new",
      title: "Invite a reviewer",
      group: "Edit",
      icon: UserPlus,
      hidden: true,
      run: async (_ctx, args) => {
        const a = need<InviteArgs>(args, "Invite a reviewer", "Annotation batch document");
        if (!a.body) {
          const doc = docRef("annotation_batch", a.batch);
          openDocument(doc);
          useEditRequests.getState().request(`${INVITE_REQUEST}${doc}`);
          return undefined;
        }
        const { data } = await invitationsNew({ path: { id: a.batch }, body: a.body, query: dry(a.dryRun), headers: commandHeaders(), throwOnError: true });
        return data;
      },
    },
    {
      id: "triage.accept",
      operation: "triage.accept",
      title: "Accept the best candidate",
      group: "Edit",
      icon: Check,
      hidden: true,
      run: async (_ctx, args) => {
        const a = need<TriageArgs<TriageAccept>>(args, "Accept", "Triage queue");
        const { data } = await triageAccept({ path: { id: a.item.id }, body: a.body ?? {}, query: dry(a.dryRun), headers: commandHeaders(a.item.rev), throwOnError: true });
        return data;
      },
    },
    {
      id: "triage.correct",
      operation: "triage.correct",
      title: "Correct the transcript",
      group: "Edit",
      icon: TextSquare,
      hidden: true,
      run: async (_ctx, args) => {
        const a = need<TriageArgs<TriageCorrect>>(args, "Correct", "Triage queue");
        if (!a.body) throw new Error("Correct: type the transcript first");
        const { data } = await triageCorrect({ path: { id: a.item.id }, body: a.body, query: dry(a.dryRun), headers: commandHeaders(a.item.rev), throwOnError: true });
        return data;
      },
    },
    {
      id: "triage.reject",
      operation: "triage.reject",
      title: "Reject the segment",
      group: "Edit",
      icon: Prohibition,
      hidden: true,
      run: async (_ctx, args) => {
        const a = need<TriageArgs<TriageReject>>(args, "Reject", "Triage queue");
        const { data } = await triageReject({ path: { id: a.item.id }, body: a.body ?? {}, query: dry(a.dryRun), headers: commandHeaders(a.item.rev), throwOnError: true });
        return data;
      },
    },
  ];
  for (const c of list) commands.register(c);
}
