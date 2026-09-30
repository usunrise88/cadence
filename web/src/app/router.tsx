import { useEffect } from "react";
import { useQuery } from "@tanstack/react-query";
import { createRootRoute, createRoute, createRouter, Outlet, useNavigate } from "@tanstack/react-router";
import { projectsListOptions } from "@/api/gen/@tanstack/react-query.gen";
import { Button } from "@/components/ui/button";
import { AuthGate } from "@/shell/auth/AuthGate";
import { TooltipProvider } from "@/components/ui/tooltip";
import { Dialogs } from "@/shell/chrome/Dialogs";
import { useDialogs } from "@/shell/chrome/dialogs";
import { setNavigator } from "@/shell/commands/builtin";
import { Shell } from "@/shell/Shell";

// Routes are deep links into the desktop, not pages: /p/:project/w/:workspace?doc=kind:id&sel=item.

const LAST = "cadence.lastWorkspace";

function remember(project: string, workspace: string): void {
  try {
    localStorage.setItem(LAST, JSON.stringify({ project, workspace }));
  } catch {
    /* storage unavailable */
  }
}

function recall(): { project: string; workspace: string } | null {
  try {
    const raw = localStorage.getItem(LAST);
    return raw ? (JSON.parse(raw) as { project: string; workspace: string }) : null;
  } catch {
    return null;
  }
}

const rootRoute = createRootRoute({
  component: () => (
    <TooltipProvider delay={400}>
      <AuthGate>
        <Outlet />
      </AuthGate>
    </TooltipProvider>
  ),
});

function Home() {
  const navigate = useNavigate();
  const { data, isLoading, error } = useQuery(projectsListOptions());
  const go = (project: string, workspace = "Training") => void navigate({ to: "/p/$project/w/$workspace", params: { project, workspace }, search: {} });
  useEffect(() => {
    if (!data || data.items.length === 0) return;
    const last = recall();
    const target = last && data.items.some((p) => p.slug === last.project) ? last : { project: data.items[0]!.slug, workspace: "Training" };
    go(target.project, target.workspace);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [data]);
  return (
    <div className="flex h-full flex-col items-center justify-center gap-3 p-6 text-center">
      <h1 className="text-lg font-semibold">Cadence</h1>
      {isLoading ? <p className="text-sm text-muted-foreground">Loading projects…</p> : null}
      {error ? (
        <p role="alert" className="text-sm text-destructive">
          The control plane is not reachable: {error instanceof Error ? error.message : String(error)}
        </p>
      ) : null}
      {data && data.items.length === 0 ? (
        <>
          <p className="max-w-md text-sm text-muted-foreground">
            No projects yet. A project is a unit of work with its own repository, budgets and gates; the wizard needs three fields.
          </p>
          <Button onClick={() => useDialogs.getState().show({ kind: "newProject" })}>New project</Button>
        </>
      ) : null}
      <Dialogs onSwitchProject={(slug) => go(slug)} />
    </div>
  );
}

const indexRoute = createRoute({ getParentRoute: () => rootRoute, path: "/", component: Home });

type WorkspaceSearch = { doc?: string; sel?: string };

const workspaceRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: "/p/$project/w/$workspace",
  validateSearch: (s: Record<string, unknown>): WorkspaceSearch => ({
    doc: typeof s.doc === "string" ? s.doc : undefined,
    sel: typeof s.sel === "string" ? s.sel : undefined,
  }),
  component: WorkspaceView,
});

function WorkspaceView() {
  const { project, workspace } = workspaceRoute.useParams();
  const { doc, sel } = workspaceRoute.useSearch();
  const navigate = useNavigate();
  const onNavigate = (p: string, w: string) => void navigate({ to: "/p/$project/w/$workspace", params: { project: p, workspace: w }, search: {} });
  useEffect(() => {
    remember(project, workspace);
    setNavigator({ toWorkspace: onNavigate });
  });
  return <Shell project={project} workspace={workspace} doc={doc} sel={sel} onNavigate={onNavigate} />;
}

const routeTree = rootRoute.addChildren([indexRoute, workspaceRoute]);

export const router = createRouter({ routeTree, defaultPreload: false });

declare module "@tanstack/react-router" {
  interface Register {
    router: typeof router;
  }
}
