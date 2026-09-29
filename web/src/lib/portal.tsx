import { createContext, useCallback, useContext, useState } from "react";

// Portals (popovers, menus, tooltips, dialogs) must render into the document that owns the panel: a panel in a
// popout window has its own document. The shell provides the container per panel; outside a panel it is the
// main document's body (Base UI's default when undefined).
export const PortalContainerContext = createContext<HTMLElement | null>(null);

export function usePortalContainer(): HTMLElement | undefined {
  return useContext(PortalContainerContext) ?? undefined;
}

/**
 * A callback ref plus the body of the document the element lives in, for components rendered by Dockview outside
 * a panel frame (tabs, header actions) that may sit in a popout window.
 */
export function useOwnerBody(): [(el: HTMLElement | null) => void, HTMLElement | null] {
  const [body, setBody] = useState<HTMLElement | null>(null);
  const ref = useCallback((el: HTMLElement | null) => {
    const b = el?.ownerDocument.body ?? null;
    setBody((cur) => (cur === b ? cur : b));
  }, []);
  return [ref, body];
}
