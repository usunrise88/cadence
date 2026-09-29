import { describe, expect, it } from "vitest";
import { clampRect, computeSnap, nextSnapPosition, snapLines, type Rect, type SnapOptions } from "./computeSnap";

const container = { width: 1000, height: 800 };
const opts: SnapOptions = { container, snapDistance: 8, releaseDistance: 12, bypass: false, minVisible: { width: 100, height: 40 } };
const win = (left: number, top: number, width = 200, height = 100): Rect => ({ left, top, width, height });
const other = win(500, 300, 200, 100);
const lines = snapLines(container, [other]);

describe("computeSnap: targets", () => {
  it.each([
    ["container left edge", win(2, 200), { left: 0 }],
    ["gutter line inside the left edge", win(11, 200), { left: 8 }],
    ["container right edge", win(798, 200), { left: 800 }],
    ["gutter inside the right edge", win(787, 200), { left: 792 }],
    ["container top edge", win(300, 2), { top: 0 }],
    ["container bottom gutter", win(300, 687), { top: 692 }],
    ["flush to another window's left edge", win(296, 150), { left: 300 }],
    ["flush to another window's right edge", win(705, 150), { left: 700 }],
    ["aligned with another window's left edge", win(503, 500), { left: 500 }],
    ["aligned with another window's top edge", win(100, 296), { top: 300 }],
    ["centre to centre", win(547, 100, 100, 100), { left: 550 }],
  ])("%s", (_name, raw, want) => {
    const { rect, guides } = computeSnap(raw, lines, {}, opts);
    expect(rect).toMatchObject(want);
    expect(guides.length).toBeGreaterThan(0);
  });

  it("does not snap beyond the snap distance", () => {
    const raw = win(150, 150);
    const { rect, guides, state } = computeSnap(raw, lines, {}, opts);
    expect(rect).toEqual(raw);
    expect(guides).toEqual([]);
    expect(state).toEqual({});
  });

  it("resolves the two axes independently", () => {
    const { rect, state } = computeSnap(win(296, 402), lines, {}, opts);
    expect(rect.left).toBe(300); // flush to the other's left edge on x
    expect(rect.top).toBe(400); // aligned to the other's bottom edge on y
    expect(state.x?.line.kind).toBe("window");
    expect(state.y?.line.kind).toBe("window");
  });

  it("draws at most one guide per axis", () => {
    const { guides } = computeSnap(win(296, 402), lines, {}, opts);
    expect(guides.filter((g) => g.axis === "x")).toHaveLength(1);
    expect(guides.filter((g) => g.axis === "y")).toHaveLength(1);
  });
});

describe("computeSnap: priority and hysteresis", () => {
  it("breaks ties by priority: container edge beats window edge", () => {
    // A window whose left edge sits exactly on the gutter line; the dragged window is equally close to both lines.
    const tie = snapLines(container, [win(8, 500, 100, 100)]);
    const { state } = computeSnap(win(12, 100), tie, {}, opts);
    expect(state.x?.line.kind).toBe("container");
  });

  it("breaks ties by priority: window edge beats centre", () => {
    const a = win(100, 500, 100, 100); // right edge at 200
    const b = win(150, 650, 100, 100); // centre at 200
    const { state } = computeSnap(win(204, 300), snapLines(container, [a, b]), {}, opts);
    expect(state.x?.line.kind).toBe("window");
  });

  it("closest line wins before priority", () => {
    const { rect } = computeSnap(win(302, 150), lines, {}, opts); // 2 px from the other's left edge (300)
    expect(rect.left).toBe(300);
  });

  it("holds a captured line until the release distance", () => {
    const first = computeSnap(win(2, 200), lines, {}, opts);
    expect(first.rect.left).toBe(0);
    // 10 px away from the held line: within the 12 px release distance → still held, although the gutter is closer.
    const held = computeSnap(win(10, 200), lines, first.state, opts);
    expect(held.rect.left).toBe(0);
    const released = computeSnap(win(40, 200), lines, first.state, opts);
    expect(released.rect.left).toBe(40);
    expect(released.state.x).toBeUndefined();
  });

  it("keeps the held line while within release distance even when another line is in range", () => {
    const first = computeSnap(win(3, 200), lines, {}, opts);
    expect(first.state.x?.line.pos).toBe(0);
    const next = computeSnap(win(6, 200), lines, first.state, opts); // gutter (8) is 2 px away, held line 0 is 6 px away
    expect(next.rect.left).toBe(0);
  });
});

describe("computeSnap: bypass and clamping", () => {
  it("bypass returns the raw position (clamped), no guides", () => {
    const raw = win(3, 200);
    const { rect, guides, state } = computeSnap(raw, lines, {}, { ...opts, bypass: true });
    expect(rect).toEqual(raw);
    expect(guides).toEqual([]);
    expect(state).toEqual({});
  });

  it("clamps the top edge at 0 so the title bar stays reachable", () => {
    expect(computeSnap(win(300, -50), lines, {}, opts).rect.top).toBe(0);
    expect(computeSnap(win(300, -50), lines, {}, { ...opts, bypass: true }).rect.top).toBe(0);
  });

  it("keeps a minimum visible area inside the container", () => {
    const r = clampRect(win(980, 790), container, { width: 100, height: 40 });
    expect(r.left).toBe(900);
    expect(r.top).toBe(760);
    const l = clampRect(win(-500, 100), container, { width: 100, height: 40 });
    expect(l.left).toBe(-100);
  });

  it("drops the capture when clamping pulls the window off the line", () => {
    // Bottom edge of the container is a line; a window snapped there by its top would be clamped back.
    const { rect, state, guides } = computeSnap(win(300, 796), lines, {}, opts);
    expect(rect.top).toBe(760);
    expect(state.y).toBeUndefined();
    expect(guides.filter((g) => g.axis === "y")).toEqual([]);
  });
});

describe("computeSnap: resize", () => {
  it("probes only the moving edge", () => {
    // Resizing the right edge to 703: snaps to the other's right edge (700); the left edge stays.
    const { rect } = computeSnap({ left: 100, top: 100, width: 603, height: 100 }, lines, {}, { ...opts, resize: { x: "end" } });
    expect(rect.left).toBe(100);
    expect(rect.width).toBe(600);
  });

  it("resizing the start edge moves left and changes width", () => {
    const { rect } = computeSnap({ left: 4, top: 100, width: 300, height: 100 }, lines, {}, { ...opts, resize: { x: "start" } });
    expect(rect.left).toBe(0);
    expect(rect.width).toBe(304);
  });

  it("does not snap the axis that is not being resized", () => {
    const { rect } = computeSnap({ left: 100, top: 3, width: 603, height: 100 }, lines, {}, { ...opts, resize: { x: "end" } });
    expect(rect.top).toBe(3);
  });
});

describe("nextSnapPosition", () => {
  it("moves to the next line in the direction", () => {
    const r = nextSnapPosition(win(100, 100), lines, "x", 1);
    // probes 100, 200, 300; the nearest line beyond any probe is the other's left (500) from the end probe
    expect(r.left).toBe(300);
  });
  it("moves backwards", () => {
    expect(nextSnapPosition(win(100, 100), lines, "x", -1).left).toBe(8);
  });
  it("stays put without a further line", () => {
    const r = win(800, 100);
    expect(nextSnapPosition(r, { x: [], y: [] }, "x", 1)).toEqual(r);
  });
});
