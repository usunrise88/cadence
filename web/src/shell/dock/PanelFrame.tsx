import { Component, useEffect, useLayoutEffect, useRef, useState, type ErrorInfo, type ReactNode } from "react";
import type { IDockviewPanelProps } from "dockview-react";
import { useQueryClient } from "@tanstack/react-query";
import { Button } from "@/components/ui/button";
import { PortalContainerContext } from "@/lib/portal";
import { cn } from "@/lib/utils";
import { EmptyState, EntityHeader, EntityTabs, NextStep, type DocTab } from "@/shell/entity/primitives";
import { parseDocRef, type EntityManifest } from "@/shell/entity/manifest";
import { patchDrafts } from "@/shell/entity/drafts";
import { PanelContext, useTopic } from "@/shell/panel/context";
import { entities, panels } from "@/shell/registries";
import { PLACEHOLDER_PANEL, type PanelManifest, type PanelParams } from "@/shell/registry/panels";

// Wraps every panel: context (visibility, instance), the portal container of the document that owns it (popouts),
// an error boundary, and — for documents — the anatomy rendered from the entity manifest.

/** The body of the document that currently holds `ref` (it changes when a group pops out). */
function useOwnerBody(ref: React.RefObject<HTMLElement | null>, deps: unknown[]): HTMLElement | null {
  const [body, setBody] = useState<HTMLElement | null>(null);
  useLayoutEffect(() => {
    setBody(ref.current?.ownerDocument.body ?? null);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, deps);
  return body;
}

export function PanelFrame(props: IDockviewPanelProps<PanelParams>) {
  const { api, params } = props;
  const [visible, setVisible] = useState(api.isVisible);
  const [moves, setMoves] = useState(0);
  const ref = useRef<HTMLDivElement>(null);

  useEffect(() => {
    const a = api.onDidVisibilityChange((e) => setVisible(e.isVisible));
    // Moving to or from a popout re-parents the DOM; re-read the owner document on the next frame.
    const b = api.onDidLocationChange(() => requestAnimationFrame(() => setMoves((n) => n + 1)));
    return () => {
      a.dispose();
      b.dispose();
    };
  }, [api]);
  const body = useOwnerBody(ref, [moves]);

  const manifest = params.panel === PLACEHOLDER_PANEL ? undefined : panels.resolve(params.panel);
  const ctx = { instanceId: api.id, panelId: manifest?.id ?? PLACEHOLDER_PANEL, doc: params.doc, visible };

  let content: ReactNode;
  if (!manifest) {
    content = <Placeholder missing={params.missing ?? params.panel} onRemove={() => api.close()} />;
  } else if (manifest.kind === "document") {
    content = <DocumentFrame manifest={manifest} doc={params.doc} instanceId={api.id} />;
  } else {
    const C = manifest.component;
    content = <C panelId={manifest.id} instanceId={api.id} doc={params.doc} />;
  }

  return (
    <PortalContainerContext.Provider value={body}>
      <PanelContext.Provider value={ctx}>
        <div
          ref={ref}
          data-panel={manifest?.id ?? PLACEHOLDER_PANEL}
          data-help={manifest?.help}
          className={cn("flex h-full min-h-0 flex-col text-foreground", manifest?.kind === "tool" ? "bg-tool" : "bg-background")}
        >
          <PanelErrorBoundary title={manifest?.title ?? "Panel"}>{content}</PanelErrorBoundary>
        </div>
      </PanelContext.Provider>
    </PortalContainerContext.Provider>
  );
}

function DocumentFrame({ manifest, doc, instanceId }: { manifest: PanelManifest; doc?: string; instanceId: string }) {
  const em = manifest.entity ? entities.get(manifest.entity) : undefined;
  const ref = doc ? parseDocRef(doc) : undefined;
  const [tab, setTab] = useState<DocTab>("overview");
  const { data, error, isLoading } = (em?.useData ?? noData)(ref?.id ?? "");
  useDocumentLive(em, ref?.id);
  if (!em) return <EmptyState step="prepare" title={`No entity manifest for ${manifest.entity ?? manifest.id}`} />;
  if (!ref) {
    const Empty = manifest.empty;
    return <Empty />;
  }
  if (isLoading) return <div className="p-3 text-xs text-muted-foreground">Loading…</div>;
  if (error || !data) {
    return <EmptyState step="prepare" title={`${manifest.title} ${ref.id} could not be loaded`} hint={error instanceof Error ? error.message : undefined} />;
  }
  const C = manifest.component;
  return (
    <>
      <EntityHeader manifest={em} entity={data} />
      <EntityTabs value={tab} onChange={setTab} />
      <div role="tabpanel" className="min-h-0 flex-1 overflow-auto">
        <C panelId={manifest.id} instanceId={instanceId} doc={doc} tab={tab} entity={data} />
      </div>
      <NextStep manifest={em} entity={data} />
    </>
  );
}

/**
 * Live updates of an open document (docs/spec/06-platform.md "Cache patching"): while the document is visible the
 * shell subscribes to its entity's topics and patches the query cache with the manifest's patcher, and the drafts
 * of a draftable kind with the shared one — so an agent's draft appears without a re-fetch.
 */
function useDocumentLive(em: EntityManifest | undefined, id: string | undefined): void {
  const qc = useQueryClient();
  const live = em?.live;
  const topics = live && id ? live.topics(id) : null;
  useTopic(topics, (batch) => {
    if (!live || !id) return;
    live.patch(qc, batch, id);
    if (em?.draftable) patchDrafts(qc, batch);
  });
}

function noData(): { data?: undefined; error?: undefined; isLoading: boolean } {
  return { isLoading: false };
}

function Placeholder({ missing, onRemove }: { missing: string; onRemove: () => void }) {
  return (
    <div className="flex h-full flex-col items-center justify-center gap-2 p-6 text-center">
      <p className="text-sm">Panel “{missing}” no longer exists.</p>
      <Button size="sm" variant="outline" onClick={onRemove}>
        Remove
      </Button>
    </div>
  );
}

class PanelErrorBoundary extends Component<{ title: string; children: ReactNode }, { error: Error | null }> {
  state = { error: null as Error | null };
  static getDerivedStateFromError(error: Error) {
    return { error };
  }
  componentDidCatch(error: Error, info: ErrorInfo) {
    console.error(`panel "${this.props.title}" crashed`, error, info.componentStack);
  }
  render() {
    if (!this.state.error) return this.props.children;
    return (
      <div role="alert" className="flex h-full flex-col items-center justify-center gap-2 p-6 text-center">
        <p className="text-sm">{this.props.title} failed to render.</p>
        <p className="max-w-96 text-xs text-muted-foreground">{this.state.error.message}</p>
        <Button size="sm" variant="outline" onClick={() => this.setState({ error: null })}>
          Retry
        </Button>
      </div>
    );
  }
}
