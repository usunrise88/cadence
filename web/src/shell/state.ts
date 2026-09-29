import { create } from "zustand";
import type { CommandContext } from "@/shell/commands/registry";
import { dockApi } from "@/shell/dock/store";
import { useSelection } from "@/shell/selection/store";

// Route-derived shell state: the current project and workspace (set by the /p/:project/w/:workspace route).
type ShellState = {
  project: string | undefined;
  workspace: string | undefined;
  setRoute(project: string | undefined, workspace: string | undefined): void;
};

export const useShell = create<ShellState>((set) => ({
  project: undefined,
  workspace: undefined,
  setRoute: (project, workspace) => set({ project, workspace }),
}));

export function commandContext(): CommandContext {
  return {
    project: useShell.getState().project,
    activeDoc: useSelection.getState().activeDoc,
    focusedPanel: dockApi()?.activePanel?.id,
  };
}
