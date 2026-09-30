import { useQuery } from "@tanstack/react-query";
import { NavArrowDown } from "iconoir-react";
import { projectsListOptions } from "@/api/gen/@tanstack/react-query.gen";
import { Button } from "@/components/ui/button";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuGroup,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuSeparator,
  DropdownMenuShortcut,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { chordLabel } from "@/shell/commands/keymap";
import type { Command } from "@/shell/commands/registry";
import { commands } from "@/shell/registries";
import { commandContext, useShell } from "@/shell/state";
import { DEFAULT_WORKSPACES } from "@/shell/workspaces/schema";
import { UserMenu } from "./UserMenu";

// Thin top menu bar: project switcher, workspaces, and the View/Window/Help menus. Every item is a command.

function CommandItem({ id, label }: { id: string; label?: string }) {
  const cmd = commands.get(id);
  if (!cmd) return null;
  const ok = commands.isEnabled(cmd, commandContext());
  return (
    <DropdownMenuItem disabled={ok !== true} title={ok === true ? undefined : ok} onClick={() => void commands.run(id, commandContext())}>
      {label ?? cmd.title}
      {cmd.keys?.[0] ? <DropdownMenuShortcut className="pl-6">{chordLabel(cmd.keys[0])}</DropdownMenuShortcut> : null}
    </DropdownMenuItem>
  );
}

function Menu({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <DropdownMenu>
      <DropdownMenuTrigger render={<Button variant="ghost" size="sm" className="h-6 px-2 text-xs font-normal" />}>{label}</DropdownMenuTrigger>
      <DropdownMenuContent align="start" className="w-max min-w-56 [&_[role=menuitem]]:whitespace-nowrap">
        {children}
      </DropdownMenuContent>
    </DropdownMenu>
  );
}

export function ProjectSwitcher({ onSwitch }: { onSwitch: (slug: string) => void }) {
  const project = useShell((s) => s.project);
  const { data } = useQuery(projectsListOptions());
  const current = data?.items.find((p) => p.slug === project);
  return (
    <DropdownMenu>
      <DropdownMenuTrigger
        render={<Button variant="ghost" size="sm" className="h-6 gap-1 px-2 text-xs font-medium" data-testid="project-switcher" />}
      >
        {current?.name ?? project ?? "No project"}
        <NavArrowDown aria-hidden className="size-3" />
      </DropdownMenuTrigger>
      <DropdownMenuContent align="start" className="min-w-56">
        <DropdownMenuGroup>
          <DropdownMenuLabel>Projects</DropdownMenuLabel>
          {(data?.items ?? []).map((p) => (
            <DropdownMenuItem key={p.id} onClick={() => onSwitch(p.slug)} aria-current={p.slug === project ? "true" : undefined}>
              {p.name}
              <span className="ml-auto text-xs text-muted-foreground">{p.slug}</span>
            </DropdownMenuItem>
          ))}
        </DropdownMenuGroup>
        <DropdownMenuSeparator />
        <CommandItem id="projects.new" />
        <CommandItem id="projects.edit" />
        <CommandItem id="projects.archive" />
        <CommandItem id="view.switchProject" />
      </DropdownMenuContent>
    </DropdownMenu>
  );
}

function groupItems(group: Command["group"]): Command[] {
  return commands.all().filter((c) => c.group === group && !c.hidden);
}

// The Window menu in sections: window states, docking, floating alignment, moving focus, and the layout reset.
const WINDOW_SECTIONS: [string, string?][][] = [
  [["view.float"], ["view.popout"], ["view.returnToGrid"], ["view.toggleMaximize"]],
  [["view.dockLeft"], ["view.dockRight"], ["view.dockTop"], ["view.dockBottom"]],
  [
    ["view.alignLeft", "Align float left"],
    ["view.alignRight", "Align float right"],
    ["view.alignTop", "Align float top"],
    ["view.alignBottom", "Align float bottom"],
  ],
  [["view.nextGroup"], ["view.previousGroup"], ["view.nextTab", "Next tab"], ["view.previousTab", "Previous tab"], ["view.closePanel"]],
  [["view.resetWorkspace", "Reset layout"]],
];

export function MenuBar({ onSwitchProject }: { onSwitchProject: (slug: string) => void }) {
  const workspace = useShell((s) => s.workspace);
  return (
    <nav aria-label="Main menu" className="flex h-8 shrink-0 items-center gap-0.5 border-b bg-chrome px-1.5">
      <span className="mr-1 flex items-center gap-1.5 px-1.5 text-xs font-semibold">
        <span aria-hidden className="size-3 rounded-sm bg-primary" />
        Cadence
      </span>
      <span aria-hidden className="mx-1 h-4 w-px bg-border" />
      <ProjectSwitcher onSwitch={onSwitchProject} />
      <span aria-hidden className="text-xs text-muted-foreground">/</span>
      <Menu label={workspace ?? "Workspace"}>
        {DEFAULT_WORKSPACES.map((w) => (
          <CommandItem key={w} id={`view.workspace${w}`} label={w} />
        ))}
        <DropdownMenuSeparator />
        <CommandItem id="workspaces.set" />
        <CommandItem id="view.resetWorkspace" />
      </Menu>
      <span aria-hidden className="mx-1 h-4 w-px bg-border" />
      <Menu label="View">
        {groupItems("View").map((c) => (
          <CommandItem key={c.id} id={c.id} />
        ))}
      </Menu>
      <Menu label="Window">
        {WINDOW_SECTIONS.map((section, i) => (
          <DropdownMenuGroup key={i}>
            {i > 0 ? <DropdownMenuSeparator /> : null}
            {section.map(([id, label]) => (
              <CommandItem key={id} id={id} label={label} />
            ))}
          </DropdownMenuGroup>
        ))}
      </Menu>
      <Menu label="Help">
        {groupItems("Help").map((c) => (
          <CommandItem key={c.id} id={c.id} />
        ))}
        <CommandItem id="view.palette" />
      </Menu>
      <span className="ml-auto" />
      <UserMenu />
    </nav>
  );
}
