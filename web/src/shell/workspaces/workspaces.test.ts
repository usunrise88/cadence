import { afterEach, describe, expect, it } from "vitest";
import type { SerializedDockview } from "dockview-core";
import { planDefaultLayout } from "./defaults";
import { DEFAULT_WORKSPACES, migrate, normalizeLayout, parseWorkspace, WorkspaceSchemaError, type WorkspaceData } from "./schema";
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

describe("default workspaces", () => {
  it.each(DEFAULT_WORKSPACES)("%s opens only registered panels, centre first", (name) => {
    const plan = planDefaultLayout(name, registry, "demo");
    expect(plan[0]).toEqual({ panel: "project", location: "centre", doc: "project:demo" });
    for (const p of plan) expect(registry.get(p.panel), p.panel).toBeDefined();
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
    expect(Object.keys(first.panels).sort()).toEqual(["library", "old-metrics", "project:project:demo", "properties"]);
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
