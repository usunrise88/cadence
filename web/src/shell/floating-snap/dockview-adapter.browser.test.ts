import "dockview-react/dist/styles/dockview.css";
import { afterEach, describe, expect, it } from "vitest";
import { headlessDockview } from "@/shell/workspaces/testkit";
import { alignFloat, floatingGroupCount, getFloatBounds, isFloating, nudgeFloat, setFloatBounds, transformFloatingGroupDrag, useSnap } from "./dockview-adapter";

// Contract test against the pinned Dockview (8.3.1): the internals the adapter relies on must keep working.
// Runs on every Dockview upgrade (CI "contract" layer).

describe("dockview adapter contract", () => {
  let dispose: (() => void) | undefined;
  afterEach(() => dispose?.());

  function withFloat() {
    const d = headlessDockview(1000, 800);
    dispose = d.dispose;
    d.api.addPanel({ id: "a", component: "panel" });
    d.api.addPanel({ id: "f", component: "panel", floating: { x: 100, y: 100, width: 300, height: 200 } });
    const group = d.api.getPanel("f")!.group;
    return { api: d.api, group };
  }

  it("finds floating groups", () => {
    const { api, group } = withFloat();
    expect(isFloating(group)).toBe(true);
    expect(floatingGroupCount(api)).toBe(1);
  });

  it("sets bounds, reads them back, and toJSON() carries them", () => {
    const { api, group } = withFloat();
    // Sizes read back include the overlay's 1 px border (Dockview 8.3.1 measures the element's rect).
    const b = getFloatBounds(api, group)!;
    expect(b).toMatchObject({ left: 100, top: 100 });
    expect(b.width - 300).toBeGreaterThanOrEqual(0);
    expect(b.width - 300).toBeLessThanOrEqual(2);
    expect(setFloatBounds(api, group, { left: 50, top: 60 })).toBe(true);
    expect(getFloatBounds(api, group)).toMatchObject({ left: 50, top: 60 });
    expect(api.toJSON().floatingGroups?.[0]?.position).toMatchObject({ left: 50, top: 60, width: b.width, height: b.height });
  });

  it("keyboard nudges by 1 and 10 px and never above the top edge", () => {
    const { api, group } = withFloat();
    nudgeFloat(api, group, 1, 0, "px");
    expect(getFloatBounds(api, group)).toMatchObject({ left: 101, top: 100 });
    nudgeFloat(api, group, 0, 1, "10px");
    expect(getFloatBounds(api, group)).toMatchObject({ left: 101, top: 110 });
    setFloatBounds(api, group, { top: 3 });
    nudgeFloat(api, group, 0, -1, "10px");
    expect(getFloatBounds(api, group)?.top).toBe(0);
  });

  it("Alt+arrow jumps to the next snap line", () => {
    const { api, group } = withFloat();
    nudgeFloat(api, group, -1, 0, "snap");
    expect(getFloatBounds(api, group)?.left).toBe(8); // the gutter line
  });

  it("aligns to a container edge inside the gutter", () => {
    const { api, group } = withFloat();
    const w = getFloatBounds(api, group)!.width;
    alignFloat(api, group, "right");
    expect(getFloatBounds(api, group)?.left).toBe(1000 - 8 - w);
  });

  it("the public drag hook snaps and bypasses with Ctrl/Cmd", () => {
    useSnap.getState().setEnabled(true);
    const ctx = (left: number, mods: Partial<Record<"ctrlKey" | "metaKey" | "altKey" | "shiftKey", boolean>> = {}) => ({
      group: undefined as never,
      proposed: { left, top: 200, width: 300, height: 200 },
      container: { width: 1000, height: 800 },
      others: [],
      modifiers: { ctrlKey: false, metaKey: false, altKey: false, shiftKey: false, ...mods },
    });
    expect(transformFloatingGroupDrag(ctx(3))).toEqual({ left: 0, top: 200 });
    window.dispatchEvent(new Event("pointerup"));
    document.dispatchEvent(new Event("pointerup"));
    expect(transformFloatingGroupDrag(ctx(3, { ctrlKey: true }))).toEqual({ left: 3, top: 200 });
  });
});
