import { useState } from "react";
import { useQuery, useQueryClient, type QueryClient } from "@tanstack/react-query";
import { branchesGetOptions, branchesListOptions, eventsListOptions, projectsGetOptions, recipesGetOptions, recipesListOptions } from "@/api/gen/@tanstack/react-query.gen";
import type { Branch, BranchDiff, Recipe } from "@/api/gen/types.gen";
import { Button } from "@/components/ui/button";
import { cn } from "@/lib/utils";
import { EmptyState, PanelToolbar } from "@/shell/entity/primitives";
import { openDocument, runCommand, useProject, useTopic, type PanelProps } from "@/shell/panel";

// The Recipe document (docs/spec/11-ui-panels.md, Recipe): one file of the project repository with its commit
// history, the repository's files to move between, and the open session and sync branches with their diff against
// main. Sync branches are accepted or discarded here; session branches are accepted with their agent session.

const QUERY_IDS = new Set(["recipesGet", "recipesList", "branchesList", "branchesGet"]);

function invalidateRepository(qc: QueryClient): void {
  void qc.invalidateQueries({ predicate: (q) => QUERY_IDS.has(String((q.queryKey[0] as { _id?: string } | undefined)?._id ?? "")) });
}

export function RecipeEmpty() {
  return <EmptyState step="review" title="No file open" hint="Open the repository from the Project document (Browse files) or the palette." />;
}

export function RecipePanel({ tab, entity }: PanelProps) {
  const routeProject = useProject();
  const project = typeof entity?.project === "string" && entity.project ? entity.project : routeProject;
  const qc = useQueryClient();
  useTopic(project ? ["recipe.*"] : null, () => invalidateRepository(qc));
  if (!entity || !project) return <RecipeEmpty />;
  const path = entity.id;
  switch (tab) {
    case "activity":
      return <Activity path={path} />;
    case "details":
      return <FileDetails project={project} path={path} />;
    case "lineage":
      return <EmptyState step="record" title="Lineage of recipe files arrives with pipeline runs" hint="Runs and pipeline runs will link the commit they used (phase 2)." />;
    case "notes":
      return <EmptyState step="record" title="Notes live in NOTES.md" hint="Open the Project document's Notes tab to add one." />;
    default:
      return <Overview project={project} path={path} />;
  }
}

function Overview({ project, path }: { project: string; path: string }) {
  return (
    <div className="@container flex flex-col">
      <div className="grid min-h-0 @3xl:grid-cols-[15rem_1fr]">
        <Files project={project} current={path} />
        <FileView project={project} path={path} />
      </div>
      <Branches project={project} />
    </div>
  );
}

function Files({ project, current }: { project: string; current: string }) {
  const q = useQuery(recipesListOptions({ path: { p: project } }));
  return (
    <nav aria-label="Repository files" className="border-b @3xl:border-r @3xl:border-b-0">
      <PanelToolbar className="h-8">
        <span className="text-[11px] font-medium tracking-wide text-muted-foreground uppercase">Files</span>
        {q.data?.commit ? <code className="ml-auto text-[11px] text-muted-foreground">main {q.data.commit.slice(0, 7)}</code> : null}
      </PanelToolbar>
      <ul className="max-h-80 overflow-auto py-1 text-xs @3xl:max-h-[32rem]">
        {(q.data?.items ?? []).map((f) => (
          <li key={f.path}>
            <button
              type="button"
              onClick={() => openDocument(`recipe:${f.path}`)}
              aria-current={f.path === current ? "page" : undefined}
              className={cn("flex min-h-6 w-full items-center gap-2 px-3 text-left hover:bg-hover", f.path === current && "bg-selected font-medium")}
            >
              <span className="truncate" title={f.path}>
                {f.path}
              </span>
              <span className="ml-auto shrink-0 text-[11px] text-muted-foreground tabular-nums">{f.bytes.toLocaleString()}</span>
            </button>
          </li>
        ))}
        {q.data && q.data.items.length === 0 ? <li className="px-3 py-2 text-muted-foreground">The repository has no files yet.</li> : null}
      </ul>
    </nav>
  );
}

function decode(r: Recipe): string | null {
  if (r.encoding === "utf-8") return r.content;
  return null;
}

function FileView({ project, path }: { project: string; path: string }) {
  const q = useQuery(recipesGetOptions({ path: { p: project, path } }));
  const r = q.data;
  if (!r) return <div className="p-3 text-xs text-muted-foreground">{q.error ? `${path} could not be read.` : "Loading…"}</div>;
  const text = decode(r);
  const lines = text?.replace(/\n$/, "").split("\n") ?? [];
  return (
    <div className="flex min-w-0 flex-col">
      {text === null ? (
        <p className="p-3 text-xs text-muted-foreground">Binary file ({r.bytes.toLocaleString()} bytes); not shown.</p>
      ) : (
        <pre className="max-h-[32rem] overflow-auto py-2 font-mono text-xs leading-5" data-testid="recipe-content" aria-label={`${path} at ${r.ref}`}>
          {lines.map((l, i) => (
            <div key={i} className="grid grid-cols-[3rem_1fr]">
              <span aria-hidden className="pr-3 text-right text-muted-foreground select-none tabular-nums">
                {i + 1}
              </span>
              <span className="pr-3 whitespace-pre">{l || " "}</span>
            </div>
          ))}
        </pre>
      )}
      <section aria-labelledby="recipe-history" className="border-t">
        <h3 id="recipe-history" className="px-3 pt-2 text-[11px] font-medium tracking-wide text-muted-foreground uppercase">
          History
        </h3>
        <ol className="px-3 py-1 text-xs">
          {r.history.map((c) => (
            <li key={c.sha} className="flex min-h-7 items-center gap-3 border-b last:border-0">
              <code className="shrink-0 text-muted-foreground">{c.sha.slice(0, 7)}</code>
              <span className="min-w-0 truncate" title={c.message}>
                {c.message}
              </span>
              <span className="ml-auto shrink-0 text-muted-foreground">{c.author}</span>
              <time className="shrink-0 text-muted-foreground tabular-nums">{new Date(c.at).toLocaleString()}</time>
            </li>
          ))}
        </ol>
      </section>
    </div>
  );
}

function Branches({ project }: { project: string }) {
  const qc = useQueryClient();
  const list = useQuery(branchesListOptions({ path: { p: project } }));
  const proj = useQuery(projectsGetOptions({ path: { p: project } }));
  const [selected, setSelected] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [message, setMessage] = useState<{ error: boolean; text: string } | null>(null);
  const items = list.data?.items ?? [];
  const active = selected && items.some((b) => b.name === selected) ? selected : null;
  const sync = async () => {
    if (!proj.data) return;
    setBusy(true);
    setMessage(null);
    try {
      const res = await runCommand("projects.sync", { project, rev: proj.data.rev });
      if (res?.upToDate) setMessage({ error: false, text: "Templates and skills are up to date." });
      else if (res?.branch) {
        setMessage({ error: false, text: `${res.changes.length} file(s) changed; review ${res.branch} below.` });
        setSelected(res.branch);
      }
      invalidateRepository(qc);
    } catch (err) {
      setMessage({ error: true, text: err instanceof Error ? err.message : String(err) });
    } finally {
      setBusy(false);
    }
  };
  return (
    <section aria-labelledby="recipe-branches" className="border-t">
      <PanelToolbar className="h-8">
        <h3 id="recipe-branches" className="text-[11px] font-medium tracking-wide text-muted-foreground uppercase">
          Open branches
        </h3>
        {list.data?.main ? <code className="text-[11px] text-muted-foreground">main {list.data.main.slice(0, 7)}</code> : null}
        <Button size="xs" variant="outline" className="ml-auto" disabled={busy || !proj.data} onClick={() => void sync()}>
          Sync templates
        </Button>
      </PanelToolbar>
      {message ? (
        <p role={message.error ? "alert" : "status"} className={cn("px-3 pt-2 text-xs", message.error ? "text-destructive" : "text-muted-foreground")}>
          {message.text}
        </p>
      ) : null}
      {items.length === 0 ? (
        <p className="px-3 py-2 text-xs text-muted-foreground">No open branches: agent sessions and template syncs will show theirs here with a diff against main.</p>
      ) : (
        <ul className="px-1 py-1 text-xs" aria-label="Branches">
          {items.map((b) => (
            <BranchRow key={b.name} b={b} active={b.name === active} onSelect={() => setSelected(b.name === active ? null : b.name)} />
          ))}
        </ul>
      )}
      {active ? <BranchDiffView project={project} name={active} onDone={() => setSelected(null)} /> : null}
    </section>
  );
}

function BranchRow({ b, active, onSelect }: { b: Branch; active: boolean; onSelect: () => void }) {
  return (
    <li>
      <button
        type="button"
        aria-expanded={active}
        onClick={onSelect}
        className={cn("grid min-h-7 w-full grid-cols-[1fr_auto_auto_auto] items-center gap-3 rounded px-2 text-left hover:bg-hover", active && "bg-selected")}
      >
        <span className="min-w-0 truncate font-medium" title={b.subject}>
          {b.name}
          {b.subject ? <span className="ml-2 font-normal text-muted-foreground">{b.subject}</span> : null}
        </span>
        <span className="rounded-full border px-1.5 text-[11px] text-muted-foreground">{b.kind}</span>
        <span className="text-muted-foreground tabular-nums" title="Commits ahead of / behind main">
          +{b.ahead} / −{b.behind}
        </span>
        <time className="text-muted-foreground tabular-nums">{new Date(b.updatedAt).toLocaleString()}</time>
      </button>
    </li>
  );
}

function patchLineClass(l: string): string {
  if (l.startsWith("diff --git")) return "mt-2 border-t pt-1 font-medium text-foreground";
  if (l.startsWith("+++") || l.startsWith("---") || l.startsWith("index ")) return "text-muted-foreground";
  if (l.startsWith("@@")) return "text-accent-text";
  if (l.startsWith("+")) return "bg-diff-added text-diff-added-foreground";
  if (l.startsWith("-")) return "bg-diff-removed text-diff-removed-foreground";
  return "";
}

function BranchDiffView({ project, name, onDone }: { project: string; name: string; onDone: () => void }) {
  const qc = useQueryClient();
  const q = useQuery(branchesGetOptions({ path: { p: project, name } }));
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const d: BranchDiff | undefined = q.data;
  if (!d) return <p className="px-3 py-2 text-xs text-muted-foreground">{q.error ? `${name} could not be read.` : "Loading the diff…"}</p>;
  const act = async (id: "branches.accept" | "branches.revert") => {
    setBusy(true);
    setError(null);
    try {
      await runCommand(id, { project, name: d.name, head: d.head });
      invalidateRepository(qc);
      onDone();
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      setBusy(false);
    }
  };
  return (
    <div className="mx-2 mb-3 rounded-md border" data-testid="branch-diff">
      <div className="flex flex-wrap items-center gap-2 border-b px-2 py-1.5 text-xs">
        <span className="font-medium">{d.name}</span>
        <code className="text-muted-foreground">{d.head.slice(0, 7)}</code>
        <span className="text-muted-foreground">
          {d.fastForward ? "fast-forwards main" : d.conflicts.length ? `${d.conflicts.length} conflicting file(s)` : "merges cleanly (merge commit)"}
        </span>
        {d.kind === "sync" ? (
          <span className="ml-auto flex gap-1">
            <Button size="xs" variant="outline" disabled={busy} onClick={() => void act("branches.revert")}>
              Discard
            </Button>
            <Button size="xs" disabled={busy || d.conflicts.length > 0} onClick={() => void act("branches.accept")}>
              Accept into main
            </Button>
          </span>
        ) : d.kind === "session" ? (
          <span className="ml-auto text-[11px] text-muted-foreground">Accepted or discarded with its agent session</span>
        ) : null}
      </div>
      {error ? (
        <p role="alert" className="px-2 pt-1.5 text-xs text-destructive">
          {error}
        </p>
      ) : null}
      {d.conflicts.length ? (
        <p className="px-2 pt-1.5 text-xs text-status-failed-foreground">Conflicts: {d.conflicts.join(", ")}</p>
      ) : null}
      <ul className="px-2 py-1.5 text-xs" aria-label="Changed files">
        {d.files.map((f) => (
          <li key={f.path} className="flex min-h-6 items-center gap-2">
            <span className="w-16 shrink-0 text-muted-foreground">{f.status}</span>
            <button type="button" className="min-w-0 truncate text-left hover:underline" onClick={() => openDocument(`recipe:${f.path}`)}>
              {f.path}
            </button>
            <span className="ml-auto shrink-0 tabular-nums">
              {f.binary ? (
                "binary"
              ) : (
                <>
                  <span className="text-diff-added-foreground">+{f.additions}</span> <span className="text-diff-removed-foreground">−{f.deletions}</span>
                </>
              )}
            </span>
          </li>
        ))}
      </ul>
      <pre className="max-h-[28rem] overflow-auto border-t py-1 font-mono text-xs leading-5" aria-label={`Diff of ${d.name} against main`}>
        {d.patch.split("\n").map((l, i) => (
          <div key={i} className={cn("px-2 whitespace-pre", patchLineClass(l))}>
            {l || " "}
          </div>
        ))}
      </pre>
      {d.truncated ? <p className="border-t px-2 py-1 text-[11px] text-muted-foreground">The diff was cut at 512 KiB.</p> : null}
    </div>
  );
}

function FileDetails({ project, path }: { project: string; path: string }) {
  const q = useQuery(recipesGetOptions({ path: { p: project, path } }));
  const r = q.data;
  if (!r) return null;
  const rows: [string, string][] = [
    ["Path", r.path],
    ["Read at", r.ref],
    ["Commit", r.commit],
    ["Size", `${r.bytes.toLocaleString()} bytes`],
    ["Encoding", r.encoding],
    ["Commits", String(r.history.length)],
  ];
  return (
    <dl className="grid grid-cols-[9rem_1fr] gap-x-4 gap-y-1.5 p-4 text-xs">
      {rows.map(([k, v]) => (
        <div key={k} className="contents">
          <dt className="text-muted-foreground">{k}</dt>
          <dd className="break-all">{v}</dd>
        </div>
      ))}
    </dl>
  );
}

function Activity({ path }: { path: string }) {
  const topic = `recipe.${path}`;
  const q = useQuery(eventsListOptions({ query: { topics: topic, limit: 50 } }));
  useTopic([topic], () => void q.refetch());
  const items = [...(q.data?.items ?? [])].reverse();
  if (items.length === 0) return <EmptyState step="record" title="No changes recorded yet" hint="Commits to main and session branches that touch this file appear here." />;
  return (
    <ol className="flex flex-col px-4 py-2 text-xs">
      {items.map((e) => {
        const p = e.payload as { branch?: string; commit?: string; status?: string } | undefined;
        return (
          <li key={e.seq} className="flex h-8 items-center gap-3 border-b last:border-0">
            <time className="text-muted-foreground tabular-nums">{new Date(e.at).toLocaleString()}</time>
            <span className="font-medium">{p?.status ?? e.type}</span>
            <span className="text-muted-foreground">{p?.branch}</span>
            {p?.commit ? <code className="text-muted-foreground">{p.commit.slice(0, 7)}</code> : null}
            <span className="ml-auto text-muted-foreground">{e.actor.name ?? e.actor.id}</span>
          </li>
        );
      })}
    </ol>
  );
}
