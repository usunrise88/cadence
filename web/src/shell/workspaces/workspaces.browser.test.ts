import "dockview-react/dist/styles/dockview.css";
import { describe, expect, it } from "vitest";
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
