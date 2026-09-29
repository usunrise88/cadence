import { Cube } from "iconoir-react";
import { useState } from "react";
import { EmptyState } from "@/shell/entity/primitives";
import { useTopic, type PanelProps } from "@/shell/panel";
import { panels } from "@/shell/registries";
import type { PanelManifest } from "@/shell/registry/panels";

// Spike S4 (docs/spikes/S4-restore-timing.md): 20 stub tool panels, each subscribing to a fake topic only while
// visible. Registered only with ?spikes in the URL; never part of the product.

function StubPanel({ panelId }: PanelProps) {
  const [n, setN] = useState(0);
  useTopic([`spike.${panelId}.*`], (b) => setN((x) => x + b.length));
  return (
    <div className="p-3 text-xs" data-stub={panelId}>
      {panelId} · {n} events
    </div>
  );
}

export const SPIKE_PANEL_COUNT = 20;

export function spikePanelIds(): string[] {
  return Array.from({ length: SPIKE_PANEL_COUNT }, (_, i) => `stub-${String(i + 1).padStart(2, "0")}`);
}

export function registerSpikePanels(renderer: "onlyWhenVisible" | "always" = "onlyWhenVisible"): void {
  for (const id of spikePanelIds()) {
    const m: PanelManifest = {
      id,
      kind: "tool",
      title: id,
      icon: Cube,
      singleton: true,
      defaultSize: { w: 320, h: 220 },
      defaultLocation: "right",
      renderer,
      help: "panels.help",
      empty: () => <EmptyState step="review" title={id} />,
      component: StubPanel,
    };
    panels.register(m);
  }
}
