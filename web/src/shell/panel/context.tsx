import { createContext, useContext, useEffect, useRef } from "react";
import type { CadenceEvent } from "@/api/gen/types.gen";
import { events } from "@/shell/registries";

// What a panel may know about itself. Panels get everything through these hooks, never from Dockview.

export type PanelContextValue = {
  instanceId: string;
  panelId: string;
  doc?: string;
  /** False while the panel's tab is hidden or its group is minimised. */
  visible: boolean;
};

export const PanelContext = createContext<PanelContextValue | null>(null);

export function usePanel(): PanelContextValue {
  const ctx = useContext(PanelContext);
  if (!ctx) throw new Error("usePanel() outside a panel");
  return ctx;
}

/**
 * Live events for a panel: subscribed only while the panel is visible, whatever its renderer mode
 * (docs/spec/10-ui-shell.md "Streams only while visible"). The handler gets one batch per animation frame.
 */
export function useTopic(patterns: string[] | null, handler: (batch: CadenceEvent[]) => void): void {
  const { visible, instanceId } = usePanel();
  const ref = useRef(handler);
  useEffect(() => {
    ref.current = handler;
  });
  const key = patterns?.join(",") ?? "";
  useEffect(() => {
    if (!visible || !key) return;
    return events.subscribe(key.split(","), (b) => ref.current(b), `panel:${instanceId}`);
  }, [visible, key, instanceId]);
}
