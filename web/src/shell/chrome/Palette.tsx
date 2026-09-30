import { useEffect, useMemo, useRef, useState, type KeyboardEvent } from "react";
import { useQuery } from "@tanstack/react-query";
import { ProblemError } from "@/api/client";
import { helpSearchOptions, projectsListOptions, projectsSearchOptions, registrySearchOptions, viewsListOptions } from "@/api/gen/@tanstack/react-query.gen";
import type { SearchHit } from "@/api/gen/types.gen";
import { Command, CommandEmpty, CommandGroup, CommandInput, CommandItem, CommandList, CommandShortcut } from "@/components/ui/command";
import { Dialog, DialogContent, DialogDescription, DialogTitle } from "@/components/ui/dialog";
import { chordLabel } from "@/shell/commands/keymap";
import { openPanel } from "@/shell/dock/layout";
import { useHelp } from "@/shell/help/store";
import { commands } from "@/shell/registries";
import { openInLibrary, openRef, previewRef } from "@/shell/search/actions";
import { chipLabel, flattenGroups, kindLabel } from "@/shell/search/hits";
import { useSearch } from "@/shell/search/store";
import { commandContext, useShell } from "@/shell/state";

// Ctrl/Cmd+K: plain text searches entities (projects.search: the current project and the registry), `>` runs
// commands, `?` searches help, `@` switches project (R37). docs/spec/11-ui-panels.md "Search".

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

const HIT_PREFIX = "hit:";

// Search snippets come from the index text, which carries internal ids (`ver_01a0…`); people read names.
const INTERNAL_ID = /\b[a-z]{2,4}_[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}\b/g;
function readableSnippet(s: string | undefined): string {
  return (s ?? "").replace(INTERNAL_ID, "").replace(/\s{2,}/g, " ").trim();
}
const OPEN_AS_LIST = "open-as-list";

export function Palette({ prefix, onClose, onSwitchProject }: { prefix: string; onClose: () => void; onSwitchProject: (slug: string) => void }) {
  const [q, setQ] = useState(prefix);
  const [highlighted, setHighlighted] = useState("");
  // Space previews the highlighted hit only after the highlight was moved with the arrow keys; typing resets it,
  // so a space inside the query still types a space.
  const navigated = useRef(false);
  const { mode, text } = modeOf(q);
  const debounced = useDebounced(text, 120);
  const project = useShell((s) => s.project);
  const ctx = commandContext();
  const entitySearch = mode === "search" && !!project;

  const search = useQuery({
    ...projectsSearchOptions({
      path: { p: project ?? "" },
      query: { q: debounced, limit: 40 },
    }),
    enabled: entitySearch && debounced.length > 1,
    retry: false,
  });
  const help = useQuery({
    ...helpSearchOptions({ query: { q: debounced, limit: 8 } }),
    enabled: (mode === "help" || (mode === "search" && !project)) && debounced.length > 1,
  });
  const registry = useQuery({
    ...registrySearchOptions({ query: { q: debounced, limit: 8 } }),
    enabled: mode === "search" && !project && debounced.length > 1,
  });
  const projects = useQuery({
    ...projectsListOptions(),
    enabled: mode === "projects" || mode === "search",
  });
  const views = useQuery({
    ...viewsListOptions({ path: { p: project ?? "" } }),
    enabled: entitySearch,
  });

  const remember = useSearch((s) => s.remember);
  const hits = useMemo(() => (search.data && debounced.length > 1 ? flattenGroups(search.data.groups) : []), [search.data, debounced]);
  useEffect(() => remember(hits), [hits, remember]);
  const byValue = useMemo(() => new Map(hits.map((h) => [HIT_PREFIX + h.ref, h])), [hits]);
  // Hits arrive after the other groups: highlight the best one so Enter opens it, unless the user already moved.
  useEffect(() => {
    if (hits[0] && !navigated.current) setHighlighted(HIT_PREFIX + hits[0].ref);
  }, [hits]);

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
  const openHit = (h: SearchHit) => {
    onClose();
    openRef(h.ref);
  };

  const onKeyDown = (e: KeyboardEvent<HTMLDivElement>) => {
    if (e.key === "ArrowDown" || e.key === "ArrowUp" || e.key === "Home" || e.key === "End") {
      navigated.current = true;
      return;
    }
    if (e.key === " " && navigated.current) {
      const hit = byValue.get(highlighted);
      if (hit) {
        e.preventDefault();
        previewRef(hit.ref);
      }
      return;
    }
    if (e.key.length === 1 || e.key === "Backspace" || e.key === "Delete") navigated.current = false;
  };

  const showCommands = mode === "commands" || (mode === "search" && text.length > 0);
  const cmds = showCommands ? cmdList.filter((c) => matches(c.title) || matches(c.id)).slice(0, mode === "commands" ? 100 : 6) : [];
  const invalid = search.error instanceof ProblemError && (search.error.slug === "invalid-query" || search.error.status === 400) ? search.error : undefined;
  const savedViews = entitySearch ? (views.data?.items ?? []).filter((v) => !text || matches(v.name) || matches(v.query)).slice(0, text ? 5 : 8) : [];
  const switchable =
    (mode === "projects" || (mode === "search" && text)) && projects.data
      ? projects.data.items.filter((p) => matches(p.name) || matches(p.slug)).slice(0, mode === "projects" ? 50 : 5)
      : [];
  const qualifiers = entitySearch && debounced.length > 1 ? (search.data?.qualifiers ?? []) : [];

  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="top-24 translate-y-0 overflow-hidden p-0 sm:max-w-xl" showCloseButton={false}>
        <DialogTitle className="sr-only">Command palette</DialogTitle>
        <DialogDescription className="sr-only">Type to search; &gt; for commands, ? for help, @ for projects.</DialogDescription>
        <Command
          shouldFilter={false}
          className="rounded-none!"
          label="Command palette"
          value={highlighted}
          onValueChange={setHighlighted}
          onKeyDown={onKeyDown}
        >
          <CommandInput
            value={q}
            onValueChange={(v) => {
              navigated.current = false;
              setQ(v);
            }}
            placeholder="Search · kind: tag: status: · > commands · ? help · @ projects"
            autoFocus
          />
          {qualifiers.length > 0 ? (
            <div className="flex flex-wrap gap-1 border-b px-3 py-1.5" aria-label="Qualifiers" data-slot="palette-qualifiers">
              {qualifiers.map((f) => (
                <span
                  key={f.raw}
                  className="inline-flex h-5 items-center rounded-full border border-accent-line bg-accent-soft px-2 text-[11px] text-accent-text"
                >
                  {chipLabel(f)}
                </span>
              ))}
            </div>
          ) : null}
          {invalid ? (
            <div role="alert" className="border-b px-3 py-2 text-xs text-destructive" data-slot="palette-invalid-query">
              {invalid.problem.detail ?? invalid.problem.title}
            </div>
          ) : null}
          <CommandList className="max-h-96">
            <CommandEmpty>{text ? (entitySearch && search.isFetching ? "Searching…" : "No matches.") : "Type to search."}</CommandEmpty>
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
            {entitySearch
              ? (search.data?.groups ?? []).map((g) =>
                  debounced.length > 1 && g.items.length > 0 ? (
                    <CommandGroup key={g.kind} heading={kindLabel(g.kind)}>
                      {g.items.map((h) => (
                        <CommandItem key={h.ref} value={HIT_PREFIX + h.ref} onSelect={() => openHit(h)}>
                          <span className="max-w-[60%] shrink-0 truncate">{h.title}</span>
                          <span className="min-w-0 flex-1 truncate text-xs text-muted-foreground">
                            {h.project && h.project !== project ? `${h.project} · ` : ""}
                            {readableSnippet(h.snippet) || h.id}
                          </span>
                          {h.status ? <CommandShortcut>{h.status}</CommandShortcut> : null}
                        </CommandItem>
                      ))}
                    </CommandGroup>
                  ) : null,
                )
              : null}
            {entitySearch && text ? (
              <CommandGroup heading="Search">
                <CommandItem
                  value={OPEN_AS_LIST}
                  onSelect={() => {
                    onClose();
                    openInLibrary(text);
                  }}
                >
                  <span>Open as list</span>
                  <span className="truncate text-xs text-muted-foreground">“{text}” in the Library</span>
                </CommandItem>
              </CommandGroup>
            ) : null}
            {savedViews.length > 0 ? (
              <CommandGroup heading="Saved searches">
                {savedViews.map((v) => (
                  <CommandItem
                    key={v.id}
                    value={`view:${v.name}`}
                    onSelect={() => {
                      onClose();
                      openInLibrary(v.query, v.name);
                    }}
                  >
                    <span>{v.name}</span>
                    <span className="truncate text-xs text-muted-foreground">{v.query}</span>
                  </CommandItem>
                ))}
              </CommandGroup>
            ) : null}
            {switchable.length > 0 ? (
              <CommandGroup heading={mode === "projects" ? "Projects" : "Switch project"}>
                {switchable.map((p) => (
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
            {(mode === "help" || (mode === "search" && !project)) && help.data?.items.length ? (
              <CommandGroup heading="Help">
                {help.data.items.map((h) => (
                  <CommandItem key={h.id} value={`help:${h.id}`} onSelect={() => openHelp(h.id)}>
                    <span>{h.title}</span>
                    <span className="truncate text-xs text-muted-foreground">{h.summary}</span>
                  </CommandItem>
                ))}
              </CommandGroup>
            ) : null}
            {mode === "search" && !project && registry.data?.items.length ? (
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
