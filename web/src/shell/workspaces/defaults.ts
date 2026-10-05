import type { PanelRegistry, Location } from "@/shell/registry/panels";
import { docRef } from "@/shell/entity/manifest";
import type { DefaultWorkspaceName } from "./schema";

// Default workspaces are code factories, not stored JSON, so "Reset to default" always matches the current
// registry (docs/spec/10-ui-shell.md "Persistence"). The table follows docs/spec/11-ui-panels.md "Default
// workspaces"; panels that do not exist yet are skipped, and every workspace opens the Project home in the centre.
// The Floating column is a slot, not a panel to open: a floating tool (Audio) opens there on first use (openAudio from
// a row, a reference, a search hit), never when the workspace is built, so no empty window covers the centre
// documents (the annotation e2e found the Audio float over the Annotation batch, Dataset version and Eval report).

type Slot = { panel: string; location: Location };

const TABLE: Record<DefaultWorkspaceName, Slot[]> = {
  // Centre documents (Run, Mix, Experiment) open from the Library, links and commands. Checkpoints and Metrics are
  // slots for panels that arrive later in phase 2: planDefaultLayout skips them until they register, then a new or
  // reset workspace places them here (stored workspaces keep their layout; no migration is needed).
  Training: [
    { panel: "library", location: "left" },
    { panel: "chat", location: "right" },
    { panel: "checkpoints", location: "right" },
    { panel: "inspector", location: "right" },
    { panel: "help", location: "right" },
    // Getting started is shown until the first gate passes (11 "Panel catalogue"); Training is where a newcomer lands.
    { panel: "getting-started", location: "right" },
    { panel: "metrics", location: "bottom" },
    { panel: "logs", location: "bottom" },
  ],
  Eval: [
    { panel: "library", location: "left" },
    { panel: "chat", location: "right" },
    { panel: "inspector", location: "right" },
    // Lineage follows the active Eval report (its subject), golden set or model; the spec's table leaves it to the
    // palette, the Eval workspace keeps it one tab away.
    { panel: "lineage", location: "right" },
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
    { panel: "triage", location: "centre" },
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
    if (s.location === "floating") continue; // a slot: the panel floats when something opens it
    if (m.inDefaults && !m.inDefaults()) continue;
    out.push({ panel: s.panel, location: s.location });
  }
  return out.sort((a, b) => ORDER.indexOf(a.location) - ORDER.indexOf(b.location));
}

export function isDefaultWorkspace(name: string): name is DefaultWorkspaceName {
  return name in TABLE;
}
