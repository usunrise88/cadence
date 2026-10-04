import { afterEach, describe, expect, it } from "vitest";
import type { SerializedDockview } from "dockview-core";
import { planDefaultLayout } from "./defaults";
import { addChatToRightColumn, DEFAULT_WORKSPACES, dropOnDemandFloats, isPlaceholderLayout, migrate, normalizeLayout, parseWorkspace, WorkspaceSchemaError, type WorkspaceData } from "./schema";
import legacy from "./fixtures/legacy-training.json";
import { headlessDockview, phase0Registry } from "./testkit";

const registry = phase0Registry();

describe("migrate", () => {
  const base: WorkspaceData = { schemaVersion: 1, name: "W", layout: {}, panels: {} };

  it("runs the chain in order up to the target", () => {
    const seen: number[] = [];
    const chain = {
      1: (w: WorkspaceData) => (seen.push(1), { ...w, name: `${w.name}+1` }),
      2: (w: WorkspaceData) => (seen.push(2), { ...w, name: `${w.name}+2` }),
    };
    const out = migrate(base, chain, 3);
    expect(seen).toEqual([1, 2]);
    expect(out).toMatchObject({ schemaVersion: 3, name: "W+1+2" });
  });

  it("refuses a schema newer than the app", () => {
    expect(() => migrate({ ...base, schemaVersion: 9 })).toThrow(WorkspaceSchemaError);
  });

  it("refuses a gap in the chain", () => {
    expect(() => migrate(base, {}, 2)).toThrow(/no migration from workspace schema 1/);
  });

  it("refuses a missing schema version", () => {
    expect(() => migrate(parseWorkspace({ name: "x", layout: {} }))).toThrow(WorkspaceSchemaError);
  });
});

describe("schema 2: Chat in the right column", () => {
  type Leaf = { type: "leaf"; data: { views: string[]; activeView: string } };
  const leaves = (w: WorkspaceData) => ((w.layout.grid as { root: { data: Leaf[] } }).root.data as Leaf[]).map((l) => l.data);

  it("adds Chat as an inactive tab of the right-column group of a layout saved before it existed", () => {
    const ws = migrate(parseWorkspace(legacy));
    expect(ws.schemaVersion).toBe(3);
    expect(ws.layout.panels?.chat).toEqual({ id: "chat", contentComponent: "panel", title: "Chat", params: { panel: "chat", loc: "right" } });
    const right = leaves(ws).find((l) => l.views.includes("properties"))!;
    expect(right.views).toEqual(["properties", "old-metrics", "chat"]);
    expect(right.activeView).toBe("properties");
    expect(leaves(ws).filter((l) => l.views.includes("chat"))).toHaveLength(1);
    expect(ws.panels).toEqual(parseWorkspace(legacy).panels); // pins (and a Chat's session) are kept
  });

  it("leaves placeholders, layouts with a Chat and layouts without a right column alone", () => {
    const placeholder = parseWorkspace({ name: "Ops", schemaVersion: 1, layout: {}, panels: {} });
    expect(migrate(placeholder).layout).toEqual({});
    const once = migrate(parseWorkspace(legacy));
    expect(addChatToRightColumn(once.layout)).toBe(once.layout);
    const noRight = { ...legacy.layout, panels: { library: legacy.layout.panels.library } };
    expect(addChatToRightColumn(noRight as WorkspaceData["layout"])).toBe(noRight);
  });
});

describe("schema 3: no empty floating Audio over the documents", () => {
  const floatAudio = (extra: string[] = []) => ({
    grid: { root: { type: "branch", data: [{ type: "leaf", data: { views: ["project:project:demo"], activeView: "project:project:demo", id: "1" } }] } },
    panels: {
      "project:project:demo": { id: "project:project:demo", params: { panel: "project", doc: "project:demo" } },
      audio: { id: "audio", params: { panel: "audio", loc: "floating" } },
      ...Object.fromEntries(extra.map((id) => [id, { id, params: { panel: id, loc: "floating" } }])),
    },
    floatingGroups: [{ data: { views: ["audio", ...extra], activeView: "audio", id: "2" }, position: { left: 120, top: 200, width: 760, height: 380 } }],
    activeGroup: "2",
  });

  it("takes the Audio float out of a layout saved before schema 3, and the window it leaves empty", () => {
    const ws = migrate({ schemaVersion: 2, name: "Data", layout: floatAudio(), panels: {} });
    expect(ws.schemaVersion).toBe(3);
    expect(ws.layout.floatingGroups).toEqual([]);
    expect(Object.keys(ws.layout.panels ?? {})).toEqual(["project:project:demo"]);
    expect(ws.layout.activeGroup).toBeUndefined();
  });

  it("keeps the other panels of a shared floating window, and docked or placeholder layouts as they are", () => {
    const out = dropOnDemandFloats(floatAudio(["help"]));
    expect(out.floatingGroups).toEqual([{ data: { views: ["help"], activeView: "help", id: "2" }, position: { left: 120, top: 200, width: 760, height: 380 } }]);
    expect(out.activeGroup).toBe("2");
    const docked = { ...floatAudio(), floatingGroups: [] };
    expect(dropOnDemandFloats(docked)).toBe(docked);
    expect(dropOnDemandFloats({})).toEqual({});
  });
});

describe("normalizeLayout", () => {
  it("resolves renamed panels through the alias map and turns unknown ones into placeholders", () => {
    const ws = parseWorkspace(legacy);
    const { layout, renamed, missing } = normalizeLayout(ws.layout, registry);
    expect(renamed).toEqual(["properties"]);
    expect(missing).toEqual(["metrics-legacy"]);
    expect(layout.panels?.properties?.params?.panel).toBe("inspector");
    expect(layout.panels?.["old-metrics"]?.params).toEqual({ panel: "placeholder", missing: "metrics-legacy" });
    expect(layout.panels?.library).toEqual(ws.layout.panels?.library);
  });
});

describe("placeholder workspaces from the project bootstrap", () => {
  it("treats a stored layout without a grid as the default plan", () => {
    const ws = parseWorkspace({ name: "Training", schemaVersion: 1, layout: {}, panels: {}, rev: 1 });
    expect(isPlaceholderLayout(ws.layout)).toBe(true);
    expect(isPlaceholderLayout({ grid: { root: {}, width: 1, height: 1, orientation: "HORIZONTAL" }, panels: {} })).toBe(false);
  });
});

describe("default workspaces", () => {
  it.each(DEFAULT_WORKSPACES)("%s opens only registered panels, centre first", (name) => {
    const plan = planDefaultLayout(name, registry, "demo");
    expect(plan[0]).toEqual({ panel: "project", location: "centre", doc: "project:demo" });
    for (const p of plan) expect(registry.get(p.panel), p.panel).toBeDefined();
  });

  it("phase-1 panels join their columns: Approvals in Ops right, Getting started in Training right until dismissed", () => {
    const r = phase0Registry();
    let dismissed = false;
    r.register({ ...r.get("help")!, id: "approvals", help: "panels.approvals" });
    r.register({ ...r.get("help")!, id: "getting-started", help: "panels.getting-started", inDefaults: () => !dismissed });
    expect(planDefaultLayout("Ops", r, "demo")).toContainEqual({ panel: "approvals", location: "right" });
    expect(planDefaultLayout("Training", r, "demo").at(-1)).toEqual({ panel: "getting-started", location: "right" });
    dismissed = true;
    expect(planDefaultLayout("Training", r, "demo").map((p) => p.panel)).not.toContain("getting-started");
  });

  it("opens no floating panel: Audio waits in its slot until something opens it (11 'Default workspaces')", () => {
    const r = phase0Registry();
    r.register({ ...r.get("help")!, id: "audio", help: "panels.audio", defaultLocation: "floating" });
    for (const name of DEFAULT_WORKSPACES) {
      const plan = planDefaultLayout(name, r, "demo");
      expect(plan.map((p) => p.panel), name).not.toContain("audio");
      expect(plan.every((p) => p.location !== "floating"), name).toBe(true);
    }
  });

  it("phase-2 panels join their columns; Metrics and Checkpoints take their Training slots once registered", () => {
    const r = phase0Registry();
    const tool = (id: string) => r.register({ ...r.get("help")!, id, help: `panels.${id}` });
    ["logs", "queue-gpu", "pipeline-run"].forEach(tool);
    expect(planDefaultLayout("Training", r, "demo")).toContainEqual({ panel: "logs", location: "bottom" });
    expect(planDefaultLayout("Data", r, "demo")).toEqual(expect.arrayContaining([{ panel: "pipeline-run", location: "right" }, { panel: "logs", location: "bottom" }]));
    expect(planDefaultLayout("Ops", r, "demo")).toEqual(expect.arrayContaining([{ panel: "queue-gpu", location: "left" }, { panel: "logs", location: "bottom" }]));
    // Before the Run, Metrics and Checkpoints panels exist the plan skips their slots…
    const before = planDefaultLayout("Training", r, "demo").map((p) => p.panel);
    expect(before).not.toContain("metrics");
    expect(before).not.toContain("checkpoints");
    // …and fills them when they register, with no workspace migration: defaults are code, not stored JSON.
    ["metrics", "checkpoints"].forEach(tool);
    const after = planDefaultLayout("Training", r, "demo");
    expect(after).toEqual(expect.arrayContaining([{ panel: "checkpoints", location: "right" }, { panel: "metrics", location: "bottom" }]));
    expect(after.findIndex((p) => p.panel === "metrics")).toBeLessThan(after.findIndex((p) => p.panel === "logs"));
  });
});

describe("Dockview round-trip (runs on every Dockview upgrade)", () => {
  let dispose: (() => void) | undefined;
  afterEach(() => dispose?.());

  function roundTrip(layout: SerializedDockview): { first: SerializedDockview; second: SerializedDockview } {
    const a = headlessDockview();
    dispose = a.dispose;
    a.api.fromJSON(layout);
    const first = a.api.toJSON();
    a.api.fromJSON(first);
    const second = a.api.toJSON();
    return { first, second };
  }

  it("restores the legacy fixture after migration and keeps it stable", () => {
    const ws = migrate(parseWorkspace(legacy));
    const { layout } = normalizeLayout(ws.layout, registry);
    const { first, second } = roundTrip(layout as unknown as SerializedDockview);
    expect(second).toEqual(first);
    // Schema 2 added Chat to the right column.
    expect(Object.keys(first.panels).sort()).toEqual(["chat", "library", "old-metrics", "project:project:demo", "properties"]);
  });

  it.each(DEFAULT_WORKSPACES)("default %s survives fromJSON(toJSON())", (name) => {
    const a = headlessDockview();
    for (const p of planDefaultLayout(name, registry, "demo")) {
      const id = p.doc ? `${p.panel}:${p.doc}` : p.panel;
      a.api.addPanel({ id, component: "panel", params: { panel: p.panel, doc: p.doc } });
    }
    const layout = a.api.toJSON();
    a.dispose();
    const { first, second } = roundTrip(layout);
    expect(second).toEqual(first);
    expect(Object.keys(first.panels).length).toBe(Object.keys(layout.panels).length);
  });

});
