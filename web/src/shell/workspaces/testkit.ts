import { DockviewComponent, type DockviewApi } from "dockview-core";
import { createPanelRegistry, type PanelManifest, type PanelRegistry } from "@/shell/registry/panels";

// Test helpers: a real Dockview (core, no React) in jsdom and a registry with the phase-0 panels' shapes.

// jsdom has no ResizeObserver; Dockview only needs it to exist (we lay out explicitly).
class NoopResizeObserver {
  observe(): void {}
  unobserve(): void {}
  disconnect(): void {}
}

export function headlessDockview(width = 1000, height = 800): { api: DockviewApi; dispose(): void } {
  const g = globalThis as { ResizeObserver?: unknown };
  g.ResizeObserver ??= NoopResizeObserver;
  const el = document.createElement("div");
  el.style.cssText = `position:relative;width:${width}px;height:${height}px`;
  document.body.appendChild(el);
  const dv = new DockviewComponent(el, {
    createComponent: () => {
      const element = document.createElement("div");
      return { element, init: () => undefined };
    },
  });
  dv.layout(width, height);
  return {
    api: dv.api,
    dispose: () => {
      dv.dispose();
      el.remove();
    },
  };
}

const stub = (id: string, kind: "document" | "tool", loc: PanelManifest["defaultLocation"]): PanelManifest => ({
  id,
  kind,
  title: id,
  icon: () => null,
  singleton: kind === "tool",
  defaultSize: { w: 300, h: 200 },
  defaultLocation: loc,
  help: `panels.${id}`,
  entity: kind === "document" ? "project" : undefined,
  empty: () => null,
  component: () => null,
});

export function phase0Registry(): PanelRegistry {
  const r = createPanelRegistry();
  r.register(stub("project", "document", "centre"));
  r.register(stub("library", "tool", "left"));
  r.register(stub("inspector", "tool", "right"));
  r.register(stub("help", "tool", "right"));
  return r;
}
