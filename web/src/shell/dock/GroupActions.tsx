import { useEffect, useState } from "react";
import type { IDockviewHeaderActionsProps } from "dockview-react";
import { Collapse, Expand, MoreHoriz, OpenNewWindow, ReturnToGrid } from "./group-icons";
import { Button } from "@/components/ui/button";
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuTrigger } from "@/components/ui/dropdown-menu";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import { PortalContainerContext, useOwnerBody } from "@/lib/portal";
import { canMaximize, popoutGroup, returnGroup, toggleMaximize } from "./layout";
import { WindowMenuItems } from "./Tab";

// Header actions on every group: pop out / return, maximize (disabled with the reason on floats) and a menu with
// the same window operations as the tab context menu.
export function GroupActions(props: IDockviewHeaderActionsProps) {
  const [ref, body] = useOwnerBody();
  const [, force] = useState(0);
  const group = props.group;
  useEffect(() => {
    const a = group.api.onDidLocationChange(() => force((n) => n + 1));
    const b = props.containerApi.onDidMaximizedGroupChange(() => force((n) => n + 1));
    return () => {
      a.dispose();
      b.dispose();
    };
  }, [group, props.containerApi]);
  const loc = group.api.location.type;
  const maxOk = canMaximize(group);
  const maximized = maxOk === true && group.api.isMaximized();
  return (
    <PortalContainerContext.Provider value={body}>
      <div ref={ref} className="cadence-group-actions flex h-full items-center gap-0.5 px-1 text-muted-foreground">
        {loc === "popout" ? (
          <IconButton label="Return to grid" onClick={() => returnGroup(group)} icon={<ReturnToGrid />} />
        ) : (
          <IconButton label="Pop out" onClick={() => void popoutGroup(group)} icon={<OpenNewWindow />} />
        )}
        <IconButton
          label={maxOk !== true ? maxOk : maximized ? "Restore" : "Maximize"}
          disabled={maxOk !== true}
          onClick={() => toggleMaximize(group)}
          icon={maximized ? <Collapse /> : <Expand />}
        />
        <DropdownMenu>
          <DropdownMenuTrigger render={<Button variant="ghost" size="icon-xs" aria-label="Window actions" onPointerDown={() => group.activePanel?.api.setActive()} className="size-6 text-muted-foreground hover:text-foreground [&_svg]:size-3.5" />}>
            <MoreHoriz aria-hidden />
          </DropdownMenuTrigger>
          <DropdownMenuContent align="end" className="min-w-48">
            <WindowMenuItems activate={() => group.activePanel?.api.setActive()} Item={DropdownMenuItem} />
          </DropdownMenuContent>
        </DropdownMenu>
      </div>
    </PortalContainerContext.Provider>
  );
}

function IconButton({ label, icon, onClick, disabled }: { label: string; icon: React.ReactNode; onClick: () => void; disabled?: boolean }) {
  const b = (
    <Button variant="ghost" size="icon-xs" aria-label={label} disabled={disabled} onClick={onClick} className="size-6 text-muted-foreground hover:text-foreground [&_svg]:size-3.5">
      {icon}
    </Button>
  );
  return (
    <Tooltip>
      <TooltipTrigger render={<span />}>{b}</TooltipTrigger>
      <TooltipContent>{label}</TooltipContent>
    </Tooltip>
  );
}
