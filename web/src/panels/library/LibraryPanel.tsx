import { useCallback, useEffect, useMemo, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { Plus } from "iconoir-react";
import { evalsListOptions, langpacksListOptions, mixesListOptions, projectsSearchOptions, registrySearchOptions, viewsListOptions } from "@/api/gen/@tanstack/react-query.gen";
import type { AgentReference, RegistryKind } from "@/api/gen/types.gen";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { cn } from "@/lib/utils";
import { AgentMenu, EmptyState, EntityList, explainThis, type ListRow } from "@/shell/entity/primitives";
import {
  askAgent,
  chipLabel,
  formatReference,
  kindNoun,
  openDocument,
  openRef,
  parseReference,
  previewRef,
  runCommand,
  scopeOf,
  useCommand,
  useProject,
  useSearch,
  useSelection,
  useTopic,
  withScope,
  type PanelProps,
} from "@/shell/panel";

// Library: browses the Cadence-wide registry with a this-project / all filter, and lists any search in the query
// language ("Open as list" from the palette, a saved view). docs/spec/11-ui-panels.md "Library", "Search".

export function LibraryEmpty() {
  return (
    <EmptyState
      step="prepare"
      title="The registry is empty"
      hint="Sources, dataset versions, golden sets and models appear here once registered. Registration arrives with the agent loop (phase 1)."
    />
  );
}

function useDebounced<T>(v: T, ms: number): T {
  const [d, setD] = useState(v);
  useEffect(() => {
    const t = setTimeout(() => setD(v), ms);
    return () => clearTimeout(t);
  }, [v, ms]);
  return d;
}

/** Rows of the project's work (document references); registry rows are `<kind>:<ver_…>`. */
const WORK = /^(mix|eval|language_pack):/;
/** Registry kinds whose versions open as a document (Golden set, Model). */
const DOCUMENTED = /^(golden_set|model):/;

const pill = (on: boolean) =>
  cn("h-6 rounded-full border px-2", on ? "border-accent-line bg-accent-soft text-accent-text" : "text-muted-foreground hover:bg-hover");

export function LibraryPanel(_props: PanelProps) {
  const project = useProject();
  const query = useSearch((s) => s.libraryQuery);
  const activeView = useSearch((s) => s.activeView);
  const setQuery = useSearch((s) => s.setLibraryQuery);
  const remember = useSearch((s) => s.remember);
  const [browseScope, setBrowseScope] = useState<"project" | "all">("all");
  const [kind, setKind] = useState<RegistryKind | undefined>(undefined);
  const debounced = useDebounced(query.trim(), 150);
  const searching = !!project && debounced.length > 0;

  // Registry browse (no query): the registry's own search with its kind chips.
  const browse = useQuery({
    ...registrySearchOptions({
      query: {
        kind,
        project: browseScope === "project" ? project : undefined,
        limit: 500,
      },
    }),
    enabled: !searching,
  });
  // A query: projects.search, the same index and grammar as the palette and the agent's tool.
  const search = useQuery({
    ...projectsSearchOptions({
      path: { p: project ?? "" },
      query: { q: debounced, limit: 200 },
    }),
    enabled: searching,
    retry: false,
  });
  const views = useQuery({
    ...viewsListOptions({ path: { p: project ?? "" } }),
    enabled: !!project,
  });

  useTopic(["entity.base_model.*", "entity.dataset_version.*", "entity.template.*", "entity.golden_set.*", "entity.normalizer.*", "entity.model.*"], () =>
    void (searching ? search.refetch() : browse.refetch()),
  );
  useTopic(project ? ["entity.saved_search.*"] : null, () => void views.refetch());

  // The project's work (mixes, evals and language packs; runs open from Metrics and links) lists before the registry
  // and opens as documents.
  const workEnabled = !!project && !searching && !kind;
  const mixes = useQuery({ ...mixesListOptions({ path: { p: project ?? "" } }), enabled: workEnabled });
  const evals = useQuery({ ...evalsListOptions({ path: { p: project ?? "" }, query: { limit: 50 } }), enabled: workEnabled });
  const packs = useQuery({ ...langpacksListOptions({ path: { p: project ?? "" } }), enabled: workEnabled });
  useTopic(project ? ["entity.mix.*"] : null, () => void mixes.refetch());
  useTopic(project ? ["entity.eval.*"] : null, () => void evals.refetch());
  const work: ListRow[] = [
    ...(mixes.data?.items ?? []).map((m) => ({
      id: `mix:${m.id}`,
      name: m.name,
      version: `rev ${m.rev}`,
      state: "active",
      tags: ["mix"],
      actor: m.cause?.draftAuthor ?? m.updatedBy,
      updatedAt: m.updatedAt,
    })),
    ...(evals.data?.items ?? []).map((e) => ({
      id: `eval:${e.id}`,
      name: `${e.subject.label} vs ${e.baseline.label}`,
      version: e.gate ? `gate ${e.gate.verdict}` : "eval",
      state: e.status,
      tags: ["eval"],
      actor: e.actor,
      updatedAt: e.updatedAt,
    })),
    ...(packs.data?.items ?? []).map((p) => ({
      id: `language_pack:${p.locale}`,
      name: `${p.locale} language pack`,
      version: p.sha.slice(0, 7),
      state: "active",
      tags: ["language pack", ...p.boost.map((b) => `boost:${b}`)],
    })),
  ];
  // Project work and registry kinds with a document panel (golden sets, models) open as documents; the rest select.
  const openRow = (r: ListRow) => (WORK.test(r.id) || DOCUMENTED.test(r.id) ? openDocument(r.id) : select(`registry:${r.id}`, undefined));

  const hits = useMemo(() => (searching ? (search.data?.groups ?? []).flatMap((g) => g.items) : []), [searching, search.data]);
  useEffect(() => remember(hits), [hits, remember]);

  const rows: ListRow[] = searching
    ? hits.map((h) => ({
        id: h.ref,
        name: h.title,
        version: kindNoun(h.kind),
        state: h.status ?? "—",
        tags: h.tags,
        actor: h.actor,
        updatedAt: h.updatedAt,
      }))
    : [
        ...(kind ? [] : work),
        ...(browse.data?.items ?? []).map((r) => ({
          id: `${r.kind}:${r.id}`,
          name: r.name,
          version: r.version,
          state: r.state,
          tags: r.tags,
          actor: r.actor,
          updatedAt: r.updatedAt,
        })),
      ];
  const select = useSelection((s) => s.select);

  const scope = searching ? (scopeOf(query) === "all" ? "all" : "project") : browseScope;
  const setScope = (s: "project" | "all") => {
    if (searching) setQuery(withScope(query, s === "all" ? "all" : undefined));
    else setBrowseScope(s);
  };

  const [naming, setNaming] = useState(false);
  const [name, setName] = useState("");
  const save = async () => {
    const n = name.trim();
    if (!n || !query.trim()) return;
    try {
      await runCommand("views.set", { name: n, query: query.trim() });
      setQuery(query, n);
      setNaming(false);
      setName("");
      void views.refetch();
    } catch {
      /* the command reported it */
    }
  };

  // An invalid query (unknown qualifier) answers 400 with the list of qualifiers in its detail.
  const problem = searching && search.error instanceof Error ? search.error.message : "";
  const savedViews = views.data?.items ?? [];
  const [current, setCurrent] = useState<ListRow | undefined>();
  // Rows are rebuilt on every render: keep the highlighted one until another row is highlighted (else a render loop).
  const onCursor = useCallback((r: ListRow | undefined) => setCurrent((p) => (p?.id === r?.id ? p : r)), []);

  return (
    <div className="flex h-full min-h-0 flex-col">
      <div className="flex shrink-0 flex-col gap-2 border-b p-2">
        <div className="flex items-center gap-1.5">
          <Input
            value={query}
            onChange={(e) => setQuery(e.target.value)}
            placeholder="Filter — text, kind:, tag:, status:, updated:>…"
            aria-label="Filter the library"
          />
          <NewMixButton />
          <LibraryAgentMenu row={current} query={query} />
        </div>
        <div className="flex flex-wrap items-center gap-1 text-xs">
          <div role="radiogroup" aria-label="Scope" className="inline-flex rounded-md border bg-background p-0.5">
            {(["project", "all"] as const).map((s) => (
              <button
                key={s}
                role="radio"
                aria-checked={scope === s}
                type="button"
                onClick={() => setScope(s)}
                className={cn(
                  "h-6 rounded-[4px] px-2.5",
                  scope === s ? "bg-selected font-medium text-foreground" : "text-muted-foreground hover:text-foreground",
                )}
              >
                {s === "project" ? "This project" : "All"}
              </button>
            ))}
          </div>
          {searching
            ? (search.data?.qualifiers ?? []).map((f) => (
                <span key={f.raw} className="inline-flex h-6 items-center rounded-full border border-accent-line bg-accent-soft px-2 text-accent-text">
                  {chipLabel(f)}
                </span>
              ))
            : (browse.data?.kinds ?? []).map((k) => (
                <button
                  key={k.kind}
                  type="button"
                  aria-pressed={kind === k.kind}
                  onClick={() => setKind(kind === k.kind ? undefined : k.kind)}
                  className={pill(kind === k.kind)}
                >
                  {k.kind} {k.count}
                </button>
              ))}
        </div>
        {project ? (
          <div className="flex flex-wrap items-center gap-1 text-xs" role="group" aria-label="Saved searches">
            <span className="text-muted-foreground">Views</span>
            {savedViews.map((v) => (
              <button
                key={v.id}
                type="button"
                aria-pressed={activeView === v.name}
                title={v.query}
                onClick={() => setQuery(activeView === v.name ? "" : v.query, activeView === v.name ? null : v.name)}
                className={pill(activeView === v.name)}
              >
                {v.name}
              </button>
            ))}
            {naming ? (
              <form
                className="flex items-center gap-1"
                onSubmit={(e) => {
                  e.preventDefault();
                  void save();
                }}
              >
                <Input
                  value={name}
                  onChange={(e) => setName(e.target.value)}
                  placeholder="View name"
                  aria-label="View name"
                  autoFocus
                  className="h-6 w-32 text-xs"
                />
                <Button type="submit" size="xs" disabled={!name.trim()}>
                  Save
                </Button>
                <Button type="button" size="xs" variant="ghost" onClick={() => setNaming(false)}>
                  Cancel
                </Button>
              </form>
            ) : (
              <Button
                type="button"
                size="xs"
                variant="ghost"
                disabled={!query.trim()}
                title={query.trim() ? undefined : "Type a query to save it"}
                onClick={() => setNaming(true)}
              >
                Save view
              </Button>
            )}
          </div>
        ) : null}
        {problem ? (
          <p role="alert" className="text-xs text-destructive">
            {problem}
          </p>
        ) : null}
      </div>
      <div className="min-h-0 flex-1">
        {rows.length === 0 ? (
          searching ? (
            <EmptyState
              step="prepare"
              title={search.isFetching ? "Searching…" : "No matches"}
              hint="Try fewer words, scope:all, or check the qualifiers (Help: Search)."
            />
          ) : (
            <LibraryEmpty />
          )
        ) : searching ? (
          <EntityList rows={rows} label="Search results" versionLabel="Kind" onOpen={(r) => openRef(r.id)} onPreview={(r) => previewRef(r.id)} onCursor={onCursor} />
        ) : (
          <EntityList
            rows={rows}
            label="Project work and registry versions"
            onOpen={openRow}
            onPreview={(r) => select(WORK.test(r.id) ? r.id : `registry:${r.id}`, undefined)}
            onCursor={onCursor}
          />
        )}
      </div>
    </div>
  );
}

/** The agent's help on the Library: the highlighted row, a search in plain words, or what the Library is. */
function LibraryAgentMenu({ row, query }: { row: ListRow | undefined; query: string }) {
  const q = query.trim();
  const ref = row ? rowReference(row) : undefined;
  return (
    <AgentMenu
      items={[
        {
          label: row ? `Ask agent about ${row.name}` : "Ask agent about the highlighted row",
          disabled: !row,
          command: "view.askAgent",
          run: () => row && askAgent({ refs: ref ? [ref] : [], intent: `Help me with ${row.name}${ref ? "" : ` (${row.id})`}` }),
        },
        {
          label: q ? `Ask agent to find “${q.length > 32 ? `${q.slice(0, 32)}…` : q}”` : "Ask agent to find something",
          command: "view.askAgent",
          run: () => askAgent({ refs: [], intent: q ? `Find in the library: ${q}` : "Help me find what I need in the library: " }),
        },
        { label: "Explain the Library", command: "agentSessions.new", run: () => void explainThis(null, "panels.library", "the Library panel, its search qualifiers and saved views") },
      ]}
    />
  );
}

/** A row attaches as its reference (`@mix:<id>`, `@version:<id>`; search hits already carry one); else it is named. */
function rowReference(r: ListRow): AgentReference | undefined {
  const at = r.id.indexOf(":");
  const ref = r.id.startsWith("@") ? r.id : at > 0 ? formatReference(r.id.slice(0, at), r.id.slice(at + 1)) : undefined;
  return ref && parseReference(ref) ? { ref, label: r.name } : undefined;
}

/** The project's work starts here: a visible way to create a mix (also in the palette as New mix…). */
function NewMixButton() {
  const cmd = useCommand("mixes.new");
  if (!cmd) return null;
  const disabled = cmd.enabled !== true;
  return (
    <Button
      type="button"
      size="sm"
      variant="outline"
      className="shrink-0"
      disabled={disabled}
      title={typeof cmd.enabled === "string" ? cmd.enabled : undefined}
      onClick={() => void cmd.run()}
    >
      <Plus aria-hidden="true" />
      New mix
    </Button>
  );
}
