import { FolderSettings, Notes, Page, RefreshDouble } from "iconoir-react";
import { commandHeaders, commandHeadersAt } from "@/api/client";
import { agentProfileEdit, branchesAccept, branchesRevert, projectsGet, projectsNote, projectsSync } from "@/api/gen/sdk.gen";
import type { AgentProfile, AgentProfileEdit, BranchMerge, Branch, ProjectNote, ProjectSync } from "@/api/gen/types.gen";
import { useDialogs } from "@/shell/chrome/dialogs";
import { openPanel } from "@/shell/dock/layout";
import { docRef } from "@/shell/entity/manifest";
import { notify, notifyError } from "@/shell/notifications/store";
import { commands } from "@/shell/registries";
import type { Command, CommandContext } from "./registry";

// Project repository commands (phase 1 · Projects): notes, template sync, the agent profile and draft branches.
// Each API-backed command calls exactly one operation. Panels pass what the user entered as arguments; run from a
// document header (args = { entity }) they open the matching dialog or panel instead, and report errors as notices.

const needProject = (ctx: CommandContext): true | string => (ctx.project ? true : "Open a project first");

type EntityArgs = { entity?: { rev?: number } };

async function projectRev(slug: string, args: EntityArgs | undefined): Promise<number | undefined> {
  const rev = args?.entity?.rev;
  if (rev !== undefined) return rev;
  const { data } = await projectsGet({ path: { p: slug } });
  return data?.rev;
}

export type NoteArgs = EntityArgs & { project?: string; text?: string; rev?: number };
export type SyncArgs = EntityArgs & { project?: string; rev?: number };
export type ProfileEditArgs = { project?: string; rev: number; body: AgentProfileEdit };
/** What agentProfile.edit answers: the profile, or the approval an agent's change waits for. */
export type ProfileEditResult = { profile?: AgentProfile; approvalId?: string };
export type BranchArgs = { project?: string; name: string; head: string };

export function registerProjectCommands(): void {
  const list: Command[] = [
    {
      id: "projects.note",
      operation: "projects.note",
      title: "Add a note…",
      group: "Project",
      icon: Notes,
      enabled: needProject,
      run: async (ctx, raw): Promise<ProjectNote | undefined> => {
        const args = raw as NoteArgs | undefined;
        const slug = args?.project ?? ctx.project;
        if (!slug) return undefined;
        if (!args?.text) {
          useDialogs.getState().show({ kind: "projectNote", slug, rev: args?.entity?.rev });
          return undefined;
        }
        const rev = args.rev ?? (await projectRev(slug, args));
        const { data } = await projectsNote({ path: { p: slug }, body: { text: args.text }, headers: commandHeaders(rev) });
        notify({ level: "success", title: "Note committed to NOTES.md" });
        return data;
      },
    },
    {
      id: "projects.sync",
      operation: "projects.sync",
      title: "Sync templates",
      group: "Project",
      icon: RefreshDouble,
      enabled: needProject,
      run: async (ctx, raw): Promise<ProjectSync | undefined> => {
        const args = raw as SyncArgs | undefined;
        const slug = args?.project ?? ctx.project;
        if (!slug) return undefined;
        const fromPanel = args?.rev !== undefined;
        try {
          const rev = args?.rev ?? (await projectRev(slug, args));
          const { data } = await projectsSync({ path: { p: slug }, headers: commandHeaders(rev) });
          if (data?.upToDate) notify({ level: "info", title: "Templates and skills are up to date" });
          else if (data?.branch) notify({ level: "success", title: `Draft branch ${data.branch}: ${data.changes.length} file(s) to review in Recipe` });
          return data;
        } catch (err) {
          if (fromPanel) throw err;
          notifyError("Sync failed", err);
          return undefined;
        }
      },
    },
    {
      id: "agentProfile.edit",
      operation: "agentProfile.edit",
      title: "Edit agent settings",
      group: "Project",
      icon: FolderSettings,
      enabled: needProject,
      run: async (ctx, raw): Promise<ProfileEditResult | undefined> => {
        const args = raw as ProfileEditArgs | undefined;
        const slug = args?.project ?? ctx.project;
        if (!slug) return undefined;
        if (!args?.body) {
          openPanel("agent-settings");
          return undefined;
        }
        const res = await agentProfileEdit({ path: { p: slug }, body: args.body, headers: commandHeaders(args.rev) });
        if (res.response?.status === 202) {
          const approvalId = (res.data as unknown as { approvalId?: string }).approvalId;
          notify({ level: "info", title: "The change waits for an approval" });
          return { approvalId };
        }
        notify({ level: "success", title: "Agent settings committed to the repository" });
        return { profile: res.data as AgentProfile };
      },
    },
    {
      id: "branches.accept",
      operation: "branches.accept",
      title: "Accept branch into main",
      group: "Project",
      hidden: true,
      enabled: needProject,
      run: async (ctx, raw): Promise<BranchMerge | undefined> => {
        const args = raw as BranchArgs | undefined;
        const slug = args?.project ?? ctx.project;
        if (!slug || !args) return undefined;
        const { data } = await branchesAccept({ path: { p: slug, name: args.name }, headers: commandHeadersAt(args.head) });
        notify({ level: "success", title: `${args.name} merged into main${data?.fastForward ? " (fast-forward)" : ""}` });
        return data;
      },
    },
    {
      id: "branches.revert",
      operation: "branches.revert",
      title: "Discard branch",
      group: "Project",
      hidden: true,
      enabled: needProject,
      run: async (ctx, raw): Promise<Branch | undefined> => {
        const args = raw as BranchArgs | undefined;
        const slug = args?.project ?? ctx.project;
        if (!slug || !args) return undefined;
        const { data } = await branchesRevert({ path: { p: slug, name: args.name }, headers: commandHeadersAt(args.head) });
        notify({ level: "success", title: `${args.name} discarded` });
        return data;
      },
    },
    {
      id: "view.openRepository",
      title: "Open repository files (Recipe)",
      group: "Project",
      icon: Page,
      enabled: needProject,
      run: () => openPanel("recipe", { doc: docRef("recipe", "project.yaml") }),
    },
  ];
  for (const c of list) commands.register(c);
}
