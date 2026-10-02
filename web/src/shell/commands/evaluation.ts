import { BookmarkBook, EditPencil, Filter, Lock, Play } from "iconoir-react";
import { commandHeaders, commandHeadersAt } from "@/api/client";
import { boostEdit, evalsGate, evalsGet, evalsNew, goldenSetsFreeze, langpacksEdit, modelsRegister } from "@/api/gen/sdk.gen";
import type {
  ApprovalAccepted,
  BoostEdit,
  Eval,
  EvalNew,
  EvalPlan,
  GoldenSetFreeze,
  GoldenSetVersion,
  LanguagePack,
  LanguagePackEdit,
  ModelRegister,
  ModelRegistration,
  ModelVersion,
} from "@/api/gen/types.gen";
import { docRef, parseDocRef, type EntityData } from "@/shell/entity/manifest";
import { useEditRequests } from "@/shell/entity/edits";
import { openDocument } from "@/shell/panel/actions";
import { notify, notifyError } from "@/shell/notifications/store";
import { commands } from "@/shell/registries";
import type { Command, CommandContext } from "./registry";

// Commands behind the phase-3 panels (Eval report, Golden set, Language pack, Checkpoints' Evaluate): each is
// exactly one API operation. Without the arguments a form supplies (the palette, a document header, the next-step
// bar) a command opens the document whose form asks for them, like runs.stage does.

/** evals.new: a dry run answers the plan (cells, cached, estimate); a real one the eval, or an approval. */
export type EvalNewArgs = { project: string; body: EvalNew; dryRun?: boolean };
/** evals.gate: the eval's revision is the If-Match; from the header `entity` stands in for it. */
export type EvalGateArgs = { eval?: Pick<Eval, "id" | "rev">; entity?: EntityData; dryRun?: boolean };
/** models.register: without a body the eval's register form opens (`entity` is the eval). */
export type ModelRegisterArgs = { project?: string; body?: ModelRegister; dryRun?: boolean; entity?: EntityData };
export type GoldenSetFreezeArgs = { body?: GoldenSetFreeze; dryRun?: boolean; entity?: EntityData };
/** langpacks.edit and boost.edit: `sha` is the pack's commit (LanguagePack.sha), sent as If-Match. */
export type LangpackEditArgs = { project?: string; locale?: string; sha?: string; body?: LanguagePackEdit; dryRun?: boolean; entity?: EntityData };
export type BoostEditArgs = { project: string; locale: string; domain: string; sha: string; body: BoostEdit; dryRun?: boolean };

export type EvaluationCommands = {
  "evals.new": { args: EvalNewArgs; result: EvalPlan | Eval | ApprovalAccepted };
  "evals.gate": { args: EvalGateArgs; result: Eval | undefined };
  "models.register": { args: ModelRegisterArgs; result: ModelRegistration | ModelVersion | ApprovalAccepted | undefined };
  "goldenSets.freeze": { args: GoldenSetFreezeArgs; result: GoldenSetVersion | ApprovalAccepted | undefined };
  "langpacks.edit": { args: LangpackEditArgs; result: LanguagePack | undefined };
  "boost.edit": { args: BoostEditArgs; result: LanguagePack };
};

function need<T>(args: unknown, what: string): T {
  if (!args) throw new Error(`${what}: run it from its panel`);
  return args as T;
}

/** The id of the active document when it is of `kind`. */
function activeOf(ctx: CommandContext, kind: string): string | undefined {
  const d = ctx.activeDoc ? parseDocRef(ctx.activeDoc) : undefined;
  return d?.kind === kind ? d.id : undefined;
}

const needActive = (kind: string, what: string) => (ctx: CommandContext) => (activeOf(ctx, kind) ? true : `Open ${what} first`);

/** Opens the document and asks its form to show (useEditRequest in the panel). */
function requestForm(kind: string, id: string): undefined {
  const doc = docRef(kind, id);
  openDocument(doc);
  useEditRequests.getState().request(doc);
  return undefined;
}

export function registerEvaluationCommands(): void {
  const list: Command[] = [
    {
      id: "evals.new",
      operation: "evals.new",
      title: "Run eval matrix",
      group: "Project",
      icon: Play,
      hidden: true,
      run: async (_ctx, args) => {
        const a = need<EvalNewArgs>(args, "Run eval matrix");
        const { data } = await evalsNew({ path: { p: a.project }, body: a.body, query: a.dryRun ? { dryRun: true } : undefined, headers: commandHeaders(), throwOnError: true });
        return data;
      },
    },
    {
      id: "evals.gate",
      operation: "evals.gate",
      title: "Run the gate",
      group: "Edit",
      icon: Filter,
      enabled: needActive("eval", "an Eval report"),
      run: async (ctx, args) => {
        const a = (args ?? {}) as EvalGateArgs;
        let target = a.eval ?? (a.entity ? { id: a.entity.id, rev: a.entity.rev ?? 0 } : undefined);
        try {
          if (!target) {
            const id = activeOf(ctx, "eval");
            if (!id) throw new Error("Run the gate: open an Eval report first");
            const { data } = await evalsGet({ path: { id }, throwOnError: true });
            target = { id, rev: data.rev };
          }
          const { data } = await evalsGate({ path: { id: target.id }, query: a.dryRun ? { dryRun: true } : undefined, headers: commandHeaders(target.rev), throwOnError: true });
          if (!a.eval && data.gate) {
            notify({
              level: data.gate.verdict === "passed" ? "success" : "warning",
              title: `Gate ${data.gate.verdict}`,
              detail: `${data.gate.checks.filter((c) => c.state === "passed").length} of ${data.gate.checks.length} checks passed`,
            });
          }
          return data;
        } catch (err) {
          // From the header or the palette nobody awaits the result: say why here (a running eval, a bad gates.yaml).
          if (!a.eval) {
            notifyError("Run the gate failed", err);
            return undefined;
          }
          throw err;
        }
      },
    },
    {
      id: "models.register",
      operation: "models.register",
      title: "Register model version",
      group: "Edit",
      icon: BookmarkBook,
      enabled: needActive("eval", "an Eval report whose gate passed"),
      run: async (ctx, args) => {
        const a = (args ?? {}) as ModelRegisterArgs;
        if (!a.body) {
          const id = a.entity?.id ?? activeOf(ctx, "eval");
          if (!id) throw new Error("Register model version: open an Eval report first");
          return requestForm("eval", id);
        }
        const project = a.project ?? ctx.project;
        if (!project) throw new Error("Register model version: open a project first");
        const { data } = await modelsRegister({ path: { p: project }, body: a.body, query: a.dryRun ? { dryRun: true } : undefined, headers: commandHeaders(), throwOnError: true });
        return data;
      },
    },
    {
      id: "goldenSets.freeze",
      operation: "goldenSets.freeze",
      title: "Freeze golden set…",
      group: "Edit",
      icon: Lock,
      enabled: needActive("golden_set", "a golden set"),
      run: async (ctx, args) => {
        const a = (args ?? {}) as GoldenSetFreezeArgs;
        if (!a.body) {
          const id = a.entity?.id ?? activeOf(ctx, "golden_set");
          if (!id) throw new Error("Freeze golden set: open a golden set first");
          return requestForm("golden_set", id);
        }
        const { data } = await goldenSetsFreeze({ body: a.body, query: a.dryRun ? { dryRun: true } : undefined, headers: commandHeaders(), throwOnError: true });
        return data;
      },
    },
    {
      id: "langpacks.edit",
      operation: "langpacks.edit",
      title: "Edit language pack",
      group: "Edit",
      icon: EditPencil,
      enabled: needActive("language_pack", "a language pack"),
      run: async (ctx, args) => {
        const a = (args ?? {}) as LangpackEditArgs;
        if (!a.body) {
          const locale = a.entity?.id ?? activeOf(ctx, "language_pack");
          if (!locale) throw new Error("Edit language pack: open a language pack first");
          return requestForm("language_pack", locale);
        }
        const project = a.project ?? ctx.project;
        if (!project || !a.locale || !a.sha) throw new Error("Edit language pack: run it from the Language pack document");
        const { data } = await langpacksEdit({
          path: { p: project, locale: a.locale },
          body: a.body,
          query: a.dryRun ? { dryRun: true } : undefined,
          headers: commandHeadersAt(a.sha),
          throwOnError: true,
        });
        return data;
      },
    },
    {
      id: "boost.edit",
      operation: "boost.edit",
      title: "Edit boost list",
      group: "Edit",
      icon: EditPencil,
      hidden: true,
      run: async (_ctx, args) => {
        const a = need<BoostEditArgs>(args, "Edit boost list");
        const { data } = await boostEdit({
          path: { p: a.project, locale: a.locale, domain: a.domain },
          body: a.body,
          query: a.dryRun ? { dryRun: true } : undefined,
          headers: commandHeadersAt(a.sha),
          throwOnError: true,
        });
        return data;
      },
    },
  ];
  for (const c of list) commands.register(c);
}
