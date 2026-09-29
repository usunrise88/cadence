// Snapping for floating windows (docs/spec/10-ui-shell.md "Snapping for floating windows").
// Pure and framework-free: the Dockview adapter snapshots the target lines at drag start, feeds every pointer frame
// through computeSnap and applies the result. Basis: Bier & Stone, "Snap-dragging" (SIGGRAPH 1986).

export type Rect = { left: number; top: number; width: number; height: number };
export type Size = { width: number; height: number };
export type Axis = "x" | "y";
export type Probe = "start" | "centre" | "end";

/** Priority on ties: container edge (and its gutter) > window edge > centre. Lower wins. */
export type LineKind = "container" | "window" | "centre";
const PRIORITY: Record<LineKind, number> = { container: 0, window: 1, centre: 2 };

export type SnapLine = {
  axis: Axis;
  pos: number;
  kind: LineKind;
  /** Extent of the line's source on the other axis, used to draw the guide. */
  span: [number, number];
};
export type SnapLines = { x: SnapLine[]; y: SnapLine[] };
export type Capture = { line: SnapLine; probe: Probe };
export type SnapState = { x?: Capture; y?: Capture };
export type Guide = { axis: Axis; pos: number; from: number; to: number };

export type ResizeEdges = { x?: "start" | "end"; y?: "start" | "end" };

export type SnapOptions = {
  container: Size;
  /** Probe-to-line distance that captures. */
  snapDistance: number;
  /** A captured line holds until the raw probe moves this far from it. */
  releaseDistance: number;
  /** Ctrl/Cmd held or the View → Snap toggle off: no snapping, only clamping. */
  bypass: boolean;
  /** Move probes all three positions; resize probes only the moving edge(s). */
  resize?: ResizeEdges;
  /** Minimum part of the window that stays inside the container (Dockview's floatingGroupBounds). */
  minVisible: Size;
};

export const DEFAULT_SNAP = { snapDistance: 8, releaseDistance: 12, gutter: 8 } as const;

export type SnapResult = { rect: Rect; guides: Guide[]; state: SnapState };

/** Target lines for one drag: container edges, gutter lines, and every other float's edges and centres. */
export function snapLines(container: Size, others: readonly Rect[], gutter: number = DEFAULT_SNAP.gutter): SnapLines {
  const x: SnapLine[] = [];
  const y: SnapLine[] = [];
  const fullY: [number, number] = [0, container.height];
  const fullX: [number, number] = [0, container.width];
  for (const pos of [0, gutter, container.width - gutter, container.width]) {
    x.push({ axis: "x", pos, kind: "container", span: fullY });
  }
  for (const pos of [0, gutter, container.height - gutter, container.height]) {
    y.push({ axis: "y", pos, kind: "container", span: fullX });
  }
  for (const o of others) {
    const spanY: [number, number] = [o.top, o.top + o.height];
    const spanX: [number, number] = [o.left, o.left + o.width];
    x.push({ axis: "x", pos: o.left, kind: "window", span: spanY });
    x.push({ axis: "x", pos: o.left + o.width, kind: "window", span: spanY });
    x.push({ axis: "x", pos: o.left + o.width / 2, kind: "centre", span: spanY });
    y.push({ axis: "y", pos: o.top, kind: "window", span: spanX });
    y.push({ axis: "y", pos: o.top + o.height, kind: "window", span: spanX });
    y.push({ axis: "y", pos: o.top + o.height / 2, kind: "centre", span: spanX });
  }
  return { x, y };
}

function startOf(r: Rect, axis: Axis): number {
  return axis === "x" ? r.left : r.top;
}
function sizeOf(r: Rect, axis: Axis): number {
  return axis === "x" ? r.width : r.height;
}
function probePos(r: Rect, axis: Axis, p: Probe): number {
  const s = startOf(r, axis);
  const len = sizeOf(r, axis);
  return p === "start" ? s : p === "centre" ? s + len / 2 : s + len;
}

function probesFor(axis: Axis, resize: ResizeEdges | undefined): Probe[] {
  if (!resize) return ["start", "centre", "end"];
  const edge = resize[axis];
  return edge ? [edge] : [];
}

function bestCapture(raw: Rect, axis: Axis, lines: readonly SnapLine[], probes: Probe[], snapDistance: number): Capture | undefined {
  let best: { c: Capture; dist: number } | undefined;
  for (const probe of probes) {
    const at = probePos(raw, axis, probe);
    for (const line of lines) {
      const dist = Math.abs(line.pos - at);
      if (dist > snapDistance) continue;
      if (
        !best ||
        dist < best.dist ||
        (dist === best.dist && PRIORITY[line.kind] < PRIORITY[best.c.line.kind])
      ) {
        best = { c: { line, probe }, dist };
      }
    }
  }
  return best?.c;
}

function applyCapture(r: Rect, axis: Axis, c: Capture, resize: ResizeEdges | undefined): Rect {
  const delta = c.line.pos - probePos(r, axis, c.probe);
  const out = { ...r };
  if (!resize) {
    if (axis === "x") out.left += delta;
    else out.top += delta;
    return out;
  }
  // Resize: the moving edge lands on the line; the opposite edge stays put.
  if (c.probe === "end") {
    if (axis === "x") out.width += delta;
    else out.height += delta;
  } else {
    if (axis === "x") {
      out.left += delta;
      out.width -= delta;
    } else {
      out.top += delta;
      out.height -= delta;
    }
  }
  return out;
}

/** Keep the title bar reachable (top ≥ 0) and a minimum part of the window inside the container. */
export function clampRect(r: Rect, container: Size, minVisible: Size): Rect {
  const out = { ...r };
  const minW = Math.min(minVisible.width, out.width);
  const minH = Math.min(minVisible.height, out.height);
  out.left = Math.min(Math.max(out.left, minW - out.width), container.width - minW);
  out.top = Math.min(Math.max(out.top, 0), container.height - minH);
  out.top = Math.max(out.top, 0);
  return out;
}

export function computeSnap(raw: Rect, lines: SnapLines, prev: SnapState, opts: SnapOptions): SnapResult {
  if (opts.bypass) {
    return { rect: clampRect(raw, opts.container, opts.minVisible), guides: [], state: {} };
  }
  let rect = { ...raw };
  const state: SnapState = {};
  const guides: Guide[] = [];
  for (const axis of ["x", "y"] as const) {
    const probes = probesFor(axis, opts.resize);
    if (probes.length === 0) continue;
    const held = prev[axis];
    let capture: Capture | undefined;
    if (held && probes.includes(held.probe) && Math.abs(held.line.pos - probePos(raw, axis, held.probe)) <= opts.releaseDistance) {
      capture = held;
    } else {
      capture = bestCapture(raw, axis, lines[axis], probes, opts.snapDistance);
    }
    if (!capture) continue;
    rect = applyCapture(rect, axis, capture, opts.resize);
    state[axis] = capture;
  }
  rect = clampRect(rect, opts.container, opts.minVisible);
  for (const axis of ["x", "y"] as const) {
    const c = state[axis];
    if (!c) continue;
    // Clamping can pull the window off the line; then the guide would lie.
    if (Math.abs(probePos(rect, axis, c.probe) - c.line.pos) > 0.5) {
      delete state[axis];
      continue;
    }
    const other: Axis = axis === "x" ? "y" : "x";
    const selfFrom = startOf(rect, other);
    const selfTo = selfFrom + sizeOf(rect, other);
    guides.push({ axis, pos: c.line.pos, from: Math.min(selfFrom, c.line.span[0]), to: Math.max(selfTo, c.line.span[1]) });
  }
  return { rect, guides, state };
}

/**
 * Keyboard alternative (Alt+arrow): move to the nearest snap line beyond the current position on one axis.
 * Returns the rect unchanged when there is no further line in that direction.
 */
export function nextSnapPosition(r: Rect, lines: SnapLines, axis: Axis, dir: 1 | -1): Rect {
  let bestDelta: number | undefined;
  for (const probe of ["start", "centre", "end"] as const) {
    const at = probePos(r, axis, probe);
    for (const line of lines[axis]) {
      const delta = line.pos - at;
      if (dir * delta <= 0.5) continue;
      if (bestDelta === undefined || Math.abs(delta) < Math.abs(bestDelta)) bestDelta = delta;
    }
  }
  if (bestDelta === undefined) return r;
  return axis === "x" ? { ...r, left: r.left + bestDelta } : { ...r, top: r.top + bestDelta };
}
