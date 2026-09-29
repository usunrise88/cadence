import type { AddPanelOptions, DockviewApi, DockviewGroupPanel, IDockviewPanel } from "dockview-react";
import { panels } from "@/shell/registries";
import { instanceIdFor, type Location, type PanelManifest, type PanelParams } from "@/shell/registry/panels";
import { dockApi, useDock } from "./store";

// Window operations on Dockview's public API. Commands, menus and the tab context menu call these; panels never do.

export type OpenOptions = { doc?: string; location?: Location; inactive?: boolean; floating?: { x: number; y: number; width: number; height: number } };

function need(): DockviewApi {
  const api = dockApi();
  if (!api) throw new Error("the layout is not ready");
  return api;
}

function manifestOf(p: IDockviewPanel): PanelManifest | undefined {
  const params = p.params as Partial<PanelParams> | undefined;
  return params?.panel ? panels.resolve(params.panel) : undefined;
}

/** A grid group already holding panels of this location (documents gather in the centre, tools at their edge). */
function groupFor(api: DockviewApi, location: Location): DockviewGroupPanel | undefined {
  for (const g of api.groups) {
    if (g.api.location.type !== "grid") continue;
    for (const p of g.panels) {
      const m = manifestOf(p);
      const loc = (p.params as { loc?: Location } | undefined)?.loc ?? m?.defaultLocation;
      if (loc === location) return g;
    }
  }
  return undefined;
}

function positionFor(api: DockviewApi, m: PanelManifest, location: Location): Partial<AddPanelOptions> {
  if (location === "floating") {
    const w = Math.min(m.defaultSize.w, Math.max(200, api.width - 80));
    const h = Math.min(m.defaultSize.h, Math.max(120, api.height - 80));
    return { floating: { width: w, height: h, x: Math.max(8, (api.width - w) / 2), y: Math.max(8, (api.height - h) / 3) } };
  }
  const same = groupFor(api, location);
  if (same) return { position: { referenceGroup: same, direction: "within" } };
  if (api.groups.length === 0) return {};
  if (location === "centre") {
    const tools = api.groups.find((g) => g.api.location.type === "grid");
    return tools ? { position: { referenceGroup: tools, direction: "right" } } : {};
  }
  const direction = location === "left" ? "left" : location === "right" ? "right" : "below";
  return {
    position: { direction },
    ...(location === "bottom" ? { initialHeight: m.defaultSize.h } : { initialWidth: m.defaultSize.w }),
  };
}

/** Opens a panel (or focuses it if it is already open) and returns its instance id. */
export function openPanel(panelId: string, opts: OpenOptions = {}): string {
  const api = need();
  const m = panels.resolve(panelId);
  if (!m) throw new Error(`unknown panel "${panelId}"`);
  const id = instanceIdFor(m, opts.doc);
  const existing = api.getPanel(id);
  if (existing) {
    if (!opts.inactive) existing.api.setActive();
    return id;
  }
  const location = opts.location ?? m.defaultLocation;
  const params: PanelParams & { loc: Location } = { panel: m.id, loc: location, ...(opts.doc ? { doc: opts.doc } : {}) };
  const pos = opts.floating ? { floating: opts.floating } : positionFor(api, m, location);
  api.addPanel({
    id,
    component: "panel",
    title: m.title,
    params,
    renderer: m.renderer ?? "onlyWhenVisible",
    inactive: opts.inactive,
    ...pos,
  } as AddPanelOptions);
  return id;
}

export function closePanel(instanceId: string): void {
  dockApi()?.getPanel(instanceId)?.api.close();
}

export function activePanel(): IDockviewPanel | undefined {
  return dockApi()?.activePanel;
}

export function activeGroup(): DockviewGroupPanel | undefined {
  return dockApi()?.activeGroup;
}

export function floatPanel(instanceId?: string): void {
  const api = need();
  const p = instanceId ? api.getPanel(instanceId) : api.activePanel;
  if (!p) return;
  if (p.group.api.location.type === "floating") return;
  const m = manifestOf(p);
  const w = m?.defaultSize.w ?? 480;
  const h = m?.defaultSize.h ?? 320;
  api.addFloatingGroup(p, { width: w, height: h, x: Math.max(8, (api.width - w) / 2), y: Math.max(8, (api.height - h) / 3) });
}

/** Moves the active group (floating, popout or grid) to an edge of the grid. */
export function dockGroup(direction: "left" | "right" | "top" | "bottom", group: DockviewGroupPanel | undefined = activeGroup()): void {
  if (!group) return;
  group.api.moveTo({ position: direction });
}

export async function popoutGroup(group: DockviewGroupPanel | undefined = activeGroup()): Promise<boolean> {
  const api = need();
  if (!group || group.api.location.type === "popout") return false;
  const bb = group.element.getBoundingClientRect();
  return api.addPopoutGroup(group, {
    popoutUrl: "/popout.html",
    position: { left: window.screenX + bb.left, top: window.screenY + bb.top, width: Math.max(bb.width, 480), height: Math.max(bb.height, 320) },
  });
}

/** Returns a popout group to the grid (closing its window does the same). */
export function returnGroup(group: DockviewGroupPanel | undefined = activeGroup()): void {
  if (!group || group.api.location.type !== "popout") return;
  group.api.moveTo({ position: "right" });
}

export function canMaximize(group: DockviewGroupPanel | undefined = activeGroup()): true | string {
  if (!group) return "No active group";
  if (group.api.location.type === "floating") return "Floating windows cannot be maximized (Dockview limitation); dock it first";
  if (group.api.location.type === "popout") return "Popout windows are sized by the operating system";
  return true;
}

export function toggleMaximize(group: DockviewGroupPanel | undefined = activeGroup()): void {
  if (!group || canMaximize(group) !== true) return;
  if (group.api.isMaximized()) group.api.exitMaximized();
  else group.api.maximize();
}

function cycle<T>(items: T[], current: T | undefined, dir: 1 | -1): T | undefined {
  if (items.length === 0) return undefined;
  const i = current === undefined ? -1 : items.indexOf(current);
  return items[(i + dir + items.length) % items.length];
}

/** F6 / Shift+F6: next or previous group, floating windows included. */
export function focusGroup(dir: 1 | -1): void {
  const api = need();
  const groups = api.groups.filter((g) => g.api.location.type !== "popout" && g.panels.length > 0);
  const next = cycle(groups, api.activeGroup, dir);
  next?.activePanel?.api.setActive();
  next?.activePanel?.view.content.element.focus?.();
}

/** Ctrl/Cmd+Alt+] / [: next or previous tab in the active group. */
export function focusTab(dir: 1 | -1): void {
  const g = activeGroup();
  if (!g) return;
  cycle(g.panels, g.activePanel, dir)?.api.setActive();
}

/** Ctrl/Cmd+\: hide or show every tool panel (documents stay). */
export function toggleToolPanels(): void {
  const api = need();
  const { hiddenTools, setHiddenTools } = useDock.getState();
  if (hiddenTools) {
    for (const panelId of hiddenTools) openPanel(panelId, { inactive: true });
    setHiddenTools(null);
    return;
  }
  const hidden: string[] = [];
  for (const p of [...api.panels]) {
    const m = manifestOf(p);
    if (m?.kind === "tool") {
      hidden.push(m.id);
      p.api.close();
    }
  }
  setHiddenTools(hidden);
}

export function panelManifestOf(instanceId: string): PanelManifest | undefined {
  const p = dockApi()?.getPanel(instanceId);
  return p ? manifestOf(p) : undefined;
}
