import { CheckCircle, Key, Lock, Server, Shield, XmarkCircle } from "iconoir-react";
import { commandHeaders } from "@/api/client";
import { approvalsApprove, approvalsDeny, computeEdit, credentialsNew, credentialsRevoke, policiesEdit, secretsNew } from "@/api/gen/sdk.gen";
import type {
  Approval,
  Branch,
  BranchMerge,
  ComputeEdit,
  ComputeHost,
  Credential,
  CredentialCreated,
  CredentialNew,
  MixEditResult,
  Policies,
  PoliciesEdit,
  ProjectNote,
  ProjectSync,
  Secret,
  SavedView,
  SecretNewWritable,
} from "@/api/gen/types.gen";
import { useDialogs } from "@/shell/chrome/dialogs";
import { useFocusedApproval } from "@/shell/approvals/store";
import { commands } from "@/shell/registries";
import { commandContext } from "@/shell/state";
import type { MixEditArgs } from "./entities";
import type { BranchArgs, NoteArgs, ProfileEditArgs, ProfileEditResult, SyncArgs } from "./projects";
import type { Command } from "./registry";

// Commands behind the Approvals, Settings and Getting started panels (phase 1). Each mutating command is exactly
// one API operation and carries its operationId as id; panels run them through `runCommand` (the panel SDK), which
// types the arguments and the result. Commands that need arguments a palette cannot supply are hidden from it.

export type ApproveArgs = { approval: Approval; grant?: "once" | "session"; note?: string };
export type DenyArgs = { approval: Approval; note?: string };

/** Arguments and results of the API commands panels run. */
export type ApiCommands = {
  "approvals.approve": { args: ApproveArgs; result: Approval };
  "approvals.deny": { args: DenyArgs; result: Approval };
  "credentials.new": { args: { body: CredentialNew }; result: CredentialCreated };
  "credentials.revoke": { args: { credential: Credential }; result: Credential };
  "compute.edit": { args: { host: ComputeHost; body: ComputeEdit }; result: ComputeHost };
  "secrets.new": { args: { body: SecretNewWritable }; result: Secret };
  "policies.edit": { args: { policies: Policies; body: PoliciesEdit }; result: Policies };
  "view.twoFactor": { args: undefined; result: void };
  "views.set": { args: { name: string; query: string }; result: SavedView | undefined };
  "mixes.edit": { args: MixEditArgs; result: MixEditResult | undefined };
  "projects.note": { args: NoteArgs; result: ProjectNote | undefined };
  "projects.sync": { args: SyncArgs; result: ProjectSync | undefined };
  "agentProfile.edit": { args: ProfileEditArgs; result: ProfileEditResult | undefined };
  "branches.accept": { args: BranchArgs; result: BranchMerge | undefined };
  "branches.revert": { args: BranchArgs; result: Branch | undefined };
};
export type ApiCommandId = keyof ApiCommands;

/** Runs a registered command with typed arguments; rejects with the command's error (a ProblemError for the API). */
export function runCommand<K extends ApiCommandId>(id: K, args: ApiCommands[K]["args"]): Promise<ApiCommands[K]["result"]> {
  return commands.run(id, commandContext(), args) as Promise<ApiCommands[K]["result"]>;
}

function need<T>(args: unknown, what: string): T {
  if (!args) throw new Error(`${what}: run it from its panel`);
  return args as T;
}

/** The approval the palette acts on: the last focused pending approval card. */
function focusedApproval(): Approval | undefined {
  const a = useFocusedApproval.getState().approval;
  return a?.state === "pending" ? a : undefined;
}
const needFocusedApproval = (): true | string => (focusedApproval() ? true : "Focus a pending approval card first");

export function registerApiCommands(): void {
  const list: Command[] = [
    {
      id: "approvals.approve",
      operation: "approvals.approve",
      title: "Approve request",
      group: "Edit",
      icon: CheckCircle,
      enabled: needFocusedApproval,
      run: async (_ctx, args) => {
        const a = (args as ApproveArgs | undefined) ?? { approval: need<Approval>(focusedApproval(), "Approve") };
        const { data } = await approvalsApprove({
          path: { id: a.approval.id },
          body: { grant: a.grant ?? "once", ...(a.note ? { note: a.note } : {}) },
          headers: commandHeaders(a.approval.rev),
          throwOnError: true,
        });
        return data;
      },
    },
    {
      id: "approvals.deny",
      operation: "approvals.deny",
      title: "Deny request",
      group: "Edit",
      icon: XmarkCircle,
      enabled: needFocusedApproval,
      run: async (_ctx, args) => {
        const a = (args as DenyArgs | undefined) ?? { approval: need<Approval>(focusedApproval(), "Deny") };
        const { data } = await approvalsDeny({
          path: { id: a.approval.id },
          body: a.note ? { note: a.note } : {},
          headers: commandHeaders(a.approval.rev),
          throwOnError: true,
        });
        return data;
      },
    },
    {
      id: "credentials.new",
      operation: "credentials.new",
      title: "Create API key",
      group: "Edit",
      icon: Key,
      hidden: true,
      run: async (_ctx, args) => {
        const { body } = need<ApiCommands["credentials.new"]["args"]>(args, "Create API key");
        const { data } = await credentialsNew({ body, headers: commandHeaders(), throwOnError: true });
        return data;
      },
    },
    {
      id: "credentials.revoke",
      operation: "credentials.revoke",
      title: "Revoke credential",
      group: "Edit",
      hidden: true,
      run: async (_ctx, args) => {
        const { credential } = need<ApiCommands["credentials.revoke"]["args"]>(args, "Revoke credential");
        const { data } = await credentialsRevoke({ path: { id: credential.id }, headers: commandHeaders(credential.rev), throwOnError: true });
        return data;
      },
    },
    {
      id: "compute.edit",
      operation: "compute.edit",
      title: "Edit compute",
      group: "Edit",
      icon: Server,
      hidden: true,
      run: async (_ctx, args) => {
        const { host, body } = need<ApiCommands["compute.edit"]["args"]>(args, "Edit compute");
        const { data } = await computeEdit({ path: { id: host.id }, body, headers: commandHeaders(host.rev), throwOnError: true });
        return data;
      },
    },
    {
      id: "secrets.new",
      operation: "secrets.new",
      title: "Add secret",
      group: "Edit",
      icon: Lock,
      hidden: true,
      run: async (_ctx, args) => {
        const { body } = need<ApiCommands["secrets.new"]["args"]>(args, "Add secret");
        const { data } = await secretsNew({ body, headers: commandHeaders(), throwOnError: true });
        return data;
      },
    },
    {
      id: "policies.edit",
      operation: "policies.edit",
      title: "Edit policies",
      group: "Edit",
      icon: Shield,
      hidden: true,
      run: async (_ctx, args) => {
        const { policies, body } = need<ApiCommands["policies.edit"]["args"]>(args, "Edit policies");
        const { data } = await policiesEdit({ body, headers: commandHeaders(policies.rev), throwOnError: true });
        return data;
      },
    },
    // Two-factor sign-in is an auth operation (never a command, R1); the dialog is the chrome's.
    { id: "view.twoFactor", title: "Two-factor authentication…", group: "Go", icon: Lock, run: () => useDialogs.getState().show({ kind: "twoFactor" }) },
  ];
  for (const c of list) commands.register(c);
}
