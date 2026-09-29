import { PLACEHOLDER_PANEL, type PanelParams, type PanelRegistry } from "@/shell/registry/panels";

// docs/spec/10-ui-shell.md "Persistence": a workspace is versioned data that survives panel renames and Dockview
// upgrades. The layout is Dockview's own serialization; everything else is ours.

export const WORKSPACE_SCHEMA_VERSION = 1;
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

/**
 * migrations[n] upgrades a workspace from schema n to n+1. Add one when the stored shape changes; never edit an
 * existing one. Version 1 is the first stored shape.
 */
export const migrations: Readonly<Record<number, Migration>> = {};

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
