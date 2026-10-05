import { ArrowUpCircle, DoubleCheck, Plus, ShareIos, ShieldCheck, Timer, Undo } from "iconoir-react";
import { commandHeaders } from "@/api/client";
import {
  deploymentsNew,
  deploymentsPromote,
  deploymentsRollback,
  modelsBenchmark,
  modelsExport,
  modelsParity,
  promotionsVerify,
} from "@/api/gen/sdk.gen";
import type {
  ApprovalAccepted,
  Deployment,
  DeploymentNew,
  DeploymentPromote,
  DeploymentPromotion,
  DeploymentRollback,
  ModelBenchmarkPlan,
  ModelBenchmarkRequest,
  ModelExportPlan,
  ModelExportRequest,
  ModelParityPlan,
  ModelParityRequest,
  PromotionRecord,
} from "@/api/gen/types.gen";
import { commands } from "@/shell/registries";
import type { Command } from "./registry";

// The Model document's deploy commands (phase 5 · stream D4): one API operation each. Exports, parity checks and
// benchmarks are GPU spend (a dry run answers the plan and estimate; a real call the plan with its pipeline run, or an
// approval); deployments.new creates a shadow; deployments.promote and deployments.rollback always answer an approval,
// for people too, which the confirm modal then decides (approvals.approve); promotions.verify confirms a delivery with
// the receipt the delivery script printed. Hidden from the palette: they need the document's selection.

export type ModelCheckArgs<B> = { project: string; body: B; dryRun?: boolean };
export type DeploymentNewArgs = { project: string; body: DeploymentNew; dryRun?: boolean };
/** deployments.promote and deployments.rollback: `deployment.rev` is the If-Match. */
export type PromoteArgs = { deployment: Deployment; body: DeploymentPromote; dryRun?: boolean };
export type RollbackArgs = { deployment: Deployment; body: DeploymentRollback; dryRun?: boolean };
/** promotions.verify: `rev` is the record's revision (1 while it is pending). */
export type VerifyArgs = { record: string; rev: number; receipt: string; dryRun?: boolean };

export type DeployCommands = {
  "models.export": { args: ModelCheckArgs<ModelExportRequest>; result: ModelExportPlan | ApprovalAccepted };
  "models.parity": { args: ModelCheckArgs<ModelParityRequest>; result: ModelParityPlan | ApprovalAccepted };
  "models.benchmark": { args: ModelCheckArgs<ModelBenchmarkRequest>; result: ModelBenchmarkPlan | ApprovalAccepted };
  "deployments.new": { args: DeploymentNewArgs; result: Deployment | ApprovalAccepted };
  "deployments.promote": { args: PromoteArgs; result: DeploymentPromotion | ApprovalAccepted };
  "deployments.rollback": { args: RollbackArgs; result: DeploymentPromotion | ApprovalAccepted };
  "promotions.verify": { args: VerifyArgs; result: PromotionRecord };
};

function need<T>(args: unknown, what: string): T {
  if (!args) throw new Error(`${what}: run it from the Model document`);
  return args as T;
}

const dry = (d?: boolean) => (d ? { dryRun: true } : undefined);

export function registerDeployCommands(): void {
  const list: Command[] = [
    {
      id: "models.export",
      operation: "models.export",
      title: "Export model version",
      group: "Edit",
      icon: ShareIos,
      hidden: true,
      run: async (_ctx, args) => {
        const a = need<DeployCommands["models.export"]["args"]>(args, "Export");
        const { data } = await modelsExport({ path: { p: a.project }, body: a.body, query: dry(a.dryRun), headers: commandHeaders(), throwOnError: true });
        return data as ModelExportPlan | ApprovalAccepted;
      },
    },
    {
      id: "models.parity",
      operation: "models.parity",
      title: "Check export parity",
      group: "Edit",
      icon: DoubleCheck,
      hidden: true,
      run: async (_ctx, args) => {
        const a = need<DeployCommands["models.parity"]["args"]>(args, "Parity check");
        const { data } = await modelsParity({ path: { p: a.project }, body: a.body, query: dry(a.dryRun), headers: commandHeaders(), throwOnError: true });
        return data as ModelParityPlan | ApprovalAccepted;
      },
    },
    {
      id: "models.benchmark",
      operation: "models.benchmark",
      title: "Benchmark export",
      group: "Edit",
      icon: Timer,
      hidden: true,
      run: async (_ctx, args) => {
        const a = need<DeployCommands["models.benchmark"]["args"]>(args, "Benchmark");
        const { data } = await modelsBenchmark({ path: { p: a.project }, body: a.body, query: dry(a.dryRun), headers: commandHeaders(), throwOnError: true });
        return data as ModelBenchmarkPlan | ApprovalAccepted;
      },
    },
    {
      id: "deployments.new",
      operation: "deployments.new",
      title: "Deploy to shadow",
      group: "Edit",
      icon: Plus,
      hidden: true,
      run: async (_ctx, args) => {
        const a = need<DeploymentNewArgs>(args, "Deploy to shadow");
        const { data } = await deploymentsNew({ path: { p: a.project }, body: a.body, query: dry(a.dryRun), headers: commandHeaders(), throwOnError: true });
        return data as Deployment | ApprovalAccepted;
      },
    },
    {
      id: "deployments.promote",
      operation: "deployments.promote",
      title: "Promote deployment",
      group: "Edit",
      icon: ArrowUpCircle,
      hidden: true,
      run: async (_ctx, args) => {
        const a = need<PromoteArgs>(args, "Promote");
        const { data } = await deploymentsPromote({
          path: { id: a.deployment.id },
          body: a.body,
          query: dry(a.dryRun),
          headers: commandHeaders(a.deployment.rev),
          throwOnError: true,
        });
        return data as DeploymentPromotion | ApprovalAccepted;
      },
    },
    {
      id: "deployments.rollback",
      operation: "deployments.rollback",
      title: "Roll back deployment",
      group: "Edit",
      icon: Undo,
      hidden: true,
      run: async (_ctx, args) => {
        const a = need<RollbackArgs>(args, "Roll back");
        const { data } = await deploymentsRollback({
          path: { id: a.deployment.id },
          body: a.body,
          query: dry(a.dryRun),
          headers: commandHeaders(a.deployment.rev),
          throwOnError: true,
        });
        return data as DeploymentPromotion | ApprovalAccepted;
      },
    },
    {
      id: "promotions.verify",
      operation: "promotions.verify",
      title: "Confirm delivery",
      group: "Edit",
      icon: ShieldCheck,
      hidden: true,
      run: async (_ctx, args) => {
        const a = need<VerifyArgs>(args, "Confirm delivery");
        const { data } = await promotionsVerify({
          path: { id: a.record },
          body: { receipt: a.receipt },
          query: dry(a.dryRun),
          headers: commandHeaders(a.rev),
          throwOnError: true,
        });
        return data as PromotionRecord;
      },
    },
  ];
  for (const c of list) commands.register(c);
}
