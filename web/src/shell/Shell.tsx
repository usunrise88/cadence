import { useCallback, useEffect, useRef, useState } from "react";
import { Button } from "@/components/ui/button";
import { Dialogs } from "@/shell/chrome/Dialogs";
import { MenuBar } from "@/shell/chrome/MenuBar";
import { StatusBar } from "@/shell/chrome/StatusBar";
import { installKeyboard } from "@/shell/commands/registry";
import { isMac } from "@/shell/commands/keymap";
import { DockHost } from "@/shell/dock/DockHost";
import { openPanel } from "@/shell/dock/layout";
import { useDock } from "@/shell/dock/store";
import { useCachePatching } from "@/shell/live/patching";
import { openDocument } from "@/shell/panel/actions";
import { commands, events } from "@/shell/registries";
import { useSelection } from "@/shell/selection/store";
import { commandContext, useShell } from "@/shell/state";
import { restoreWorkspace, saveWorkspace, startAutosave, useWorkspaceSync } from "@/shell/workspaces/persistence";

// The desktop: menu bar, the Dockview work surface, status bar and modal flows, for one project workspace.

export type ShellProps = {
  project: string;
  workspace: string;
  doc?: string;
  sel?: string;
  onNavigate: (project: string, workspace: string) => void;
};

export function Shell({ project, workspace, doc, sel, onNavigate }: ShellProps) {
  const api = useDock((s) => s.api);
  const [restoredKey, setRestoredKey] = useState<string | null>(null);
  const deepLinked = useRef<string | null>(null);
  useCachePatching();

  useEffect(() => {
    useShell.getState().setRoute(project, workspace);
    events.setProject(project);
  }, [project, workspace]);

  useEffect(() => installKeyboard(document, commands, commandContext, isMac()), []);

  // Restore on (project, workspace) change, then autosave.
  useEffect(() => {
    if (!api) return;
    let stop: (() => void) | undefined;
    let cancelled = false;
    void restoreWorkspace(api, project, workspace).then(() => {
      if (cancelled) return;
      setRestoredKey(`${project}/${workspace}`);
      stop = startAutosave(api, project, workspace);
    });
    return () => {
      cancelled = true;
      stop?.();
    };
  }, [api, project, workspace]);

  // Deep link: open or focus the document, then restore the selection inside it.
  useEffect(() => {
    const key = `${project}/${workspace}`;
    if (restoredKey !== key || !doc) return;
    const link = `${key}?${doc}&${sel ?? ""}`;
    if (deepLinked.current === link) return;
    deepLinked.current = link;
    openDocument(doc);
    if (sel) useSelection.getState().select(doc, sel);
  }, [restoredKey, project, workspace, doc, sel]);

  const switchProject = useCallback((slug: string) => onNavigate(slug, workspace), [onNavigate, workspace]);

  return (
    <div className="flex h-full flex-col">
      <MenuBar onSwitchProject={switchProject} />
      <WorkspaceConflict />
      <main className="min-h-0 flex-1" aria-label="Work surface">
        <DockHost onReady={() => undefined} />
      </main>
      <StatusBar />
      <Dialogs onSwitchProject={switchProject} />
    </div>
  );
}

/** Two tabs editing one workspace: last write wins with a version check; the losing tab gets this notice. */
function WorkspaceConflict() {
  const conflict = useWorkspaceSync((s) => s.conflict);
  const api = useDock((s) => s.api);
  const { project, workspace } = useShell();
  if (!conflict || !api || !project || !workspace) return null;
  return (
    <div role="alert" className="flex items-center gap-2 border-b bg-status-warning/15 px-3 py-1 text-xs" data-testid="workspace-conflict">
      This workspace was changed in another tab or window. Your layout here is not saved.
      <Button size="xs" variant="outline" className="ml-auto" onClick={() => void restoreWorkspace(api, project, workspace)}>
        Load theirs
      </Button>
      <Button size="xs" onClick={() => void saveWorkspace(api, project, workspace, { force: true })}>
        Keep mine
      </Button>
    </div>
  );
}

export { openPanel };
