import type { IDockviewPanelHeaderProps } from "dockview-react";
import { Xmark } from "iconoir-react";
import {
  ContextMenu,
  ContextMenuContent,
  ContextMenuItem,
  ContextMenuSeparator,
  ContextMenuShortcut,
  ContextMenuTrigger,
} from "@/components/ui/context-menu";
import { PortalContainerContext, useOwnerBody } from "@/lib/portal";
import { chordLabel } from "@/shell/commands/keymap";
import { commands, panels } from "@/shell/registries";
import type { PanelParams } from "@/shell/registry/panels";
import { commandContext } from "@/shell/state";

// Our tab chrome (Dockview owns geometry and drag-and-drop; the look is ours). The context menu is the
// single-pointer path for every window operation (WCAG 2.5.7); the same commands are in the palette.

export const WINDOW_MENU: { id: string; label: string }[][] = [
  [
    { id: "view.float", label: "Float" },
    { id: "view.popout", label: "Pop out" },
    { id: "view.returnToGrid", label: "Return to grid" },
    { id: "view.toggleMaximize", label: "Maximize / restore" },
  ],
  [
    { id: "view.dockLeft", label: "Dock left" },
    { id: "view.dockRight", label: "Dock right" },
    { id: "view.dockTop", label: "Dock top" },
    { id: "view.dockBottom", label: "Dock bottom" },
  ],
  [
    { id: "view.helpForThis", label: "Help for this panel" },
    { id: "view.closePanel", label: "Close" },
  ],
];

export function WindowMenuItems({ activate, Item }: { activate: () => void; Item: typeof ContextMenuItem }) {
  const ctx = commandContext();
  return (
    <>
      {WINDOW_MENU.map((section, i) => (
        <div key={i}>
          {i > 0 ? <ContextMenuSeparator /> : null}
          {section.map((item) => {
            const cmd = commands.get(item.id);
            if (!cmd) return null;
            const ok = commands.isEnabled(cmd, ctx);
            return (
              <Item
                key={item.id}
                disabled={ok !== true}
                title={ok === true ? undefined : ok}
                onClick={() => {
                  activate();
                  void commands.run(item.id, commandContext());
                }}
              >
                {item.label}
                {cmd.keys?.[0] ? <ContextMenuShortcut>{chordLabel(cmd.keys[0])}</ContextMenuShortcut> : null}
              </Item>
            );
          })}
        </div>
      ))}
    </>
  );
}

export function Tab(props: IDockviewPanelHeaderProps<PanelParams>) {
  const [ref, body] = useOwnerBody();
  const m = props.params?.panel ? panels.resolve(props.params.panel) : undefined;
  const Icon = m?.icon;
  const activate = () => props.api.setActive();
  return (
    <PortalContainerContext.Provider value={body}>
      <ContextMenu>
        <ContextMenuTrigger
          render={
            <div
              ref={ref}
              data-tab={props.api.id}
              className="cadence-tab flex h-full items-center gap-1.5 pr-1 pl-2.5 text-xs select-none"
              // The menu's commands act on the active group: make this tab's group active before it opens.
              onContextMenu={activate}
              onMouseDown={(e) => {
                if (e.button === 1) {
                  e.preventDefault();
                  props.api.close();
                }
              }}
            />
          }
        >
          {m?.tab ? (
            <m.tab instanceId={props.api.id} doc={props.params?.doc} title={props.api.title ?? m.title} icon={m.icon} />
          ) : (
            <>
              {Icon ? <Icon aria-hidden className="size-3.5 shrink-0 opacity-80" /> : null}
              <span className="max-w-48 truncate">{props.api.title ?? m?.title ?? props.api.id}</span>
            </>
          )}
          <button
            type="button"
            aria-label={`Close ${props.api.title ?? ""}`}
            className="cadence-tab-close inline-flex size-6 items-center justify-center rounded text-muted-foreground hover:bg-accent hover:text-foreground"
            onPointerDown={(e) => e.stopPropagation()}
            onClick={(e) => {
              e.stopPropagation();
              props.api.close();
            }}
          >
            <Xmark aria-hidden className="size-3.5" />
          </button>
        </ContextMenuTrigger>
        <ContextMenuContent className="min-w-48">
          <WindowMenuItems activate={activate} Item={ContextMenuItem} />
        </ContextMenuContent>
      </ContextMenu>
    </PortalContainerContext.Provider>
  );
}
