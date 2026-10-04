import { BookmarkBook, EditPencil, Filter, Import, Lock, Pin, Play } from "iconoir-react";
import { commandHeaders, commandHeadersAt } from "@/api/client";
import {
  aliasesGet,
  aliasesSet,
  boostEdit,
  evalsGate,
  evalsGet,
  evalsNew,
  gatesEdit,
  goldenSetsFreeze,
  langpacksEdit,
  modelsRegister,
  projectsAdopt,
  projectsGet,
} from "@/api/gen/sdk.gen";
import type {
  Adoption,
  Alias,
  ApprovalAccepted,
  BoostEdit,
  Eval,
  EvalNew,
  EvalPlan,
  Gates,
  GatesEdit,
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
import { REGISTER_REQUEST } from "./experiments";
import type { Command, CommandContext } from "./registry";

// Commands behind the phase-3 panels (Eval report, Golden set, Model, Language pack, Checkpoints' Evaluate, the
// Project home's gate): each is exactly one API operation. Without the arguments a form supplies (the palette, a
// document header, the next-step bar) a command opens the document whose form asks for them, like runs.stage does.

/** evals.new: a dry run answers the plan (cells, cached, estimate); a real one the eval, or an approval. */
export type EvalNewArgs = { project: string; body: EvalNew; dryRun?: boolean };
/** evals.new from the Eval report's header or the palette: the report's Run eval form opens. */
export type EvalFormArgs = { entity?: EntityData };
/**
 * projects.adopt: `version` (ver_…) is adopted into the project (If-Match the project's revision, read when not
 * given). From a golden set's or dataset version's header (only `entity`) its document shows its adopt card, which
 * dry-runs first so a leakage, licence or locale refusal shows before anything changes. `purpose` replay adopts data
 * of another language kept to measure forgetting (the locale is not checked).
 */
export type ProjectAdoptArgs = {
  project?: string;
  version?: string;
  rev?: number;
  dryRun?: boolean;
  purpose?: "target" | "replay";
  entity?: EntityData;
  /** The registry kind of `entity` (golden_set, dataset_version) when it is not the active document. */
  kind?: string;
};

/** Registry kinds whose documents show an adopt card. */
const ADOPT_DOCS = ["golden_set", "dataset_version"];
/**
 * aliases.set: points `name` (default baseline) at `version`, or at the open model / the header's entity. baseline is
 * gated, so the answer is an approval id. The alias's current revision is read for the If-Match.
 */
export type AliasSetArgs = { project?: string; name?: string; version?: string; dryRun?: boolean; entity?: EntityData };
/** gates.edit: `expect` is the ETag of gates.get (the commit that last changed gates.yaml, or "defaults"). */
export type GatesEditArgs = { project?: string; expect?: string; body?: GatesEdit; dryRun?: boolean };

/** Requests only an open document can complete (useEditRequest on `<prefix><doc>`). */
export const ADOPT_REQUEST = "adopt:";
export const EVAL_FORM_REQUEST = "evalnew:";
export const GATE_REQUEST = "gate:";
/** evals.gate: the eval's revision is the If-Match; from the header `entity` stands in for it. */
export type EvalGateArgs = { eval?: Pick<Eval, "id" | "rev">; entity?: EntityData; dryRun?: boolean };
/** models.register: without a body the eval's register form opens (`entity` is the eval). */
export type ModelRegisterArgs = { project?: string; body?: ModelRegister; dryRun?: boolean; entity?: EntityData };
export type GoldenSetFreezeArgs = { body?: GoldenSetFreeze; dryRun?: boolean; entity?: EntityData };
/** langpacks.edit and boost.edit: `sha` is the pack's commit (LanguagePack.sha), sent as If-Match. */
export type LangpackEditArgs = { project?: string; locale?: string; sha?: string; body?: LanguagePackEdit; dryRun?: boolean; entity?: EntityData };
export type BoostEditArgs = { project: string; locale: string; domain: string; sha: string; body: BoostEdit; dryRun?: boolean };

export type EvaluationCommands = {
  "evals.new": { args: EvalNewArgs | EvalFormArgs; result: EvalPlan | Eval | ApprovalAccepted };
  "projects.adopt": { args: ProjectAdoptArgs; result: Adoption | undefined };
  "aliases.set": { args: AliasSetArgs; result: Alias | ApprovalAccepted | undefined };
  "gates.edit": { args: GatesEditArgs; result: Gates | ApprovalAccepted | undefined };
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

const needProject = (ctx: CommandContext): true | string => (ctx.project ? true : "Open a project first");

/** Opens the document and asks its form to show (useEditRequest in the panel), under a prefix when it has several. */
function requestForm(kind: string, id: string, prefix = ""): undefined {
  const doc = docRef(kind, id);
  openDocument(doc);
  useEditRequests.getState().request(`${prefix}${doc}`);
  return undefined;
}

function projectOf(ctx: CommandContext, a: { project?: string }, what: string): string {
  const p = a.project ?? ctx.project;
  if (!p) throw new Error(`${what}: open a project first`);
  return p;
}

export function registerEvaluationCommands(): void {
  const list: Command[] = [
    {
      id: "evals.new",
      operation: "evals.new",
      title: "Run eval…",
      group: "Project",
      icon: Play,
      hidden: true,
      run: async (ctx, args) => {
        const a = (args ?? {}) as Partial<EvalNewArgs> & EvalFormArgs;
        if (!a.body || !a.project) {
          // The Eval report's header: its Run eval form, filled from the eval's axes. Checkpoints and the Experiment
          // document open the same form from their rows.
          const id = a.entity?.id.startsWith("evl_") ? a.entity.id : activeOf(ctx, "eval");
          if (!id) throw new Error("Run eval: open an Eval report, a run's Checkpoints or an Experiment first");
          return requestForm("eval", id, EVAL_FORM_REQUEST);
        }
        const { data } = await evalsNew({ path: { p: a.project }, body: a.body, query: a.dryRun ? { dryRun: true } : undefined, headers: commandHeaders(), throwOnError: true });
        return data;
      },
    },
    {
      id: "projects.adopt",
      operation: "projects.adopt",
      title: "Adopt into project",
      group: "Project",
      icon: Import,
      enabled: needProject,
      run: async (ctx, args) => {
        const a = (args ?? {}) as ProjectAdoptArgs;
        if (!a.version) {
          // The document of the header's entity (or of the active document) shows the adopt card.
          const active = ctx.activeDoc ? parseDocRef(ctx.activeDoc) : undefined;
          const activeKind = active && ADOPT_DOCS.includes(active.kind) && (!a.entity || active.id === a.entity.id) ? active.kind : undefined;
          const kind = a.kind ?? activeKind ?? "golden_set";
          const id = a.entity?.id ?? (active?.kind === kind ? active.id : undefined);
          if (!id) throw new Error("Adopt into project: open a golden set or dataset version first");
          return requestForm(kind, id, ADOPT_REQUEST);
        }
        const project = projectOf(ctx, a, "Adopt into project");
        const rev = a.rev ?? (await projectsGet({ path: { p: project }, throwOnError: true })).data.rev;
        const { data } = await projectsAdopt({
          path: { p: project },
          body: { version: a.version, ...(a.purpose ? { purpose: a.purpose } : {}) },
          query: a.dryRun ? { dryRun: true } : undefined,
          headers: commandHeaders(rev),
          throwOnError: true,
        });
        return data;
      },
    },
    {
      id: "aliases.set",
      operation: "aliases.set",
      title: "Set as baseline",
      group: "Project",
      icon: Pin,
      enabled: needProject,
      run: async (ctx, args) => {
        const a = (args ?? {}) as AliasSetArgs;
        // From a header or the palette nobody awaits the answer: say it here (the approval id, or why not).
        const quiet = !a.version;
        const name = a.name ?? "baseline";
        try {
          const project = projectOf(ctx, a, "Set as baseline");
          const version = a.version ?? (a.entity?.id.startsWith("ver_") ? a.entity.id : activeOf(ctx, "model"));
          if (!version) throw new Error("Set as baseline: open a model version first");
          const cur = await aliasesGet({ path: { p: project, name } }); // an error (404): the alias is not set yet
          const { data } = await aliasesSet({
            path: { p: project, name },
            body: { version },
            query: a.dryRun ? { dryRun: true } : undefined,
            headers: commandHeaders(cur.data?.rev),
            throwOnError: true,
          });
          if (quiet) {
            if ("approvalId" in data) notify({ level: "info", title: `@${name} waits for an approval`, detail: `Approval ${data.approvalId}: a person decides it in Approvals.` });
            else notify({ level: "success", title: `@${name} now points at ${data.version.name} ${data.version.version}` });
          }
          return data;
        } catch (err) {
          if (!quiet) throw err;
          notifyError(`Setting @${name} failed`, err);
          return undefined;
        }
      },
    },
    {
      id: "gates.edit",
      operation: "gates.edit",
      title: "Edit gates.yaml",
      group: "Project",
      icon: EditPencil,
      enabled: needProject,
      run: async (ctx, args) => {
        const a = (args ?? {}) as GatesEditArgs;
        const project = projectOf(ctx, a, "Edit gates.yaml");
        // Without a body: the Project home's gate editor (the effective gate, the file, Check then Commit).
        if (!a.body || !a.expect) return requestForm("project", project, GATE_REQUEST);
        const { data } = await gatesEdit({
          path: { p: project },
          body: a.body,
          query: a.dryRun ? { dryRun: true } : undefined,
          headers: commandHeadersAt(a.expect),
          throwOnError: true,
        });
        return data;
      },
    },
    {
      id: "evals.gate",
      operation: "evals.gate",
      title: "Run the gate",
      group: "Edit",
      icon: Filter,
      // A project, not an open report: Getting started gates the newest finished eval by id (args.entity).
      enabled: needProject,
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
      run: async (ctx, args) => {
        const a = (args ?? {}) as ModelRegisterArgs;
        if (!a.body) {
          // The Experiment header's Register best: its document confirms the best run's checkpoint.
          const exp = a.entity?.id.startsWith("exp_") ? a.entity.id : a.entity ? undefined : activeOf(ctx, "experiment");
          if (exp) {
            const doc = docRef("experiment", exp);
            openDocument(doc);
            useEditRequests.getState().request(`${REGISTER_REQUEST}${doc}`);
            return undefined;
          }
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
