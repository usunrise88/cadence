import { useEffect, useMemo, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { helpSearchOptions, projectsListOptions, registrySearchOptions } from "@/api/gen/@tanstack/react-query.gen";
import { Command, CommandEmpty, CommandGroup, CommandInput, CommandItem, CommandList, CommandShortcut } from "@/components/ui/command";
import { Dialog, DialogContent, DialogDescription, DialogTitle } from "@/components/ui/dialog";
import { chordLabel } from "@/shell/commands/keymap";
import { openPanel } from "@/shell/dock/layout";
import { useHelp } from "@/shell/help/store";
import { commands } from "@/shell/registries";
import { commandContext, useShell } from "@/shell/state";

// Ctrl/Cmd+K: plain text searches everything, `>` runs commands, `?` searches help, `@` switches project (R37).

type Mode = "search" | "commands" | "help" | "projects";

function modeOf(q: string): { mode: Mode; text: string } {
  if (q.startsWith(">")) return { mode: "commands", text: q.slice(1).trim() };
  if (q.startsWith("?")) return { mode: "help", text: q.slice(1).trim() };
  if (q.startsWith("@")) return { mode: "projects", text: q.slice(1).trim() };
  return { mode: "search", text: q.trim() };
}

function useDebounced<T>(v: T, ms: number): T {
  const [d, setD] = useState(v);
  useEffect(() => {
    const t = setTimeout(() => setD(v), ms);
    return () => clearTimeout(t);
  }, [v, ms]);
  return d;
}

export function Palette({ prefix, onClose, onSwitchProject }: { prefix: string; onClose: () => void; onSwitchProject: (slug: string) => void }) {
  const [q, setQ] = useState(prefix);
  const { mode, text } = modeOf(q);
  const debounced = useDebounced(text, 120);
  const project = useShell((s) => s.project);
  const ctx = commandContext();

  const help = useQuery({ ...helpSearchOptions({ query: { q: debounced, limit: 8 } }), enabled: (mode === "help" || mode === "search") && debounced.length > 1 });
  const registry = useQuery({ ...registrySearchOptions({ query: { q: debounced, limit: 8 } }), enabled: mode === "search" && debounced.length > 1 });
  const projects = useQuery({ ...projectsListOptions(), enabled: mode === "projects" || mode === "search" });

  const cmdList = useMemo(() => {
    const recent = commands.recentIds();
    const all = commands.all().filter((c) => !c.hidden);
    return all.sort((a, b) => {
      const ra = recent.indexOf(a.id);
      const rb = recent.indexOf(b.id);
      if (ra !== rb) return (ra === -1 ? 99 : ra) - (rb === -1 ? 99 : rb);
      return a.title.localeCompare(b.title);
    });
  }, []);

  const matches = (s: string) => s.toLowerCase().includes(text.toLowerCase());
  const run = (id: string) => {
    onClose();
    void commands.run(id, commandContext());
  };
  const openHelp = (id: string) => {
    onClose();
    useHelp.getState().show(id);
    openPanel("help");
  };

  const showCommands = mode === "commands" || (mode === "search" && text.length > 0);
  const cmds = showCommands ? cmdList.filter((c) => matches(c.title) || matches(c.id)).slice(0, mode === "commands" ? 100 : 6) : [];

  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="top-24 translate-y-0 overflow-hidden p-0 sm:max-w-xl" showCloseButton={false}>
        <DialogTitle className="sr-only">Command palette</DialogTitle>
        <DialogDescription className="sr-only">Type to search; &gt; for commands, ? for help, @ for projects.</DialogDescription>
        <Command shouldFilter={false} className="rounded-none!" label="Command palette">
          <CommandInput value={q} onValueChange={setQ} placeholder="Search · > commands · ? help · @ projects" autoFocus />
          <CommandList className="max-h-96">
            <CommandEmpty>{text ? "No matches." : "Type to search."}</CommandEmpty>
            {cmds.length > 0 ? (
              <CommandGroup heading="Commands">
                {cmds.map((c) => {
                  const ok = commands.isEnabled(c, ctx);
                  return (
                    <CommandItem key={c.id} value={c.id} disabled={ok !== true} onSelect={() => run(c.id)} title={ok === true ? undefined : ok}>
                      {c.icon ? <c.icon aria-hidden /> : null}
                      <span>{c.title}</span>
                      <span className="text-xs text-muted-foreground">{c.id}</span>
                      {c.keys?.[0] ? <CommandShortcut>{chordLabel(c.keys[0])}</CommandShortcut> : null}
                    </CommandItem>
                  );
                })}
              </CommandGroup>
            ) : null}
            {(mode === "projects" || (mode === "search" && text)) && projects.data ? (
              <CommandGroup heading="Projects">
                {projects.data.items
                  .filter((p) => matches(p.name) || matches(p.slug))
                  .slice(0, mode === "projects" ? 50 : 5)
                  .map((p) => (
                    <CommandItem
                      key={p.id}
                      value={`project:${p.slug}`}
                      onSelect={() => {
                        onClose();
                        onSwitchProject(p.slug);
                      }}
                    >
                      <span>{p.name}</span>
                      <span className="text-xs text-muted-foreground">{p.slug}</span>
                      {p.slug === project ? <CommandShortcut>current</CommandShortcut> : null}
                    </CommandItem>
                  ))}
              </CommandGroup>
            ) : null}
            {(mode === "help" || mode === "search") && help.data?.items.length ? (
              <CommandGroup heading="Help">
                {help.data.items.map((h) => (
                  <CommandItem key={h.id} value={`help:${h.id}`} onSelect={() => openHelp(h.id)}>
                    <span>{h.title}</span>
                    <span className="truncate text-xs text-muted-foreground">{h.summary}</span>
                  </CommandItem>
                ))}
              </CommandGroup>
            ) : null}
            {mode === "search" && registry.data?.items.length ? (
              <CommandGroup heading="Registry">
                {registry.data.items.map((r) => (
                  <CommandItem key={`${r.kind}:${r.id}`} value={`reg:${r.kind}:${r.id}`} onSelect={onClose}>
                    <span>{r.name}</span>
                    <span className="text-xs text-muted-foreground">
                      {r.kind} · {r.version}
                    </span>
                  </CommandItem>
                ))}
              </CommandGroup>
            ) : null}
          </CommandList>
        </Command>
      </DialogContent>
    </Dialog>
  );
}
