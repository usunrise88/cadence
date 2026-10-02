import type { IconoirIcon } from "@/shell/registry/panels";
import type { QueryClient } from "@tanstack/react-query";
import type { CadenceEvent, Presence } from "@/api/gen/types.gen";
import type { Verb } from "@/api/operations.gen";

// docs/spec/10-ui-shell.md "Uniform workflow": every entity is worked through the same loop, rendered from one
// manifest by shared primitives (EntityHeader, ActionBar, StatusChip, NextStep).

export type EntityKind = "project" | (string & {});

/** The six loop steps; the stepper in every document header shows where the entity is. */
export const LOOP_STEPS = ["prepare", "check", "run", "review", "decide", "record"] as const;
export type LoopStep = (typeof LOOP_STEPS)[number];

/** State templates. Every kind uses one of them; `container` covers projects (see 07 open questions). */
export const STATE_TEMPLATES = {
  registry: ["draft", "frozen", "deprecated"],
  work: ["planned", "queued", "running", "paused", "done", "failed", "cancelled"],
  promotion: ["proposed", "approved", "denied", "applied", "rolled back"],
  container: ["bootstrapping", "active", "failed", "archived"],
} as const;
export type StateTemplate = keyof typeof STATE_TEMPLATES;

export type StatusTone = "neutral" | "running" | "done" | "warning" | "failed";

/** How a state reads as a status chip. */
export const STATE_TONES: Record<string, StatusTone> = {
  draft: "neutral",
  frozen: "done",
  deprecated: "warning",
  planned: "neutral",
  queued: "neutral",
  running: "running",
  paused: "warning",
  done: "done",
  failed: "failed",
  cancelled: "neutral",
  proposed: "neutral",
  approved: "done",
  denied: "failed",
  applied: "done",
  "rolled back": "warning",
  active: "done",
  archived: "neutral",
  bootstrapping: "running",
};

export type EntityVerb = {
  verb: Verb;
  /**
   * The command the button runs when it is another entity's operation (an Experiment's "Run sweep" is sweeps.run,
   * "Register best" models.register); default `<apiEntity>.<verb>`. The verb still picks the icon.
   */
  command?: string;
  /** The primary action in the header; exactly one per manifest. */
  primary?: boolean;
  /** Enabled, or the reason it is not (shown as the disabled button's tooltip). */
  enabled?: (e: EntityData) => true | string;
};

export type FactSpec = { label: string; value: (e: EntityData) => string };

export type Suggestion = { step: LoopStep; title: string; command?: string };

/** The fields every entity row carries; kinds add their own. */
export type EntityData = {
  id: string;
  name: string;
  state: string;
  rev?: number;
  version?: string;
  updatedAt?: string;
  /** Who made the current revision; an agent shows as its session badge. */
  actor?: { kind: string; id: string; name?: string; sessionId?: string };
  /** The agent tool call behind the current revision (the badge's click-through). */
  toolCallId?: string;
  /** Agents editing the entity now (draftable kinds); the header shows "agent editing". */
  presence?: Presence[];
  [field: string]: unknown;
};

export type EntityManifest = {
  kind: EntityKind;
  /** The API entity (operationId prefix) whose operations the verbs map to. */
  apiEntity: string;
  layer: "registry" | "project";
  template: StateTemplate;
  verbs: EntityVerb[];
  facts: FactSpec[]; // the four header facts
  comparable: boolean;
  draftable: boolean;
  loopStep: (e: EntityData) => LoopStep;
  nextStep: (e: EntityData) => Suggestion;
  icon: IconoirIcon;
  /**
   * Live updates of one open document: the topics its events arrive on and how they patch the query cache. The
   * shell subscribes while the document is visible and, for draftable kinds, patches its drafts too.
   */
  live?: { topics: (id: string) => string[]; patch: (qc: QueryClient, batch: CadenceEvent[], id: string) => void };
  /** Loads one entity through the generated query layer (a React hook). */
  useData: (id: string) => { data?: EntityData; error?: unknown; isLoading: boolean };
};

export type EntityRegistry = {
  register(m: EntityManifest): void;
  get(kind: EntityKind): EntityManifest | undefined;
  all(): EntityManifest[];
};

export function createEntityRegistry(): EntityRegistry {
  const byKind = new Map<string, EntityManifest>();
  return {
    register(m) {
      if (byKind.has(m.kind)) throw new Error(`entity "${m.kind}" registered twice`);
      if (m.facts.length > 4) throw new Error(`entity "${m.kind}": at most four header facts`);
      const primaries = m.verbs.filter((v) => v.primary).length;
      if (primaries > 1) throw new Error(`entity "${m.kind}": exactly one primary action`);
      byKind.set(m.kind, m);
    },
    get: (kind) => byKind.get(kind),
    all: () => [...byKind.values()],
  };
}

/** `kind:id` ↔ parts. Ids may contain colons; kinds never do. */
export function parseDocRef(ref: string): { kind: string; id: string } | undefined {
  const i = ref.indexOf(":");
  if (i <= 0 || i === ref.length - 1) return undefined;
  return { kind: ref.slice(0, i), id: ref.slice(i + 1) };
}

export function docRef(kind: string, id: string): string {
  return `${kind}:${id}`;
}
