import "dockview-react/dist/styles/dockview.css";
import { describe, expect, it } from "vitest";
import type { SerializedDockview } from "dockview-core";
import { migrate, type SerializedLayout } from "./schema";
import { headlessDockview } from "./testkit";

// The shell's rule (styles/shell.css): floating overlays are border-box. With content-box, Dockview 8.3.1 grows a
// float by its border width on every fromJSON(toJSON()) round-trip (found in spike S4).
const style = document.createElement("style");
style.textContent = ".dv-resize-container { box-sizing: border-box; }";
document.head.appendChild(style);

// Floating bounds need real layout: runs in headless Chromium (vitest browser project).
describe("Dockview round-trip with floating groups", () => {
  it("keeps floating groups and their bounds across repeated round-trips", () => {
    const a = headlessDockview();
    a.api.addPanel({ id: "library", component: "panel", params: { panel: "library" } });
    a.api.addPanel({ id: "help", component: "panel", params: { panel: "help" }, floating: { x: 120, y: 80, width: 300, height: 200 } });
    const layout = a.api.toJSON();
    a.dispose();
    const b = headlessDockview();
    b.api.fromJSON(layout);
    const first = b.api.toJSON();
    b.api.fromJSON(first);
    const second = b.api.toJSON();
    b.dispose();
    expect(second.floatingGroups).toEqual(first.floatingGroups);
    expect(first.floatingGroups?.[0]?.position).toMatchObject({ left: 120, top: 80 });
    expect(first.floatingGroups?.[0]?.position).toEqual(layout.floatingGroups?.[0]?.position);
  });
});

// Schema 3 (the annotation e2e's finding): a stored Data, Eval or Triage workspace kept an empty Audio float over the
// centre documents. Migrated, the layout restores in a real Dockview with the document uncovered.
describe("schema 3 on a real Dockview layout", () => {
  it("restores a pre-schema-3 workspace without the Audio float and with the document in place", () => {
    const a = headlessDockview(1200, 800);
    a.api.addPanel({ id: "library", component: "panel", params: { panel: "library", loc: "left" } });
    a.api.addPanel({ id: "annotation-batch:anb_1", component: "panel", params: { panel: "annotation-batch", doc: "annotation-batch:anb_1" }, position: { direction: "right" } });
    a.api.addPanel({ id: "audio", component: "panel", params: { panel: "audio", loc: "floating" }, floating: { x: 220, y: 140, width: 760, height: 380 } });
    const stored = { schemaVersion: 2, name: "Data", layout: a.api.toJSON() as unknown as SerializedLayout, panels: {} };
    a.dispose();
    expect(stored.layout.floatingGroups).toHaveLength(1);

    const ws = migrate(stored);
    const b = headlessDockview(1200, 800);
    b.api.fromJSON(ws.layout as unknown as SerializedDockview);
    expect(b.api.groups.filter((g) => g.api.location.type === "floating")).toHaveLength(0);
    expect(b.api.getPanel("audio")).toBeUndefined();
    const doc = b.api.getPanel("annotation-batch:anb_1");
    expect(doc?.group.api.location.type).toBe("grid");
    // The same layout round-trips unchanged.
    const first = b.api.toJSON();
    b.api.fromJSON(first);
    expect(b.api.toJSON()).toEqual(first);
    b.dispose();
  });
});
