import { useEffect, useLayoutEffect, useRef, useState } from "react";
import { usePortalContainer } from "@/lib/portal";
import { useTheme } from "@/shell/theme/store";
import { readChartTheme, type ChartTheme } from "./tokens";

/**
 * The document that owns the panel. Dockview moves a group's DOM into a popout window without re-rendering it; the
 * shell re-provides the portal container when that happens, so observers bound to a window re-bind here.
 */
export function useOwnerDocument(el: HTMLElement | null): Document | null {
  const container = usePortalContainer();
  return container?.ownerDocument ?? el?.ownerDocument ?? null;
}

/** Chart tokens as the element sees them, re-read when the theme switches (no chart is re-created). */
export function useChartTheme(el: HTMLElement | null, doc: Document | null): ChartTheme | null {
  const dark = useTheme((s) => s.dark);
  const [theme, setTheme] = useState<ChartTheme | null>(null);
  useLayoutEffect(() => {
    if (el) setTheme(readChartTheme(el));
  }, [el, doc, dark]);
  // A popout's theme class can change without the store (installTheme per window): watch the root class too.
  useEffect(() => {
    const root = (doc ?? el?.ownerDocument)?.documentElement;
    const MO = root?.ownerDocument.defaultView?.MutationObserver;
    if (!root || !MO || !el) return;
    const mo = new MO(() => setTheme(readChartTheme(el)));
    mo.observe(root, { attributes: true, attributeFilter: ["class"] });
    return () => mo.disconnect();
  }, [el, doc]);
  return theme;
}

/** Content-box size of an element, observed with its own window's ResizeObserver (popouts have their own). */
export function useElementSize(el: HTMLElement | null, doc: Document | null): { width: number; height: number } {
  const [size, setSize] = useState({ width: 0, height: 0 });
  useLayoutEffect(() => {
    if (!el) return;
    const view = (doc ?? el.ownerDocument).defaultView ?? window;
    const measure = () => {
      const r = el.getBoundingClientRect();
      const width = Math.floor(r.width);
      const height = Math.floor(r.height);
      setSize((s) => (s.width === width && s.height === height ? s : { width, height }));
    };
    measure();
    const RO = view.ResizeObserver ?? (typeof ResizeObserver !== "undefined" ? ResizeObserver : undefined);
    if (!RO) return;
    const ro = new RO(measure);
    ro.observe(el);
    return () => ro.disconnect();
  }, [el, doc]);
  return size;
}

/** A ref that always holds the latest value (for callbacks registered once with a library). */
export function useLatest<T>(value: T): { readonly current: T } {
  const ref = useRef(value);
  useLayoutEffect(() => {
    ref.current = value;
  });
  return ref;
}
