import type { PanelRegistry, Location } from "@/shell/registry/panels";
import { docRef } from "@/shell/entity/manifest";
import type { DefaultWorkspaceName } from "./schema";

// Default workspaces are code factories, not stored JSON, so "Reset to default" always matches the current
// registry (docs/spec/10-ui-shell.md "Persistence"). The table follows docs/spec/11-ui-panels.md "Default
// workspaces"; panels that do not exist yet are skipped, and every workspace opens the Project home in the centre.

type Slot = { panel: string; location: Location };

const TABLE: Record<DefaultWorkspaceName, Slot[]> = {
  Training: [
    { panel: "library", location: "left" },
    { panel: "chat", location: "right" },
    { panel: "checkpoints", location: "right" },
    { panel: "inspector", location: "right" },
    { panel: "help", location: "right" },
    { panel: "metrics", location: "bottom" },
    { panel: "logs", location: "bottom" },
  ],
  Eval: [
    { panel: "library", location: "left" },
    { panel: "chat", location: "right" },
    { panel: "inspector", location: "right" },
    { panel: "help", location: "right" },
    { panel: "diff", location: "bottom" },
    { panel: "audio", location: "floating" },
  ],
  Data: [
    { panel: "library", location: "left" },
    { panel: "chat", location: "right" },
    { panel: "inspector", location: "right" },
    { panel: "pipeline-run", location: "right" },
    { panel: "help", location: "right" },
    { panel: "logs", location: "bottom" },
    { panel: "audio", location: "floating" },
  ],
  Triage: [
    { panel: "chat", location: "right" },
    { panel: "diff", location: "right" },
    { panel: "help", location: "right" },
    { panel: "inspector", location: "bottom" },
    { panel: "audio", location: "floating" },
  ],
  Ops: [
    { panel: "queue-gpu", location: "left" },
    { panel: "chat", location: "right" },
    { panel: "approvals", location: "right" },
    { panel: "storage", location: "right" },
    { panel: "help", location: "right" },
    { panel: "shadow", location: "bottom" },
    { panel: "logs", location: "bottom" },
  ],
};

export type Placement = { panel: string; location: Location; doc?: string };

const ORDER: Location[] = ["centre", "left", "right", "bottom", "floating"];

/** The panels a default workspace opens, in the order they must be added (centre first). */
export function planDefaultLayout(name: DefaultWorkspaceName, registry: PanelRegistry, project: string): Placement[] {
  const out: Placement[] = [];
  if (registry.get("project")) out.push({ panel: "project", location: "centre", doc: docRef("project", project) });
  for (const s of TABLE[name]) {
    const m = registry.get(s.panel);
    if (!m || m.kind === "document") continue; // documents need an entity; they open from Library or links
    out.push({ panel: s.panel, location: s.location });
  }
  return out.sort((a, b) => ORDER.indexOf(a.location) - ORDER.indexOf(b.location));
}

export function isDefaultWorkspace(name: string): name is DefaultWorkspaceName {
  return name in TABLE;
}
