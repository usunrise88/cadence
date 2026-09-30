import type { DockviewApi, SerializedDockview } from "dockview-react";
import { create } from "zustand";
import { commandHeaders, ProblemError } from "@/api/client";
import { workspacesGet, workspacesSet } from "@/api/gen/sdk.gen";
import { openPanel } from "@/shell/dock/layout";
import { panels } from "@/shell/registries";
import { useSelection } from "@/shell/selection/store";
import { notifyError } from "@/shell/notifications/store";
import { createAutosaver } from "./autosave";
import { isDefaultWorkspace, planDefaultLayout, type Placement } from "./defaults";
import { isPlaceholderLayout, migrate, normalizeLayout, parseWorkspace, WORKSPACE_SCHEMA_VERSION, type PanelState, type SerializedLayout } from "./schema";

// Workspaces are saved per user per project through the API, 2 s after the last layout change (a window being
// dragged fires many), only when the layout differs from the stored one, and at once when the page is hidden or the
// workspace is switched; with If-Match on the revision. A losing tab (412) gets an inline notice instead of
// overwriting.

type SyncState = {
  key: string | null; // `${project}/${name}`
  rev: number | undefined;
  /** The stored workspace is the bootstrap's placeholder: shown from the default plan, not saved by anyone yet. */
  placeholder: boolean;
  restoring: boolean;
  conflict: boolean;
  saving: boolean;
  lastRestoreMs: number | undefined;
  missingPanels: string[];
  /** The serialized body the server holds for `key` (after a restore or a save); autosave skips when unchanged. */
  savedSnapshot: string | undefined;
};

export const useWorkspaceSync = create<SyncState>(() => ({
  key: null,
  rev: undefined,
  placeholder: false,
  restoring: false,
  conflict: false,
  saving: false,
  lastRestoreMs: undefined,
  missingPanels: [],
  savedSnapshot: undefined,
}));

export const SAVE_QUIET_MS = 2000;
/** Browsers cap keepalive request bodies at 64 KiB; a larger layout is sent as a normal request. */
const KEEPALIVE_MAX_BYTES = 60_000;

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

type WorkspaceBody = { schemaVersion: number; layout: Record<string, unknown>; panels: Record<string, PanelState> };

/** The request body for the layout on screen, serialized (the autosave compares these strings). */
function serialize(api: DockviewApi): string {
  const body: WorkspaceBody = { schemaVersion: WORKSPACE_SCHEMA_VERSION, layout: api.toJSON() as unknown as Record<string, unknown>, panels: pinsOf(api) };
  return JSON.stringify(body);
}

/** Loads a workspace (or builds its default) into Dockview. Measures `cadence:restore` for the S4 budget. */
export async function restoreWorkspace(api: DockviewApi, project: string, name: string): Promise<void> {
  const key = `${project}/${name}`;
  useWorkspaceSync.setState({ key, restoring: true, conflict: false, rev: undefined, placeholder: false, missingPanels: [], savedSnapshot: undefined });
  let layout: SerializedLayout | null = null;
  let rev: number | undefined;
  let pinned: Record<string, PanelState> = {};
  try {
    const res = await workspacesGet({ path: { p: project, name } });
    const ws = migrate(parseWorkspace(res.data));
    rev = res.data?.rev;
    pinned = ws.panels;
    if (isPlaceholderLayout(ws.layout)) {
      useWorkspaceSync.setState({ placeholder: true });
    } else {
      const norm = normalizeLayout(ws.layout, panels);
      layout = norm.layout;
      useWorkspaceSync.setState({ missingPanels: norm.missing });
    }
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
  if (useWorkspaceSync.getState().key !== key) return;
  useWorkspaceSync.setState({ restoring: false, rev, lastRestoreMs: m.duration, savedSnapshot: serialize(api) });
}

export async function saveWorkspace(api: DockviewApi, project: string, name: string, opts: { force?: boolean } = {}): Promise<void> {
  const st = useWorkspaceSync.getState();
  if (st.key !== `${project}/${name}` || st.restoring) return;
  const snapshot = serialize(api);
  let rev = st.rev;
  if (opts.force) {
    try {
      rev = (await workspacesGet({ path: { p: project, name } })).data?.rev;
    } catch {
      rev = undefined;
    }
  }
  await put(project, name, snapshot, rev, false);
}

/** Stores a serialized workspace on `rev`; state updates are dropped when the shell has moved to another workspace. */
async function put(project: string, name: string, snapshot: string, rev: number | undefined, urgent: boolean): Promise<void> {
  const key = `${project}/${name}`;
  const current = () => useWorkspaceSync.getState().key === key;
  useWorkspaceSync.setState({ saving: true });
  try {
    const res = await workspacesSet({
      path: { p: project, name },
      body: JSON.parse(snapshot) as WorkspaceBody,
      headers: commandHeaders(rev),
      // Page hidden or closing: let the request outlive the page.
      keepalive: urgent && snapshot.length < KEEPALIVE_MAX_BYTES,
    });
    if (current()) useWorkspaceSync.setState({ rev: res.data?.rev, conflict: false, placeholder: false, savedSnapshot: snapshot });
  } catch (err) {
    if (err instanceof ProblemError && (err.status === 412 || err.status === 428)) {
      if (current()) useWorkspaceSync.setState({ conflict: true });
    } else {
      notifyError(`Workspace “${name}” was not saved`, err);
    }
  } finally {
    useWorkspaceSync.setState({ saving: false });
  }
}

/**
 * Autosave on layout and pin changes (quiet period, skip-unchanged, flush on page hide); returns the stop function,
 * which saves a pending change before unsubscribing (a workspace switch or leaving the shell).
 */
export function startAutosave(api: DockviewApi, project: string, name: string): () => void {
  const key = `${project}/${name}`;
  const saver = createAutosaver({
    delayMs: SAVE_QUIET_MS,
    snapshot: () => {
      const st = useWorkspaceSync.getState();
      return st.key === key && !st.restoring && !st.conflict ? serialize(api) : undefined;
    },
    saved: () => useWorkspaceSync.getState().savedSnapshot,
    save: (snapshot, urgent) => put(project, name, snapshot, useWorkspaceSync.getState().rev, urgent),
  });
  const sub = api.onDidLayoutChange(() => saver.schedule());
  // A pin (a tool pinned to a document, Chat pinned to its agent session) is workspace state too.
  const offPins = useSelection.subscribe((s, prev) => {
    if (s.pins !== prev.pins) saver.schedule();
  });
  const onHide = () => {
    if (document.visibilityState === "hidden") void saver.flush(true);
  };
  const onPageHide = () => void saver.flush(true);
  document.addEventListener("visibilitychange", onHide);
  window.addEventListener("pagehide", onPageHide);
  return () => {
    void saver.flush();
    saver.dispose();
    sub.dispose();
    offPins();
    document.removeEventListener("visibilitychange", onHide);
    window.removeEventListener("pagehide", onPageHide);
  };
}
