import { Archive, Compress, EditPencil, Expand, HalfMoon, HelpCircle, KeyCommand as Keyboard, OpenNewWindow, Plus, Search, ViewGrid } from "iconoir-react";
import { commandHeaders } from "@/api/client";
import { projectsArchive } from "@/api/gen/sdk.gen";
import { useDialogs } from "@/shell/chrome/dialogs";
import {
  activeGroup,
  canMaximize,
  closePanel,
  dockGroup,
  floatPanel,
  focusGroup,
  focusTab,
  openPanel,
  popoutGroup,
  returnGroup,
  toggleMaximize,
  toggleToolPanels,
} from "@/shell/dock/layout";
import { dockApi } from "@/shell/dock/store";
import { alignFloat, isFloating, useSnap } from "@/shell/floating-snap/dockview-adapter";
import { useHelp } from "@/shell/help/store";
import { notify, notifyError } from "@/shell/notifications/store";
import { commands, panels } from "@/shell/registries";
import { useShell } from "@/shell/state";
import { useTheme } from "@/shell/theme/store";
import { DEFAULT_WORKSPACES } from "@/shell/workspaces/schema";
import { applyPlan, defaultPlan, restoreWorkspace, saveWorkspace } from "@/shell/workspaces/persistence";
import type { Command } from "./registry";

// Built-in commands. Window and view commands are client-only (`view.*`); project and workspace commands call
// exactly one API operation each and carry its operationId as their id.

type Nav = { toWorkspace(project: string, workspace: string): void };
let nav: Nav = { toWorkspace: () => {} };
/** The router hands the shell a navigator so commands can switch workspace. */
export function setNavigator(n: Nav): void {
  nav = n;
}

const needProject = (ctx: { project?: string }): true | string => (ctx.project ? true : "Open a project first");
const needFloat = (): true | string => (isFloating(activeGroup()) ? true : "The active window is not floating");
const needGroup = (): true | string => (activeGroup() ? true : "No active window");

function pascal(id: string): string {
  return id.replace(/(^|-)([a-z0-9])/g, (_m, _d, c: string) => c.toUpperCase());
}

export function registerBuiltinCommands(): void {
  const list: Command[] = [
    // ---- palette, help
    { id: "view.palette", title: "Command palette", group: "Go", icon: Search, keys: ["Mod+K"], allowInInput: true, run: () => useDialogs.getState().show({ kind: "palette", prefix: "" }) },
    { id: "view.paletteCommands", title: "Run a command…", group: "Go", hidden: true, run: () => useDialogs.getState().show({ kind: "palette", prefix: ">" }) },
    { id: "view.switchProject", title: "Switch project…", group: "Project", keys: ["Mod+Alt+P"], allowInInput: true, run: () => useDialogs.getState().show({ kind: "palette", prefix: "@" }) },
    { id: "view.searchHelp", title: "Search help…", group: "Help", icon: HelpCircle, run: () => useDialogs.getState().show({ kind: "palette", prefix: "?" }) },
    {
      id: "view.helpForThis",
      title: "Help for this panel",
      group: "Help",
      keys: ["Shift+?"],
      run: () => {
        useHelp.getState().follow();
        openPanel("help");
      },
    },
    { id: "view.shortcuts", title: "Keyboard shortcuts", group: "Help", icon: Keyboard, keys: ["Mod+/"], allowInInput: true, run: () => useDialogs.getState().show({ kind: "shortcuts" }) },

    // ---- windows (docs/spec/10-ui-shell.md "Window states" and "Keyboard map")
    { id: "view.float", title: "Float panel", group: "Window", enabled: () => (isFloating(activeGroup()) ? "Already floating" : needGroup()), run: () => floatPanel() },
    { id: "view.popout", title: "Pop out group", group: "Window", icon: OpenNewWindow, enabled: needGroup, run: () => void popoutGroup() },
    { id: "view.returnToGrid", title: "Return to grid", group: "Window", enabled: () => (activeGroup()?.api.location.type === "popout" ? true : "The active window is not a popout"), run: () => returnGroup() },
    { id: "view.toggleMaximize", title: "Toggle maximize", group: "Window", icon: Expand, enabled: () => canMaximize(), run: () => toggleMaximize() },
    { id: "view.dockLeft", title: "Dock left", group: "Window", enabled: needGroup, run: () => dockGroup("left") },
    { id: "view.dockRight", title: "Dock right", group: "Window", enabled: needGroup, run: () => dockGroup("right") },
    { id: "view.dockTop", title: "Dock top", group: "Window", enabled: needGroup, run: () => dockGroup("top") },
    { id: "view.dockBottom", title: "Dock bottom", group: "Window", enabled: needGroup, run: () => dockGroup("bottom") },
    { id: "view.closePanel", title: "Close panel", group: "Window", keys: ["Alt+W"], enabled: (ctx) => (ctx.focusedPanel ? true : "No active panel"), run: (ctx) => ctx.focusedPanel && closePanel(ctx.focusedPanel) },
    { id: "view.nextGroup", title: "Next group", group: "Window", keys: ["F6"], run: () => focusGroup(1) },
    { id: "view.previousGroup", title: "Previous group", group: "Window", keys: ["Shift+F6"], run: () => focusGroup(-1) },
    { id: "view.nextTab", title: "Next tab in group", group: "Window", keys: ["Mod+Alt+]"], run: () => focusTab(1) },
    { id: "view.previousTab", title: "Previous tab in group", group: "Window", keys: ["Mod+Alt+["], run: () => focusTab(-1) },
    { id: "view.toggleToolPanels", title: "Hide or show all tool panels", group: "View", keys: ["Mod+\\"], run: () => toggleToolPanels() },
    { id: "view.alignLeft", title: "Align floating window left", group: "Window", enabled: needFloat, run: () => alignFloatActive("left") },
    { id: "view.alignRight", title: "Align floating window right", group: "Window", enabled: needFloat, run: () => alignFloatActive("right") },
    { id: "view.alignTop", title: "Align floating window top", group: "Window", enabled: needFloat, run: () => alignFloatActive("top") },
    { id: "view.alignBottom", title: "Align floating window bottom", group: "Window", enabled: needFloat, run: () => alignFloatActive("bottom") },
    {
      id: "view.toggleSnap",
      title: "Toggle snapping",
      group: "View",
      icon: Compress,
      keys: ["Mod+Shift+;"],
      run: () => {
        const s = useSnap.getState();
        s.setEnabled(!s.enabled);
        notify({ level: "info", title: `Snapping ${s.enabled ? "off" : "on"}` });
      },
    },
    {
      id: "view.toggleTheme",
      title: "Toggle dark mode",
      group: "View",
      icon: HalfMoon,
      run: () => {
        const t = useTheme.getState();
        t.setMode(t.dark ? "light" : "dark");
      },
    },

    // ---- workspaces
    {
      id: "workspaces.set",
      operation: "workspaces.set",
      title: "Save workspace",
      group: "Workspace",
      enabled: needProject,
      run: async () => {
        const api = dockApi();
        const { project, workspace } = useShell.getState();
        if (api && project && workspace) await saveWorkspace(api, project, workspace, { force: true });
      },
    },
    {
      id: "view.resetWorkspace",
      title: "Reset workspace to default",
      group: "Workspace",
      icon: ViewGrid,
      enabled: needProject,
      run: () => {
        const api = dockApi();
        const { project, workspace } = useShell.getState();
        if (api && project && workspace) applyPlan(api, defaultPlan(workspace, project));
      },
    },
    {
      id: "view.reloadWorkspace",
      title: "Reload workspace",
      group: "Workspace",
      hidden: true,
      run: () => {
        const api = dockApi();
        const { project, workspace } = useShell.getState();
        if (api && project && workspace) void restoreWorkspace(api, project, workspace);
      },
    },
    ...DEFAULT_WORKSPACES.map(
      (name, i): Command => ({
        id: `view.workspace${name}`,
        title: `Switch to ${name} workspace`,
        group: "Workspace",
        keys: [`Mod+Shift+${i + 1}`],
        enabled: needProject,
        run: (ctx) => ctx.project && nav.toWorkspace(ctx.project, name),
      }),
    ),

    // ---- projects
    { id: "projects.new", operation: "projects.new", title: "New project…", group: "Project", icon: Plus, run: () => useDialogs.getState().show({ kind: "newProject" }) },
    {
      id: "projects.edit",
      operation: "projects.edit",
      title: "Rename project…",
      group: "Project",
      icon: EditPencil,
      enabled: needProject,
      run: (ctx) => ctx.project && useDialogs.getState().show({ kind: "editProject", slug: ctx.project }),
    },
    {
      id: "projects.archive",
      operation: "projects.archive",
      title: "Archive project…",
      group: "Project",
      icon: Archive,
      enabled: needProject,
      run: (ctx, args) => {
        const slug = ctx.project;
        const rev = (args as { entity?: { rev?: number } } | undefined)?.entity?.rev;
        if (!slug) return;
        useDialogs.getState().show({
          kind: "confirm",
          title: `Archive project “${slug}”?`,
          detail: "Archiving is reversible; the project disappears from the switcher until restored.",
          confirmLabel: "Archive",
          onConfirm: async () => {
            try {
              await projectsArchive({ path: { p: slug }, headers: commandHeaders(rev) });
              notify({ level: "success", title: `Project “${slug}” archived` });
            } catch (err) {
              notifyError("Archive failed", err);
            }
          },
        });
      },
    },
  ];
  for (const c of list) commands.register(c);

  // One "Open <panel>" command per registered tool panel.
  for (const m of panels.all()) {
    if (m.kind !== "tool") continue;
    commands.register({ id: `view.open${pascal(m.id)}`, title: `Open ${m.title}`, group: "View", icon: m.icon, run: () => openPanel(m.id) });
  }
}

function alignFloatActive(edge: "left" | "right" | "top" | "bottom"): void {
  const api = dockApi();
  const g = activeGroup();
  if (api && g) alignFloat(api, g, edge);
}
