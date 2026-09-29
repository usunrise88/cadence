// The only module allowed to touch Dockview internals (lint-enforced). Pinned to dockview-core 8.3.1; the
// contract test (dockview-adapter.test.ts) runs on every upgrade: set bounds, read them back, check toJSON().
//
// Public API used: `transformFloatingGroupDrag` (move snapping), group locations, popout events.
// Internals used: `api.component.floatingGroups[].overlay` for resize snapping, keyboard moves, focus yielding and
// reading/writing float bounds. Nothing extra is stored: final bounds reach the workspace via Dockview's toJSON().

import type { DockviewApi, DockviewGroupPanel, FloatingGroupDragContext, IDockviewGroupPanel } from "dockview-react";
import { create } from "zustand";
import {
  DEFAULT_SNAP,
  computeSnap,
  nextSnapPosition,
  snapLines,
  type Guide,
  type Rect,
  type ResizeEdges,
  type SnapLines,
  type SnapState,
} from "./computeSnap";

type Box = { left: number; top: number; width: number; height: number };

/** The part of Dockview's Overlay we rely on. */
type OverlayInternal = {
  element: HTMLElement;
  setBounds(bounds: Partial<{ top: number; left: number; width: number; height: number }>): void;
  toJSON(): { top?: number; left?: number; bottom?: number; right?: number; width: number; height: number };
  onDidChange(fn: () => void): { dispose(): void };
  onDidChangeEnd(fn: () => void): { dispose(): void };
};
type FloatingInternal = { group: DockviewGroupPanel; overlay: OverlayInternal };
type ComponentInternal = { floatingGroups: readonly FloatingInternal[]; element?: HTMLElement; gridview?: { element: HTMLElement } };

export const MIN_VISIBLE = { width: 100, height: 40 };

const ARROWS: Record<string, [-1 | 0 | 1, -1 | 0 | 1]> = { ArrowLeft: [-1, 0], ArrowRight: [1, 0], ArrowUp: [0, -1], ArrowDown: [0, 1] };

function internals(api: DockviewApi): ComponentInternal {
  return (api as unknown as { component: ComponentInternal }).component;
}

function floats(api: DockviewApi): readonly FloatingInternal[] {
  return internals(api).floatingGroups ?? [];
}

function findFloat(api: DockviewApi, group: IDockviewGroupPanel): FloatingInternal | undefined {
  return floats(api).find((f) => f.group === group || f.group.id === group.id);
}

/** Container-relative box of a float, from the overlay's anchored JSON. */
function boxOf(overlay: OverlayInternal, container: { width: number; height: number }): Box {
  const j = overlay.toJSON();
  const left = j.left ?? container.width - (j.right ?? 0) - j.width;
  const top = j.top ?? container.height - (j.bottom ?? 0) - j.height;
  return { left, top, width: j.width, height: j.height };
}

function containerSize(api: DockviewApi): { width: number; height: number } {
  return { width: api.width, height: api.height };
}

// ---------------------------------------------------------------- snap settings and guides (shell state)

type SnapUiState = {
  enabled: boolean;
  guides: Guide[];
  setEnabled(v: boolean): void;
  setGuides(g: Guide[]): void;
};

function readSnapSetting(): boolean {
  try {
    return localStorage.getItem("cadence.snap") !== "off";
  } catch {
    return true;
  }
}

export const useSnap = create<SnapUiState>((set) => ({
  enabled: readSnapSetting(),
  guides: [],
  setEnabled(v) {
    try {
      localStorage.setItem("cadence.snap", v ? "on" : "off");
    } catch {
      /* storage unavailable: the toggle still works for this session */
    }
    set({ enabled: v });
  },
  setGuides: (guides) => set({ guides }),
}));

// ---------------------------------------------------------------- move snapping (public API)

let dragState: { lines: SnapLines; state: SnapState; group: DockviewGroupPanel; start: Box } | null = null;

function endDrag(): void {
  dragState = null;
  if (useSnap.getState().guides.length) useSnap.getState().setGuides([]);
}

/** Passed to DockviewReact as `transformFloatingGroupDrag`. */
export function transformFloatingGroupDrag(ctx: FloatingGroupDragContext): { top: number; left: number } | void {
  const bypass = !useSnap.getState().enabled || ctx.modifiers.ctrlKey || ctx.modifiers.metaKey;
  if (!dragState) {
    // Lines are snapshotted once per drag: the layout does not change while dragging.
    dragState = { lines: snapLines(ctx.container, ctx.others, DEFAULT_SNAP.gutter), state: {}, group: ctx.group, start: { ...ctx.proposed } };
  }
  const raw: Rect = { left: ctx.proposed.left, top: ctx.proposed.top, width: ctx.proposed.width, height: ctx.proposed.height };
  const res = computeSnap(raw, dragState.lines, dragState.state, {
    container: ctx.container,
    snapDistance: DEFAULT_SNAP.snapDistance,
    releaseDistance: DEFAULT_SNAP.releaseDistance,
    bypass,
    minVisible: MIN_VISIBLE,
  });
  dragState.state = res.state;
  useSnap.getState().setGuides(res.guides);
  return { top: res.rect.top, left: res.rect.left };
}

// ---------------------------------------------------------------- install: resize snapping, keyboard, popouts, focus

export type AdapterOptions = {
  /** Called with each popout window so the shell can inject theme and listeners. */
  onPopoutWindow?: (win: Window) => void;
};

export function installAdapter(api: DockviewApi, root: HTMLElement, opts: AdapterOptions = {}): () => void {
  const disposers: (() => void)[] = [];
  const doc = root.ownerDocument;

  // End of a move drag (pointer released or cancelled anywhere) clears guides and snap state.
  const onPointerEnd = () => endDrag();
  doc.addEventListener("pointerup", onPointerEnd, true);
  doc.addEventListener("pointercancel", onPointerEnd, true);
  disposers.push(() => {
    doc.removeEventListener("pointerup", onPointerEnd, true);
    doc.removeEventListener("pointercancel", onPointerEnd, true);
  });

  // Resize snapping: Dockview offers no resize hook, so watch overlay changes while a resize handle is held.
  const watched = new WeakSet<OverlayInternal>();
  const watchFloats = () => {
    for (const f of floats(api)) {
      if (watched.has(f.overlay)) continue;
      watched.add(f.overlay);
      let resize: { start: Box; edges: ResizeEdges; state: SnapState; lines: SnapLines } | null = null;
      let applying = false;
      const onDown = (e: PointerEvent) => {
        const t = e.target as HTMLElement | null;
        const handle = t?.closest<HTMLElement>(".dv-resize-handle-top, .dv-resize-handle-bottom, .dv-resize-handle-left, .dv-resize-handle-right, .dv-resize-handle-topleft, .dv-resize-handle-topright, .dv-resize-handle-bottomleft, .dv-resize-handle-bottomright");
        if (!handle) return;
        const cls = handle.className;
        const edges: ResizeEdges = {};
        if (/left/.test(cls)) edges.x = "start";
        if (/right/.test(cls)) edges.x = "end";
        if (/top/.test(cls)) edges.y = "start";
        if (/bottom/.test(cls)) edges.y = "end";
        const size = containerSize(api);
        const others = floats(api).filter((o) => o !== f).map((o) => boxOf(o.overlay, size));
        resize = { start: boxOf(f.overlay, size), edges, state: {}, lines: snapLines(size, others) };
      };
      const onUp = () => {
        resize = null;
        endDrag();
      };
      f.overlay.element.addEventListener("pointerdown", onDown, true);
      doc.addEventListener("pointerup", onUp, true);
      const sub = f.overlay.onDidChange(() => {
        if (!resize || applying) return;
        const size = containerSize(api);
        const raw = boxOf(f.overlay, size);
        const bypass = !useSnap.getState().enabled;
        const res = computeSnap(raw, resize.lines, resize.state, {
          container: size,
          snapDistance: DEFAULT_SNAP.snapDistance,
          releaseDistance: DEFAULT_SNAP.releaseDistance,
          bypass,
          resize: resize.edges,
          minVisible: MIN_VISIBLE,
        });
        resize.state = res.state;
        useSnap.getState().setGuides(res.guides);
        if (res.rect.left !== raw.left || res.rect.top !== raw.top || res.rect.width !== raw.width || res.rect.height !== raw.height) {
          applying = true;
          f.overlay.setBounds({ left: res.rect.left, top: res.rect.top, width: res.rect.width, height: res.rect.height });
          applying = false;
        }
      });
      disposers.push(() => {
        sub.dispose();
        f.overlay.element.removeEventListener("pointerdown", onDown, true);
        doc.removeEventListener("pointerup", onUp, true);
      });
    }
  };
  watchFloats();
  const layoutSub = api.onDidLayoutChange(() => watchFloats());
  disposers.push(() => layoutSub.dispose());

  // Keyboard: with a floating window's header focused, arrows move it 1 px, Shift 10 px, Alt to the next snap
  // line; Esc during a pointer drag cancels it and restores the start position.
  const onKey = (e: KeyboardEvent) => {
    if (e.key === "Escape" && dragState) {
      const d = dragState;
      const f = findFloat(api, d.group);
      (f?.overlay as unknown as { cancelPendingDrag?: () => void } | undefined)?.cancelPendingDrag?.();
      f?.overlay.setBounds({ left: d.start.left, top: d.start.top });
      endDrag();
      e.preventDefault();
      return;
    }
    const dir = ARROWS[e.key];
    if (!dir || e.ctrlKey || e.metaKey) return;
    const t = e.target as HTMLElement | null;
    if (!t?.closest(".dv-tabs-and-actions-container, .dv-floating-titlebar")) return;
    const f = floats(api).find((x) => x.overlay.element.contains(t));
    if (!f) return;
    e.preventDefault();
    e.stopPropagation();
    nudgeFloat(api, f.group, dir[0], dir[1], e.altKey ? "snap" : e.shiftKey ? "10px" : "px");
  };
  doc.addEventListener("keydown", onKey, true);
  disposers.push(() => doc.removeEventListener("keydown", onKey, true));

  // Focus Not Obscured (WCAG 2.4.11): floats covering the focused element fade to 20% and ignore the pointer.
  const onFocusIn = (e: FocusEvent) => updateYield(api, e.target as Element | null);
  doc.addEventListener("focusin", onFocusIn);
  disposers.push(() => doc.removeEventListener("focusin", onFocusIn));

  // Popout windows are separate documents: the shell injects theme class and key handling into each one.
  const popSub = api.onDidAddPopoutGroup((p) => {
    // Typed as Window; some 8.x builds hand over the PopoutWindow wrapper instead.
    const raw = p.window as unknown as Window | { window: Window | null } | undefined;
    const w = raw && "document" in raw ? raw : raw?.window;
    if (w && opts.onPopoutWindow) opts.onPopoutWindow(w);
  });
  disposers.push(() => popSub.dispose());

  return () => disposers.forEach((d) => d());
}

function updateYield(api: DockviewApi, target: Element | null): void {
  for (const f of floats(api)) {
    const el = f.overlay.element;
    const covering =
      !!target &&
      target instanceof HTMLElement &&
      !el.contains(target) &&
      el.ownerDocument === target.ownerDocument &&
      intersects(el.getBoundingClientRect(), target.getBoundingClientRect());
    el.classList.toggle("cadence-float-yield", covering);
  }
}

function intersects(a: DOMRect, b: DOMRect): boolean {
  return a.left < b.right && b.left < a.right && a.top < b.bottom && b.top < a.bottom;
}

// ---------------------------------------------------------------- bounds (commands, keyboard, contract test)

export function isFloating(group: IDockviewGroupPanel | undefined): boolean {
  return group?.api.location.type === "floating";
}

export function getFloatBounds(api: DockviewApi, group: IDockviewGroupPanel): Box | undefined {
  const f = findFloat(api, group);
  return f ? boxOf(f.overlay, containerSize(api)) : undefined;
}

export function setFloatBounds(api: DockviewApi, group: IDockviewGroupPanel, bounds: Partial<Box>): boolean {
  const f = findFloat(api, group);
  if (!f) return false;
  f.overlay.setBounds(bounds);
  return true;
}

/** Keyboard alternative to dragging: arrows 1 px, Shift 10 px, Alt to the next snap line. */
export function nudgeFloat(api: DockviewApi, group: IDockviewGroupPanel, dx: -1 | 0 | 1, dy: -1 | 0 | 1, mode: "px" | "10px" | "snap"): boolean {
  const f = findFloat(api, group);
  if (!f) return false;
  const size = containerSize(api);
  const cur = boxOf(f.overlay, size);
  let next: Box;
  if (mode === "snap") {
    const others = floats(api).filter((o) => o !== f).map((o) => boxOf(o.overlay, size));
    const lines = snapLines(size, others);
    next = dx !== 0 ? nextSnapPosition(cur, lines, "x", dx) : nextSnapPosition(cur, lines, "y", dy === 0 ? 1 : dy);
  } else {
    const step = mode === "px" ? 1 : 10;
    next = { ...cur, left: cur.left + dx * step, top: cur.top + dy * step };
  }
  next.top = Math.max(0, next.top);
  f.overlay.setBounds({ left: next.left, top: next.top });
  f.overlay.element.dispatchEvent(new Event("cadence-moved"));
  return true;
}

/** Align a float with a container edge (inside the gutter). */
export function alignFloat(api: DockviewApi, group: IDockviewGroupPanel, edge: "left" | "right" | "top" | "bottom"): boolean {
  const f = findFloat(api, group);
  if (!f) return false;
  const size = containerSize(api);
  const cur = boxOf(f.overlay, size);
  const g = DEFAULT_SNAP.gutter;
  const pos = {
    left: { left: g },
    right: { left: size.width - g - cur.width },
    top: { top: g },
    bottom: { top: size.height - g - cur.height },
  }[edge];
  f.overlay.setBounds(pos);
  return true;
}

export function floatingGroupCount(api: DockviewApi): number {
  return floats(api).length;
}
