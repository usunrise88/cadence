import { PLACEHOLDER_PANEL, type PanelParams, type PanelRegistry } from "@/shell/registry/panels";

// docs/spec/10-ui-shell.md "Persistence": a workspace is versioned data that survives panel renames and Dockview
// upgrades. The layout is Dockview's own serialization; everything else is ours.

export const WORKSPACE_SCHEMA_VERSION = 3;
export const DEFAULT_WORKSPACES = ["Training", "Eval", "Data", "Triage", "Ops"] as const;
export type DefaultWorkspaceName = (typeof DEFAULT_WORKSPACES)[number];

/** Dockview's SerializedDockview, kept opaque except for the parts we migrate. */
export type SerializedLayout = {
  grid?: unknown;
  panels?: Record<string, SerializedPanel>;
  activeGroup?: string;
  floatingGroups?: unknown[];
  popoutGroups?: unknown[];
  [key: string]: unknown;
};
export type SerializedPanel = {
  id: string;
  contentComponent?: string;
  params?: Partial<PanelParams> & Record<string, unknown>;
  title?: string;
  [key: string]: unknown;
};

export type PanelState = { pinnedTo?: string; viewState?: Record<string, unknown> };

export type WorkspaceData = {
  schemaVersion: number;
  name: string;
  layout: SerializedLayout;
  panels: Record<string, PanelState>;
};

type Migration = (w: WorkspaceData) => WorkspaceData;

type GridNode = { type: "branch"; data: GridNode[]; [k: string]: unknown } | { type: "leaf"; data: { views: string[]; activeView?: string; id: string }; [k: string]: unknown };

/**
 * Schema 2: Chat sits in the right column of every workspace (docs/spec/11-ui-panels.md "Default workspaces"). Layouts
 * saved before the Chat panel existed get it as an inactive tab of their right-column group; a layout that already
 * has a Chat, has no right-column group, or is a bootstrap placeholder stays as it is.
 */
export function addChatToRightColumn(layout: SerializedLayout): SerializedLayout {
  const panels = layout.panels ?? {};
  if (isPlaceholderLayout(layout) || Object.values(panels).some((p) => p.params?.panel === "chat")) return layout;
  const right = new Set(Object.entries(panels).filter(([, p]) => p.params?.loc === "right").map(([id]) => id));
  if (right.size === 0) return layout;
  let done = false;
  const visit = (n: GridNode): GridNode => {
    if (done) return n;
    if (n.type === "leaf") {
      if (!n.data.views.some((v) => right.has(v))) return n;
      done = true;
      return { ...n, data: { ...n.data, views: [...n.data.views, "chat"] } };
    }
    return { ...n, data: n.data.map(visit) };
  };
  const grid = layout.grid as { root: GridNode; [k: string]: unknown };
  const root = visit(grid.root);
  if (!done) return layout;
  return {
    ...layout,
    grid: { ...grid, root },
    panels: { ...panels, chat: { id: "chat", contentComponent: "panel", title: "Chat", params: { panel: "chat", loc: "right" } } },
  };
}

/** The panels a default workspace keeps in a floating slot that opens on first use, not when the workspace is built. */
export const ON_DEMAND_FLOATS = ["audio"] as const;

type FloatingGroup = { data?: { views: string[]; activeView?: string; id: string; [k: string]: unknown }; [k: string]: unknown };

/**
 * Schema 3: a default workspace no longer opens its floating Audio when it is built (docs/spec/11-ui-panels.md "Default
 * workspaces": the floating column is where the panel opens on first use). Layouts saved before kept an empty Audio
 * floating over the centre documents (found by the annotation e2e, 2026-10-04); its target never survives a reload, so
 * the restored panel was always empty. The migration takes those panels out of single-group floating windows (a
 * window left empty goes); docked ones and nested floating grids stay as the person arranged them.
 */
export function dropOnDemandFloats(layout: SerializedLayout, ids: readonly string[] = ON_DEMAND_FLOATS): SerializedLayout {
  const panels = layout.panels ?? {};
  const drop = new Set(Object.entries(panels).filter(([, p]) => ids.includes(String(p.params?.panel))).map(([id]) => id));
  const floats = (layout.floatingGroups ?? []) as FloatingGroup[];
  if (isPlaceholderLayout(layout) || drop.size === 0 || floats.length === 0) return layout;
  const removed = new Set<string>();
  const goneGroups = new Set<string>();
  const kept: FloatingGroup[] = [];
  for (const f of floats) {
    const views = f.data?.views;
    if (!f.data || !views || !views.some((v) => drop.has(v))) {
      kept.push(f);
      continue;
    }
    const rest = views.filter((v) => !drop.has(v));
    views.filter((v) => drop.has(v)).forEach((v) => removed.add(v));
    if (rest.length === 0) {
      goneGroups.add(f.data.id);
      continue;
    }
    const activeView = f.data.activeView && rest.includes(f.data.activeView) ? f.data.activeView : rest[0];
    kept.push({ ...f, data: { ...f.data, views: rest, activeView } });
  }
  if (removed.size === 0) return layout;
  const out: SerializedLayout = {
    ...layout,
    floatingGroups: kept,
    panels: Object.fromEntries(Object.entries(panels).filter(([id]) => !removed.has(id))),
  };
  if (typeof layout.activeGroup === "string" && goneGroups.has(layout.activeGroup)) delete out.activeGroup;
  return out;
}

/**
 * migrations[n] upgrades a workspace from schema n to n+1. Add one when the stored shape changes; never edit an
 * existing one. Version 1 is the first stored shape.
 */
export const migrations: Readonly<Record<number, Migration>> = {
  1: (w) => ({ ...w, layout: addChatToRightColumn(w.layout) }),
  2: (w) => ({ ...w, layout: dropOnDemandFloats(w.layout) }),
};

export class WorkspaceSchemaError extends Error {}

export function migrate(input: WorkspaceData, chain: Readonly<Record<number, Migration>> = migrations, target: number = WORKSPACE_SCHEMA_VERSION): WorkspaceData {
  let w = input;
  if (!Number.isInteger(w.schemaVersion) || w.schemaVersion < 1) {
    throw new WorkspaceSchemaError(`workspace "${w.name}" has no valid schemaVersion`);
  }
  if (w.schemaVersion > target) {
    throw new WorkspaceSchemaError(`workspace "${w.name}" is schema ${w.schemaVersion}, newer than this app (${target}); reload`);
  }
  while (w.schemaVersion < target) {
    const step = chain[w.schemaVersion];
    if (!step) throw new WorkspaceSchemaError(`no migration from workspace schema ${w.schemaVersion}`);
    w = { ...step(w), schemaVersion: w.schemaVersion + 1 };
  }
  return w;
}

/**
 * Rewrites panel params through the registry: aliases resolve to current ids; an id that no longer exists becomes a
 * placeholder panel ("Panel X no longer exists — remove"), never a failed restore. Returns the ids that changed.
 */
export function normalizeLayout(layout: SerializedLayout, registry: PanelRegistry): { layout: SerializedLayout; renamed: string[]; missing: string[] } {
  const renamed: string[] = [];
  const missing: string[] = [];
  const panels: Record<string, SerializedPanel> = {};
  for (const [key, p] of Object.entries(layout.panels ?? {})) {
    const id = typeof p.params?.panel === "string" ? p.params.panel : undefined;
    const manifest = id ? registry.resolve(id) : undefined;
    if (id && manifest && manifest.id === id) {
      panels[key] = p;
    } else if (id && manifest) {
      renamed.push(id);
      panels[key] = { ...p, params: { ...p.params, panel: manifest.id } };
    } else if (id === PLACEHOLDER_PANEL) {
      panels[key] = p;
    } else {
      missing.push(id ?? key);
      panels[key] = { ...p, contentComponent: "panel", params: { panel: PLACEHOLDER_PANEL, missing: id ?? key } };
    }
  }
  return { layout: { ...layout, panels }, renamed, missing };
}

/**
 * A stored workspace whose layout has no grid is a placeholder: the project bootstrap records the default workspaces
 * with an empty layout, and the client builds them from the code factories on first open (then saves over the
 * placeholder with its revision).
 */
export function isPlaceholderLayout(layout: SerializedLayout): boolean {
  return !layout.grid || typeof layout.grid !== "object";
}

/** Parses what the API returned (or a fixture) into WorkspaceData, rejecting shapes we cannot restore. */
export function parseWorkspace(raw: unknown): WorkspaceData {
  if (!raw || typeof raw !== "object") throw new WorkspaceSchemaError("workspace is not an object");
  const w = raw as Partial<WorkspaceData>;
  if (typeof w.name !== "string") throw new WorkspaceSchemaError("workspace has no name");
  if (!w.layout || typeof w.layout !== "object") throw new WorkspaceSchemaError(`workspace "${w.name}" has no layout`);
  return {
    schemaVersion: Number(w.schemaVersion),
    name: w.name,
    layout: w.layout,
    panels: w.panels && typeof w.panels === "object" ? w.panels : {},
  };
}
