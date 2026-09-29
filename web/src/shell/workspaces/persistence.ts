import type { DockviewApi, SerializedDockview } from "dockview-react";
import { create } from "zustand";
import { commandHeaders, ProblemError } from "@/api/client";
import { workspacesGet, workspacesSet } from "@/api/gen/sdk.gen";
import { openPanel } from "@/shell/dock/layout";
import { panels } from "@/shell/registries";
import { useSelection } from "@/shell/selection/store";
import { notifyError } from "@/shell/notifications/store";
import { isDefaultWorkspace, planDefaultLayout, type Placement } from "./defaults";
import { migrate, normalizeLayout, parseWorkspace, WORKSPACE_SCHEMA_VERSION, type PanelState, type SerializedLayout } from "./schema";

// Workspaces are saved per user per project through the API, debounced 1 s after Dockview's layout-change event,
// with If-Match on the revision. A losing tab (412) gets an inline notice instead of overwriting.

type SyncState = {
  key: string | null; // `${project}/${name}`
  rev: number | undefined;
  restoring: boolean;
  conflict: boolean;
  saving: boolean;
  lastRestoreMs: number | undefined;
  missingPanels: string[];
};

export const useWorkspaceSync = create<SyncState>(() => ({
  key: null,
  rev: undefined,
  restoring: false,
  conflict: false,
  saving: false,
  lastRestoreMs: undefined,
  missingPanels: [],
}));

const SAVE_DEBOUNCE_MS = 1000;

function nextFrame(): Promise<void> {
  return new Promise((r) => requestAnimationFrame(() => r()));
}

export function applyPlan(api: DockviewApi, plan: Placement[]): void {
  api.clear();
  for (const p of plan) openPanel(p.panel, { location: p.location, doc: p.doc, inactive: p.location !== "centre" });
  const centre = plan.find((p) => p.location === "centre");
  if (centre) openPanel(centre.panel, { doc: centre.doc });
}

export function defaultPlan(name: string, project: string): Placement[] {
  return planDefaultLayout(isDefaultWorkspace(name) ? name : "Training", panels, project);
}

function pinsOf(api: DockviewApi): Record<string, PanelState> {
  const pins = useSelection.getState().pins;
  const out: Record<string, PanelState> = {};
  for (const p of api.panels) {
    const pinnedTo = pins[p.id];
    if (pinnedTo) out[p.id] = { pinnedTo };
  }
  return out;
}

/** Loads a workspace (or builds its default) into Dockview. Measures `cadence:restore` for the S4 budget. */
export async function restoreWorkspace(api: DockviewApi, project: string, name: string): Promise<void> {
  const key = `${project}/${name}`;
  useWorkspaceSync.setState({ key, restoring: true, conflict: false, rev: undefined, missingPanels: [] });
  let layout: SerializedLayout | null = null;
  let rev: number | undefined;
  let pinned: Record<string, PanelState> = {};
  try {
    const res = await workspacesGet({ path: { p: project, name } });
    const ws = migrate(parseWorkspace(res.data));
    const norm = normalizeLayout(ws.layout, panels);
    layout = norm.layout;
    rev = res.data?.rev;
    pinned = ws.panels;
    useWorkspaceSync.setState({ missingPanels: norm.missing });
  } catch (err) {
    if (!(err instanceof ProblemError && err.status === 404)) {
      notifyError(`Workspace “${name}” could not be loaded; showing the default`, err);
    }
  }
  if (useWorkspaceSync.getState().key !== key) return; // a newer restore started
  performance.mark("cadence:restore:start");
  if (layout) {
    api.fromJSON(layout as unknown as SerializedDockview);
  } else {
    applyPlan(api, defaultPlan(name, project));
  }
  const sel = useSelection.getState();
  for (const [id, st] of Object.entries(pinned)) if (st.pinnedTo) sel.pin(id, st.pinnedTo);
  await nextFrame();
  performance.mark("cadence:restore:end");
  const m = performance.measure("cadence:restore", "cadence:restore:start", "cadence:restore:end");
  useWorkspaceSync.setState({ restoring: false, rev, lastRestoreMs: m.duration });
}

export async function saveWorkspace(api: DockviewApi, project: string, name: string, opts: { force?: boolean } = {}): Promise<void> {
  const st = useWorkspaceSync.getState();
  if (st.key !== `${project}/${name}` || st.restoring) return;
  let rev = st.rev;
  if (opts.force) {
    try {
      rev = (await workspacesGet({ path: { p: project, name } })).data?.rev;
    } catch {
      rev = undefined;
    }
  }
  useWorkspaceSync.setState({ saving: true });
  try {
    const res = await workspacesSet({
      path: { p: project, name },
      body: { schemaVersion: WORKSPACE_SCHEMA_VERSION, layout: api.toJSON() as unknown as Record<string, unknown>, panels: pinsOf(api) },
      headers: commandHeaders(rev),
    });
    useWorkspaceSync.setState({ rev: res.data?.rev, conflict: false });
  } catch (err) {
    if (err instanceof ProblemError && (err.status === 412 || err.status === 428)) {
      useWorkspaceSync.setState({ conflict: true });
    } else {
      notifyError(`Workspace “${name}” was not saved`, err);
    }
  } finally {
    useWorkspaceSync.setState({ saving: false });
  }
}

/** Debounced autosave on every layout change; returns the unsubscribe. */
export function startAutosave(api: DockviewApi, project: string, name: string): () => void {
  let timer: ReturnType<typeof setTimeout> | undefined;
  const sub = api.onDidLayoutChange(() => {
    const st = useWorkspaceSync.getState();
    if (st.restoring || st.conflict) return;
    clearTimeout(timer);
    timer = setTimeout(() => void saveWorkspace(api, project, name), SAVE_DEBOUNCE_MS);
  });
  return () => {
    clearTimeout(timer);
    sub.dispose();
  };
}
