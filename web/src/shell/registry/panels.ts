import type { ComponentType, SVGProps } from "react";
import type { EntityData, EntityKind } from "@/shell/entity/manifest";
import type { DocTab } from "@/shell/entity/primitives";
import type { Reference, Selection } from "@/shell/selection/store";

export type IconoirIcon = ComponentType<SVGProps<SVGSVGElement>>;
export type Location = "centre" | "left" | "right" | "bottom" | "floating";

/** Props every panel component receives from the shell. */
export type PanelProps = {
  /** Registered manifest id. */
  panelId: string;
  /** Dockview panel id: the manifest id for singletons, `<manifest>:<doc>` for documents. */
  instanceId: string;
  /** For documents: the entity this document shows (`kind:id`). */
  doc?: string;
  /** For documents: the section the shell's tab strip selected, and the loaded entity. */
  tab?: DocTab;
  entity?: EntityData;
};

// docs/spec/10-ui-shell.md "Shell concepts" — plus the fields the shell needs to render and document a panel.
export type PanelManifest = {
  id: string; // stable; renames go through panelAliases
  kind: "document" | "tool";
  title: string;
  icon: IconoirIcon;
  singleton: boolean;
  defaultSize: { w: number; h: number };
  defaultLocation: Location;
  renderer?: "onlyWhenVisible" | "always";
  commands?: string[];
  agentContext?: (sel: Selection) => Reference[];
  acceptsDrafts?: EntityKind[];
  /** Documents: the entity manifest that renders header, actions and next step. */
  entity?: EntityKind;
  /** Help article id (`panels.<id>`); CI fails when the article is missing. */
  help: string;
  empty: ComponentType;
  component: ComponentType<PanelProps>;
};

/**
 * Renamed panel ids: old id → current id. Stored workspaces restore through this map; an id that is neither
 * registered nor aliased restores as a placeholder panel.
 */
export const panelAliases: Readonly<Record<string, string>> = {
  properties: "inspector",
};

export const PLACEHOLDER_PANEL = "placeholder";

export type PanelRegistry = {
  register(m: PanelManifest): void;
  get(id: string): PanelManifest | undefined;
  resolve(id: string): PanelManifest | undefined;
  all(): PanelManifest[];
};

export function createPanelRegistry(aliases: Readonly<Record<string, string>> = panelAliases): PanelRegistry {
  const byId = new Map<string, PanelManifest>();
  return {
    register(m) {
      if (byId.has(m.id)) throw new Error(`panel "${m.id}" registered twice`);
      if (aliases[m.id]) throw new Error(`panel id "${m.id}" is an alias of "${aliases[m.id]}"`);
      if (m.kind === "document" && !m.entity) throw new Error(`document panel "${m.id}" needs an entity manifest`);
      if (!/^panels\.[a-z0-9-]+$/.test(m.help)) throw new Error(`panel "${m.id}": help must be panels.<slug>`);
      byId.set(m.id, m);
    },
    get: (id) => byId.get(id),
    resolve(id) {
      const seen = new Set<string>();
      let cur = id;
      let next = aliases[cur];
      while (!byId.has(cur) && next && !seen.has(cur)) {
        seen.add(cur);
        cur = next;
        next = aliases[cur];
      }
      return byId.get(cur);
    },
    all: () => [...byId.values()].sort((a, b) => a.id.localeCompare(b.id)),
  };
}

/** Dockview panel id for a manifest (+ document reference). */
export function instanceIdFor(m: Pick<PanelManifest, "id" | "singleton">, doc?: string): string {
  return m.singleton || !doc ? m.id : `${m.id}:${doc}`;
}

export type PanelParams = { panel: string; doc?: string; missing?: string };
