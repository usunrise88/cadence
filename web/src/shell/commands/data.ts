import { Archive, Eye, Lock, ShareIos, ShieldCheck } from "iconoir-react";
import { commandHeaders } from "@/api/client";
import { datasetsExport, datasetsFreeze, datasetsPreview, sourcesArchive, sourcesEdit, versionsArchive } from "@/api/gen/sdk.gen";
import type {
  ApprovalAccepted,
  DatasetExport,
  DatasetExportPlan,
  DatasetExportRequest,
  DatasetFreeze,
  DatasetPreview,
  DatasetPreviewRequest,
  JobAccepted,
  RegistryVersion,
  Source,
  SourceEdit,
} from "@/api/gen/types.gen";
import { useDialogs } from "@/shell/chrome/dialogs";
import { docRef, parseDocRef, type EntityData } from "@/shell/entity/manifest";
import { useEditRequests } from "@/shell/entity/edits";
import { notify, notifyError } from "@/shell/notifications/store";
import { openDocument } from "@/shell/panel/actions";
import { commands } from "@/shell/registries";
import type { Command, CommandContext } from "./registry";

// Commands behind the data panels (phase 4 · stream R: Source, Dataset version, the Library): each is exactly one API
// operation. Without the arguments a form supplies (the palette, a document header) a command opens the document
// whose card asks for them, like evals.new and projects.adopt do.

/** Requests only an open document can complete (useEditRequest on `<prefix><doc>`). */
export const FREEZE_REQUEST = "freeze:";
export const PREVIEW_REQUEST = "preview:";
export const CLEAR_REQUEST = "clear:";
export const EXPORT_REQUEST = "export:";

/**
 * datasets.export: `body` names the frozen version, the format and the target (phase 4 tail: the export UI). A dry run
 * answers the plan (DatasetExportPlan); the real call answers the export (201) or, for the Hub, an approval (202).
 * From the header (`entity`) the Dataset version document shows its export card, which plans first.
 */
export type DatasetExportArgs = { body?: DatasetExportRequest; dryRun?: boolean; entity?: EntityData };

/**
 * datasets.freeze: `version` is the draft (ver_…). A dry run runs the leakage check only (200 DatasetFreeze); the real
 * call answers 202 with the cut job, or 200 when the version is frozen already. From the header (`entity`) the
 * Dataset version document shows its freeze card, which checks first.
 */
export type DatasetFreezeArgs = { version?: string; dryRun?: boolean; entity?: EntityData };
/** datasets.preview: hours per language and split after filters; nothing is written. */
export type DatasetPreviewArgs = { body?: DatasetPreviewRequest; entity?: EntityData };
/**
 * sources.edit: `source` is the current source (its rev is the If-Match). An agent's call waits for a person's
 * approval (202). From the header (`entity`) the Source document shows its clearance card.
 */
export type SourceEditArgs = { source?: Source; body?: SourceEdit; dryRun?: boolean; entity?: EntityData };
/** sources.archive: the admin's; asks first. */
export type SourceArchiveArgs = { source?: Source; entity?: EntityData };
/** versions.archive: the admin's soft delete of a registry version; asks first unless `confirmed`. */
export type VersionArchiveArgs = { version?: string; name?: string; dryRun?: boolean; confirmed?: boolean; entity?: EntityData };

export type DataCommands = {
  "datasets.freeze": { args: DatasetFreezeArgs; result: DatasetFreeze | JobAccepted | undefined };
  "datasets.preview": { args: DatasetPreviewArgs; result: DatasetPreview | undefined };
  "datasets.export": { args: DatasetExportArgs; result: DatasetExportPlan | DatasetExport | ApprovalAccepted | undefined };
  "sources.edit": { args: SourceEditArgs; result: Source | ApprovalAccepted | undefined };
  "sources.archive": { args: SourceArchiveArgs; result: Source | ApprovalAccepted | undefined };
  "versions.archive": { args: VersionArchiveArgs; result: RegistryVersion | ApprovalAccepted | undefined };
};

/** The id of the active document when it is of `kind`. */
function activeOf(ctx: CommandContext, kind: string): string | undefined {
  const d = ctx.activeDoc ? parseDocRef(ctx.activeDoc) : undefined;
  return d?.kind === kind ? d.id : undefined;
}

/** Opens the document and asks its card to show (useEditRequest in the panel). */
function requestCard(kind: string, id: string, prefix: string): undefined {
  const doc = docRef(kind, id);
  openDocument(doc);
  useEditRequests.getState().request(`${prefix}${doc}`);
  return undefined;
}

const needActive = (kind: string, what: string) => (ctx: CommandContext) => (activeOf(ctx, kind) ? true : `Open ${what} first`);

function sourceOf(a: { source?: Source; entity?: EntityData }): Source | undefined {
  return a.source ?? (a.entity?.source as Source | undefined);
}

export function registerDataCommands(): void {
  const list: Command[] = [
    {
      id: "datasets.freeze",
      operation: "datasets.freeze",
      title: "Freeze dataset version…",
      group: "Project",
      icon: Lock,
      enabled: needActive("dataset_version", "a dataset version"),
      run: async (ctx, args) => {
        const a = (args ?? {}) as DatasetFreezeArgs;
        if (!a.version) {
          const id = a.entity?.id ?? activeOf(ctx, "dataset_version");
          if (!id) throw new Error("Freeze: open a draft dataset version first");
          return requestCard("dataset_version", id, FREEZE_REQUEST);
        }
        const { data } = await datasetsFreeze({
          body: { version: a.version },
          query: a.dryRun ? { dryRun: true } : undefined,
          headers: commandHeaders(),
          throwOnError: true,
        });
        return data;
      },
    },
    {
      id: "datasets.preview",
      operation: "datasets.preview",
      title: "Preview dataset version…",
      group: "Project",
      icon: Eye,
      enabled: needActive("dataset_version", "a dataset version"),
      run: async (ctx, args) => {
        const a = (args ?? {}) as DatasetPreviewArgs;
        if (!a.body) {
          const id = a.entity?.id ?? activeOf(ctx, "dataset_version");
          if (!id) throw new Error("Preview: open a dataset version first");
          return requestCard("dataset_version", id, PREVIEW_REQUEST);
        }
        const { data } = await datasetsPreview({ body: a.body, headers: commandHeaders(), throwOnError: true });
        return data;
      },
    },
    {
      id: "datasets.export",
      operation: "datasets.export",
      title: "Export dataset version…",
      group: "Project",
      icon: ShareIos,
      enabled: needActive("dataset_version", "a dataset version"),
      run: async (ctx, args) => {
        const a = (args ?? {}) as DatasetExportArgs;
        if (!a.body) {
          const id = a.entity?.id ?? activeOf(ctx, "dataset_version");
          if (!id) throw new Error("Export: open a frozen dataset version first");
          return requestCard("dataset_version", id, EXPORT_REQUEST);
        }
        const { data } = await datasetsExport({
          body: a.body,
          query: a.dryRun ? { dryRun: true } : undefined,
          headers: commandHeaders(),
          throwOnError: true,
        });
        return data as DatasetExportPlan | DatasetExport | ApprovalAccepted;
      },
    },
    {
      id: "sources.edit",
      operation: "sources.edit",
      title: "Clear source for training…",
      group: "Project",
      icon: ShieldCheck,
      enabled: needActive("source", "a source"),
      run: async (ctx, args) => {
        const a = (args ?? {}) as SourceEditArgs;
        const src = sourceOf(a);
        if (!a.body || !src) {
          const id = src?.id ?? a.entity?.id ?? activeOf(ctx, "source");
          if (!id) throw new Error("Edit source: open a source first");
          return requestCard("source", id, CLEAR_REQUEST);
        }
        const { data } = await sourcesEdit({
          path: { id: src.id },
          body: a.body,
          query: a.dryRun ? { dryRun: true } : undefined,
          headers: commandHeaders(src.rev),
          throwOnError: true,
        });
        return data;
      },
    },
    {
      id: "sources.archive",
      operation: "sources.archive",
      title: "Archive source…",
      group: "Project",
      icon: Archive,
      enabled: needActive("source", "a source"),
      run: (_ctx, args) => {
        const src = sourceOf((args ?? {}) as SourceArchiveArgs);
        if (!src) throw new Error("Archive source: open a source first");
        useDialogs.getState().show({
          kind: "confirm",
          title: `Archive source “${src.name}”?`,
          detail: "An archived source takes no new imports or ingests and cannot be edited; its utterances and dataset versions stay.",
          confirmLabel: "Archive",
          onConfirm: async () => {
            try {
              await sourcesArchive({ path: { id: src.id }, headers: commandHeaders(src.rev), throwOnError: true });
              notify({ level: "success", title: `Source “${src.name}” archived` });
            } catch (err) {
              notifyError("Archive failed", err);
            }
          },
        });
        return undefined;
      },
    },
    {
      id: "versions.archive",
      operation: "versions.archive",
      title: "Archive registry version…",
      group: "Project",
      icon: Archive,
      run: async (ctx, args) => {
        const a = (args ?? {}) as VersionArchiveArgs;
        const version = a.version ?? a.entity?.id ?? (ctx.activeDoc ? parseDocRef(ctx.activeDoc)?.id : undefined);
        if (!version?.startsWith("ver_")) throw new Error("Archive: open a registry version first");
        const name = a.name ?? a.entity?.name ?? version;
        const call = async (dryRun: boolean) => {
          const { data } = await versionsArchive({ body: { version }, query: dryRun ? { dryRun: true } : undefined, headers: commandHeaders(), throwOnError: true });
          return data;
        };
        if (a.dryRun || a.confirmed) return call(!!a.dryRun);
        useDialogs.getState().show({
          kind: "confirm",
          title: `Archive ${name}?`,
          detail: "The registry never deletes: the version keeps its content and lineage, but nothing can adopt it and the Library stops offering it. A version anything uses is refused.",
          confirmLabel: "Archive",
          onConfirm: async () => {
            try {
              await call(false);
              notify({ level: "success", title: `${name} archived` });
            } catch (err) {
              notifyError("Archive failed", err);
            }
          },
        });
        return undefined;
      },
    },
  ];
  for (const c of list) commands.register(c);
}
