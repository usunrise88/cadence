import { Archive, Bell, CheckCircle, Database, DatabaseRestore, Key, Lock, SendDiagonal, Server, Shield, ShieldCheck, XmarkCircle } from "iconoir-react";
import { commandHeaders } from "@/api/client";
import {
  agentCredentialsArchive,
  agentCredentialsSet,
  agentCredentialsVerify,
  approvalsApprove,
  approvalsDeny,
  backupsNew,
  backupsVerify,
  computeEdit,
  credentialsNew,
  credentialsRevoke,
  notificationRulesEdit,
  notificationSettingsEdit,
  policiesEdit,
  secretsNew,
  telegramBotSet,
  telegramBotVerify,
} from "@/api/gen/sdk.gen";
import type {
  AgentCredential,
  AgentCredentialSetWritable,
  Approval,
  Backup,
  Branch,
  BranchMerge,
  ComputeEdit,
  ComputeHost,
  Credential,
  CredentialCreated,
  CredentialNew,
  JobAccepted,
  MixEditResult,
  NotificationRule,
  NotificationRuleEdit,
  NotificationSettings,
  NotificationSettingsEdit,
  Policies,
  PoliciesEdit,
  ProjectNote,
  ProjectSync,
  Secret,
  SavedView,
  SecretNewWritable,
  TelegramBotVerify,
} from "@/api/gen/types.gen";
import { useDialogs } from "@/shell/chrome/dialogs";
import { useFocusedApproval } from "@/shell/approvals/store";
import { commands } from "@/shell/registries";
import { commandContext } from "@/shell/state";
import type { AgentMessage, AgentSession } from "@/api/gen/types.gen";
import type { MessageNewArgs, SessionArgs, SessionCancelArgs, SessionNewArgs } from "./agents";
import type { MixEditArgs } from "./entities";
import type { BranchArgs, NoteArgs, ProfileEditArgs, ProfileEditResult, SyncArgs } from "./projects";
import type { Command } from "./registry";
import type { TrainingCommands } from "./training";

// Commands behind the Approvals, Settings and Getting started panels (phase 1). Each mutating command is exactly
// one API operation and carries its operationId as id; panels run them through `runCommand` (the panel SDK), which
// types the arguments and the result. Commands that need arguments a palette cannot supply are hidden from it.

export type ApproveArgs = { approval: Approval; grant?: "once" | "session"; note?: string };
export type DenyArgs = { approval: Approval; note?: string };
/** agentCredentials.set: `credential` is the current one (its rev is the If-Match) when it exists and is not archived. */
export type AgentCredentialSetArgs = { id: string; credential?: AgentCredential; body: AgentCredentialSetWritable };

/** Arguments and results of the API commands panels run. */
export type ApiCommands = {
  "approvals.approve": { args: ApproveArgs; result: Approval };
  "approvals.deny": { args: DenyArgs; result: Approval };
  "agentCredentials.set": { args: AgentCredentialSetArgs; result: AgentCredential };
  "agentCredentials.verify": { args: { credential: AgentCredential }; result: AgentCredential };
  "agentCredentials.archive": { args: { credential: AgentCredential }; result: AgentCredential };
  "credentials.new": { args: { body: CredentialNew }; result: CredentialCreated };
  "credentials.revoke": { args: { credential: Credential }; result: Credential };
  "compute.edit": { args: { host: ComputeHost; body: ComputeEdit }; result: ComputeHost };
  "secrets.new": { args: { body: SecretNewWritable }; result: Secret };
  "policies.edit": { args: { policies: Policies; body: PoliciesEdit }; result: Policies };
  "notificationRules.edit": { args: { rule: NotificationRule; body: NotificationRuleEdit }; result: NotificationRule };
  "notificationSettings.edit": { args: { settings: NotificationSettings; body: NotificationSettingsEdit }; result: NotificationSettings };
  /** telegramBot.set: `settings` is the current settings (their rev is the If-Match once a token is stored). */
  "telegramBot.set": { args: { settings: NotificationSettings; token: string }; result: NotificationSettings };
  "telegramBot.verify": { args: undefined; result: TelegramBotVerify };
  "backups.new": { args: undefined; result: JobAccepted };
  "backups.verify": { args: { backup: Backup }; result: JobAccepted };
  "view.twoFactor": { args: undefined; result: void };
  "views.set": { args: { name: string; query: string }; result: SavedView | undefined };
  "mixes.edit": { args: MixEditArgs; result: MixEditResult | undefined };
  "projects.note": { args: NoteArgs; result: ProjectNote | undefined };
  "projects.sync": { args: SyncArgs; result: ProjectSync | undefined };
  "agentProfile.edit": { args: ProfileEditArgs; result: ProfileEditResult | undefined };
  "branches.accept": { args: BranchArgs; result: BranchMerge | undefined };
  "branches.revert": { args: BranchArgs; result: Branch | undefined };
  "agentSessions.new": { args: SessionNewArgs; result: AgentSession | undefined };
  "agentSessions.cancel": { args: SessionCancelArgs; result: AgentSession | undefined };
  "agentSessions.pause": { args: SessionArgs; result: AgentSession | undefined };
  "agentSessions.resume": { args: SessionArgs; result: AgentSession | undefined };
  "agentSessions.accept": { args: SessionArgs; result: AgentSession | undefined };
  "agentSessions.revert": { args: SessionArgs; result: AgentSession | undefined };
  "agentMessages.new": { args: MessageNewArgs; result: AgentMessage };
} & TrainingCommands;
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
    // Notifications and backups (Settings → Notifications, Backups): admin only; the bot token is write-only.
    {
      id: "notificationRules.edit",
      operation: "notificationRules.edit",
      title: "Edit notification rule",
      group: "Edit",
      icon: Bell,
      hidden: true,
      run: async (_ctx, args) => {
        const { rule, body } = need<ApiCommands["notificationRules.edit"]["args"]>(args, "Edit notification rule");
        const { data } = await notificationRulesEdit({ path: { id: rule.id }, body, headers: commandHeaders(rule.rev), throwOnError: true });
        return data;
      },
    },
    {
      id: "notificationSettings.edit",
      operation: "notificationSettings.edit",
      title: "Edit notification settings",
      group: "Edit",
      icon: Bell,
      hidden: true,
      run: async (_ctx, args) => {
        const { settings, body } = need<ApiCommands["notificationSettings.edit"]["args"]>(args, "Edit notification settings");
        const { data } = await notificationSettingsEdit({ body, headers: commandHeaders(settings.rev), throwOnError: true });
        return data;
      },
    },
    {
      id: "telegramBot.set",
      operation: "telegramBot.set",
      title: "Set Telegram bot token",
      group: "Edit",
      icon: Lock,
      hidden: true,
      run: async (_ctx, args) => {
        const { settings, token } = need<ApiCommands["telegramBot.set"]["args"]>(args, "Set Telegram bot token");
        const headers = commandHeaders(settings.telegram.tokenSet ? settings.rev : undefined);
        const { data } = await telegramBotSet({ body: { token }, headers, throwOnError: true });
        return data;
      },
    },
    {
      id: "telegramBot.verify",
      operation: "telegramBot.verify",
      title: "Send a Telegram test message",
      group: "Edit",
      icon: SendDiagonal,
      run: async () => {
        const { data } = await telegramBotVerify({ headers: commandHeaders(), throwOnError: true });
        return data;
      },
    },
    {
      id: "backups.new",
      operation: "backups.new",
      title: "Back up now",
      group: "Edit",
      icon: Database,
      run: async () => {
        const { data } = await backupsNew({ headers: commandHeaders(), throwOnError: true });
        return data as JobAccepted;
      },
    },
    {
      id: "backups.verify",
      operation: "backups.verify",
      title: "Run a restore test",
      group: "Edit",
      icon: DatabaseRestore,
      hidden: true,
      run: async (_ctx, args) => {
        const { backup } = need<ApiCommands["backups.verify"]["args"]>(args, "Run a restore test");
        const { data } = await backupsVerify({ path: { id: backup.id }, headers: commandHeaders(backup.rev), throwOnError: true });
        return data as JobAccepted;
      },
    },
    // The agents' model accounts (Settings → Agents): admin only, never MCP tools; the value is write-only.
    {
      id: "agentCredentials.set",
      operation: "agentCredentials.set",
      title: "Set agent credential",
      group: "Edit",
      icon: Key,
      hidden: true,
      run: async (_ctx, args) => {
        const { id, credential, body } = need<AgentCredentialSetArgs>(args, "Set agent credential");
        const live = credential && !credential.archivedAt ? credential : undefined;
        const { data } = await agentCredentialsSet({ path: { id }, body, headers: commandHeaders(live?.rev), throwOnError: true });
        return data;
      },
    },
    {
      id: "agentCredentials.verify",
      operation: "agentCredentials.verify",
      title: "Verify agent credential",
      group: "Edit",
      icon: ShieldCheck,
      hidden: true,
      run: async (_ctx, args) => {
        const { credential } = need<ApiCommands["agentCredentials.verify"]["args"]>(args, "Verify agent credential");
        const { data } = await agentCredentialsVerify({ path: { id: credential.id }, headers: commandHeaders(credential.rev), throwOnError: true });
        return data;
      },
    },
    {
      id: "agentCredentials.archive",
      operation: "agentCredentials.archive",
      title: "Disconnect agent credential",
      group: "Edit",
      icon: Archive,
      hidden: true,
      run: async (_ctx, args) => {
        const { credential } = need<ApiCommands["agentCredentials.archive"]["args"]>(args, "Disconnect agent credential");
        const { data } = await agentCredentialsArchive({ path: { id: credential.id }, headers: commandHeaders(credential.rev), throwOnError: true });
        return data;
      },
    },
    // Two-factor sign-in is an auth operation (never a command, R1); the dialog is the chrome's.
    { id: "view.twoFactor", title: "Two-factor authentication…", group: "Go", icon: Lock, run: () => useDialogs.getState().show({ kind: "twoFactor" }) },
  ];
  for (const c of list) commands.register(c);
}
