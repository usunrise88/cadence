import { useEffect, useRef } from "react";
import { DockviewReact, type DockviewReadyEvent, type DockviewTheme, type IWatermarkPanelProps } from "dockview-react";
import { EmptyState } from "@/shell/entity/primitives";
import { installAdapter, MIN_VISIBLE, transformFloatingGroupDrag } from "@/shell/floating-snap/dockview-adapter";
import { Guides } from "@/shell/floating-snap/Guides";
import { useHelp } from "@/shell/help/store";
import { trackFocus } from "@/shell/chrome/dialogs";
import { installKeyboard } from "@/shell/commands/registry";
import { isMac } from "@/shell/commands/keymap";
import { announce } from "@/shell/notifications/store";
import { commands, panels } from "@/shell/registries";
import type { PanelParams } from "@/shell/registry/panels";
import { useSelection } from "@/shell/selection/store";
import { commandContext } from "@/shell/state";
import { installTheme } from "@/shell/theme/store";
import { GroupActions } from "./GroupActions";
import { PanelFrame } from "./PanelFrame";
import { useDock } from "./store";
import { Tab } from "./Tab";

// The Dockview host: our tab chrome, header actions, snapping hook, keyboard navigation and live-region wording.

const theme: DockviewTheme = {
  name: "cadence",
  className: "dockview-theme-cadence",
  gap: 0,
  dndOverlayMounting: "absolute",
  dndPanelOverlay: "group",
  dndTabIndicator: "line",
};

// Keyboard: Dockview 8.3's `keyboardNavigation` needs a dockview-enterprise module, which we never install; the
// command registry owns every window key (F6, tab cycling, close, float moves) instead.

function Desktop(_props: IWatermarkPanelProps) {
  return <EmptyState step="prepare" title="Nothing is open" hint="Open the Library or a document from the palette (Ctrl/Cmd+K), or reset the workspace." />;
}

export function DockHost({ onReady }: { onReady: (e: DockviewReadyEvent) => void }) {
  const root = useRef<HTMLDivElement>(null);
  const cleanup = useRef<(() => void)[]>([]);

  useEffect(
    () => () => {
      cleanup.current.forEach((c) => c());
      cleanup.current = [];
      useDock.getState().setApi(null);
    },
    [],
  );

  const ready = (e: DockviewReadyEvent) => {
    const api = e.api;
    useDock.getState().setApi(api);
    const mac = isMac();
    cleanup.current.push(
      installAdapter(api, root.current!, {
        onPopoutWindow: (win) => {
          const offTheme = installTheme(win.document);
          const offKeys = installKeyboard(win.document, commands, commandContext, mac);
          const offFocus = trackFocus(win.document);
          win.addEventListener("unload", () => {
            offTheme();
            offKeys();
            offFocus();
          });
        },
      }),
    );
    const active = api.onDidActivePanelChange(({ panel }) => {
      const params = panel?.params as PanelParams | undefined;
      const m = params?.panel ? panels.resolve(params.panel) : undefined;
      if (m?.kind === "document" && params?.doc) useSelection.getState().setActiveDoc(params.doc);
      if (m && m.id !== "help") useHelp.getState().setFocusedHelp(m.help);
    });
    const removed = api.onDidRemovePanel((p) => {
      const doc = (p.params as PanelParams | undefined)?.doc;
      if (doc && !api.panels.some((q) => (q.params as PanelParams | undefined)?.doc === doc)) useSelection.getState().forgetDoc(doc);
      useSelection.getState().unpin(p.id);
    });
    cleanup.current.push(() => active.dispose(), () => removed.dispose());
    onReady(e);
  };

  return (
    <div ref={root} className="relative h-full w-full" data-testid="dock">
      <DockviewReact
        theme={theme}
        components={{ panel: PanelFrame }}
        defaultTabComponent={Tab}
        rightHeaderActionsComponent={GroupActions}
        watermarkComponent={Desktop}
        floatingGroupDragHandle="tabbar"
        floatingGroupBounds={{ minimumWidthWithinViewport: MIN_VISIBLE.width, minimumHeightWithinViewport: MIN_VISIBLE.height }}
        transformFloatingGroupDrag={transformFloatingGroupDrag}
        popoutUrl="/popout.html"
        announcer={(a) => announce(a.message)}
        onReady={ready}
      />
      <Guides />
    </div>
  );
}
