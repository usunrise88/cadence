import { useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { langpacksGetQueryKey, langpacksListOptions } from "@/api/gen/@tanstack/react-query.gen";
import type { BoostList, LanguagePack, LanguagePackFile, ProblemFieldError } from "@/api/gen/types.gen";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { cn } from "@/lib/utils";
import { EmptyState, PanelToolbar } from "@/shell/entity/primitives";
import { errorMessage, invalidateLangpacks, openDocument, problemOf, runCommand, textDirection, useEditRequest, useProject, type PanelProps } from "@/shell/panel";
import { domainError, initialFile, parseTerms, weightError } from "./model";

// The Language pack document (docs/spec/11-ui-panels.md "Panel catalogue", Language pack; R21, R24): one locale of the
// project, lang/<locale>/ on main (langpacks.get). The files with a YAML editor (langpacks.edit: If-Match is the pack's
// commit; Check runs the server's dry run and Commit is offered for the checked text only), the scoring normalizer it
// references, and the boost lists with their weights (boost.edit, checked the same way). An agent's edit under the
// draft policy lands on a branch the header names. "Test a phrase" waits for a one-utterance eval or the Transcription
// panel (see the help article).

export function LanguagePackEmpty() {
  return <EmptyState step="prepare" title="No language pack open" hint="Open a language pack from the Library (this project's work) or the palette." />;
}

export function LanguagePackPanel({ tab, entity, doc }: PanelProps) {
  const pack = entity?.pack as LanguagePack | undefined;
  const routeProject = useProject();
  const project = typeof entity?.project === "string" && entity.project ? entity.project : routeProject;
  if (!entity || !pack || !project) return <LanguagePackEmpty />;
  switch (tab) {
    case "details":
      return <Packs project={project} current={pack.locale} />;
    case "lineage":
    case "activity":
    case "notes":
      return <EmptyState step="record" title="The pack's history is its commits" hint="Open lang/<locale>/ files in the Recipe document for their commit history." />;
    default:
      return <Overview key={pack.locale} project={project} pack={pack} doc={doc} />;
  }
}

function Overview({ project, pack, doc }: { project: string; pack: LanguagePack; doc?: string }) {
  const [path, setPath] = useState<string | undefined>(() => initialFile(pack));
  const [editing, setEditing] = useState(false);
  useEditRequest(doc, () => setEditing(true));
  const file = pack.files.find((f) => f.path === path);
  return (
    <div className="@container flex flex-col text-xs" data-pack={pack.locale}>
      {pack.branch ? (
        <p role="status" className="border-b px-4 py-2 text-status-warning-foreground">
          Read from branch <code>{pack.branch}</code> (a drafted edit): accept it from the Recipe document's branches or Agent sessions.
        </p>
      ) : null}
      {pack.issues.length ? (
        <div role="alert" className="border-b px-4 py-2 text-status-warning-foreground" data-slot="pack-issues">
          <p>Some files do not check out:</p>
          <ul className="list-disc pl-5">
            {pack.issues.map((i, n) => (
              <li key={n}>
                <code className="mr-1">{i.path}</code>
                {i.message}
              </li>
            ))}
          </ul>
        </div>
      ) : null}
      <p className="border-b px-4 py-2">
        Scoring normalizer <code className="text-[11px]">{pack.scoring.normalizer}</code>
        {pack.scoring.versionId ? (
          <span className="text-muted-foreground">
            {" "}
            → {pack.scoring.version} (<code className="text-[11px]">{pack.scoring.versionId}</code>)
          </span>
        ) : (
          <span className="text-status-warning-foreground"> — the registry has no version of it</span>
        )}
      </p>
      <div className="grid min-h-0 @3xl:grid-cols-[14rem_1fr]">
        <nav aria-label="Pack files" className="border-b @3xl:border-r @3xl:border-b-0">
          <PanelToolbar className="h-8">
            <span className="text-[11px] font-medium tracking-wide text-muted-foreground uppercase">lang/{pack.locale}</span>
            <code className="ml-auto text-[11px] text-muted-foreground">{pack.sha.slice(0, 7)}</code>
          </PanelToolbar>
          <ul className="max-h-80 overflow-auto py-1">
            {pack.files.map((f) => (
              <li key={f.path}>
                <button
                  type="button"
                  onClick={() => (setPath(f.path), setEditing(false))}
                  aria-current={f.path === path ? "page" : undefined}
                  className={cn("flex min-h-6 w-full items-center gap-2 px-3 text-left hover:bg-hover", f.path === path && "bg-selected font-medium")}
                >
                  <span className="truncate">{f.path}</span>
                  <span className="ml-auto shrink-0 text-[11px] text-muted-foreground tabular-nums">{f.bytes.toLocaleString()}</span>
                </button>
              </li>
            ))}
          </ul>
        </nav>
        <div className="min-w-0">
          {file ? (
            editing ? (
              <FileEditor key={`${file.path}@${pack.sha}`} project={project} pack={pack} file={file} onDone={() => setEditing(false)} />
            ) : (
              <FileView file={file} locale={pack.locale} onEdit={() => setEditing(true)} />
            )
          ) : (
            <p className="p-3 text-muted-foreground">The pack has no files.</p>
          )}
        </div>
      </div>
      <BoostLists project={project} pack={pack} />
    </div>
  );
}

function FileView({ file, locale, onEdit }: { file: LanguagePackFile; locale: string; onEdit: () => void }) {
  // Word lists (boost/*.txt) are in the pack's language; YAML is code and reads left to right.
  const dir = file.path.endsWith(".txt") ? textDirection(locale) : "ltr";
  return (
    <div className="flex flex-col">
      <PanelToolbar className="h-8">
        <span className="truncate font-medium">{file.path}</span>
        <Button size="xs" variant="outline" className="ml-auto" onClick={onEdit} aria-label={`Edit ${file.path}`} data-command="langpacks.edit">
          Edit
        </Button>
      </PanelToolbar>
      <pre dir={dir} className="max-h-[28rem] overflow-auto p-3 font-mono text-xs leading-5 whitespace-pre-wrap" data-slot="pack-file">
        {file.content}
      </pre>
    </div>
  );
}

function useCommit(project: string, locale: string) {
  const qc = useQueryClient();
  return (next: LanguagePack) => {
    qc.setQueryData(langpacksGetQueryKey({ path: { p: project, locale } }), next);
    invalidateLangpacks(qc);
  };
}

/** A checked edit: Check (dry run) must pass for exactly the text that is committed. */
function useChecked<T>(value: T) {
  const [checked, setChecked] = useState<T | null>(null);
  return { fresh: checked !== null && JSON.stringify(checked) === JSON.stringify(value), mark: () => setChecked(value), reset: () => setChecked(null) };
}

function Problems({ error, fields }: { error: string | null; fields: ProblemFieldError[] }) {
  if (!error) return null;
  return (
    <div role="alert" className="text-destructive">
      <p>{error}</p>
      {fields.length ? (
        <ul className="mt-1 list-disc pl-5">
          {fields.map((f, i) => (
            <li key={i}>
              {f.path ? <code className="mr-1">{f.path}</code> : null}
              {f.message}
            </li>
          ))}
        </ul>
      ) : null}
    </div>
  );
}

function FileEditor({ project, pack, file, onDone }: { project: string; pack: LanguagePack; file: LanguagePackFile; onDone: () => void }) {
  const commit = useCommit(project, pack.locale);
  const qc = useQueryClient();
  const [text, setText] = useState(file.content);
  const [message, setMessage] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [fields, setFields] = useState<ProblemFieldError[]>([]);
  const [conflict, setConflict] = useState(false);
  const [note, setNote] = useState<string | null>(null);
  const checked = useChecked(text);
  const dirty = text !== file.content;
  const save = async (dryRun: boolean) => {
    setBusy(true);
    setError(null);
    setFields([]);
    setNote(null);
    try {
      const next = await runCommand("langpacks.edit", {
        project,
        locale: pack.locale,
        sha: pack.sha,
        dryRun,
        body: { files: [{ path: file.path, content: text }], message: message.trim() || `edit lang/${pack.locale}/${file.path}` },
      });
      if (!next) return;
      if (dryRun) {
        checked.mark();
        setNote(next.issues.length ? `Checked, with ${next.issues.length} issue${next.issues.length > 1 ? "s" : ""} in the pack.` : "Checked: it would commit cleanly.");
        return;
      }
      commit(next);
      onDone();
    } catch (err) {
      const p = problemOf(err);
      if (p?.status === 412) {
        setConflict(true);
        invalidateLangpacks(qc);
      } else {
        setError(errorMessage(err));
        setFields(p?.errors ?? []);
      }
      checked.reset();
    } finally {
      setBusy(false);
    }
  };
  return (
    <section aria-label={`Edit ${file.path}`} className="flex flex-col gap-2 p-3" data-slot="pack-editor">
      <textarea
        className="min-h-72 w-full resize-y rounded-md border bg-background p-2 font-mono text-xs leading-5 focus-visible:ring-2 focus-visible:ring-ring focus-visible:outline-none"
        value={text}
        spellCheck={false}
        dir={file.path.endsWith(".txt") ? textDirection(pack.locale) : "ltr"}
        aria-label={`${file.path} content`}
        onChange={(e) => setText(e.target.value)}
        onKeyDown={(e) => {
          // Tab indents (two spaces, YAML) instead of leaving the field; Esc then Tab moves focus on.
          if (e.key === "Tab" && !e.shiftKey && !e.altKey && !e.ctrlKey && !e.metaKey) {
            e.preventDefault();
            const t = e.currentTarget;
            const { selectionStart: a, selectionEnd: b } = t;
            setText(`${text.slice(0, a)}  ${text.slice(b)}`);
            requestAnimationFrame(() => t.setSelectionRange(a + 2, a + 2));
          }
        }}
      />
      <div className="flex flex-wrap items-center gap-2">
        <input className="h-7 min-w-48 flex-1 rounded-md border bg-background px-2 text-xs" placeholder={`edit lang/${pack.locale}/${file.path}`} aria-label="Commit message" value={message} onChange={(e) => setMessage(e.target.value)} />
        <Button size="xs" variant="outline" disabled={busy || !dirty || conflict} onClick={() => void save(true)} data-command="langpacks.edit">
          Check
        </Button>
        <Button size="xs" disabled={busy || !dirty || conflict || !checked.fresh} title={checked.fresh ? undefined : "Check the change first"} onClick={() => void save(false)} data-command="langpacks.edit">
          Commit to main
        </Button>
        <Button size="xs" variant="ghost" disabled={busy} onClick={onDone}>
          Cancel
        </Button>
      </div>
      {conflict ? (
        <p role="alert" className="text-status-warning-foreground">
          The pack changed on main while you edited it; nothing was committed. Copy your text if you need it, then{" "}
          <button type="button" className="underline underline-offset-2" onClick={onDone}>
            reload
          </button>
          .
        </p>
      ) : null}
      {note ? (
        <p role="status" className="text-muted-foreground">
          {note}
        </p>
      ) : null}
      <Problems error={error} fields={fields} />
    </section>
  );
}

function BoostLists({ project, pack }: { project: string; pack: LanguagePack }) {
  const [open, setOpen] = useState<string | null>(null);
  return (
    <section aria-labelledby={`boost-${pack.locale}`} className="flex flex-col gap-1.5 border-t p-4">
      <div className="flex items-center gap-2">
        <h3 id={`boost-${pack.locale}`} className="text-[11px] font-medium tracking-wide text-muted-foreground uppercase">
          Boost lists
        </h3>
        <Button size="xs" variant="outline" className="ml-auto" onClick={() => setOpen("")} data-command="boost.edit">
          New list…
        </Button>
      </div>
      {open === "" ? <BoostEditor key="new" project={project} pack={pack} onDone={() => setOpen(null)} /> : null}
      {pack.boost.length ? (
        <ul className="flex flex-col" aria-label="Boost lists" data-slot="boost-lists">
          {pack.boost.map((b) => (
            <li key={b.domain} className="flex flex-col border-b py-1 last:border-0" data-boost={b.domain}>
              <div className="flex min-h-7 flex-wrap items-center gap-2">
                <span className="font-medium">{b.domain}</span>
                <span className="text-muted-foreground tabular-nums">
                  {b.terms.length} terms · weight {b.weight}
                </span>
                <code className="text-[11px] text-muted-foreground" title="The identity an eval's decoding names">
                  {b.sha256.slice(0, 12)}
                </code>
                <Button size="xs" variant="ghost" className="ml-auto" onClick={() => setOpen(open === b.domain ? null : b.domain)} aria-expanded={open === b.domain} aria-label={`${open === b.domain ? "Close" : "Edit"} boost list ${b.domain}`}>
                  {open === b.domain ? "Close" : "Edit"}
                </Button>
              </div>
              {open === b.domain ? <BoostEditor key={`${b.domain}@${pack.sha}`} project={project} pack={pack} list={b} onDone={() => setOpen(null)} /> : null}
            </li>
          ))}
        </ul>
      ) : (
        <p className="text-muted-foreground">No boost lists yet. A list's terms are boosted when an eval names it as a decoding variant.</p>
      )}
    </section>
  );
}

function BoostEditor({ project, pack, list, onDone }: { project: string; pack: LanguagePack; list?: BoostList; onDone: () => void }) {
  const commit = useCommit(project, pack.locale);
  const qc = useQueryClient();
  const [domain, setDomain] = useState(list?.domain ?? "");
  const [text, setText] = useState(list?.terms.join("\n") ?? "");
  const [weight, setWeight] = useState(list ? String(list.weight) : "");
  const [message, setMessage] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [fields, setFields] = useState<ProblemFieldError[]>([]);
  const [note, setNote] = useState<string | null>(null);
  const terms = parseTerms(text);
  const body = { terms, ...(weight.trim() ? { weight: Number(weight) } : {}), ...(message.trim() ? { message: message.trim() } : {}) };
  const checked = useChecked({ domain, terms, weight });
  const invalid = domainError(domain) ?? weightError(weight) ?? (terms.length === 0 ? "Add at least one term" : undefined);
  const save = async (dryRun: boolean) => {
    setBusy(true);
    setError(null);
    setFields([]);
    setNote(null);
    try {
      const next = await runCommand("boost.edit", { project, locale: pack.locale, domain, sha: pack.sha, body, dryRun });
      if (dryRun) {
        checked.mark();
        const after = next.boost.find((b) => b.domain === domain);
        setNote(`Checked: ${after?.terms.length ?? terms.length} terms at weight ${after?.weight ?? weight}.`);
        return;
      }
      commit(next);
      onDone();
    } catch (err) {
      const p = problemOf(err);
      if (p?.status === 412) invalidateLangpacks(qc);
      setError(p?.status === 412 ? "The pack changed on main while you edited; nothing was committed. Reload and edit again." : errorMessage(err));
      setFields(p?.errors ?? []);
      checked.reset();
    } finally {
      setBusy(false);
    }
  };
  const id = `${pack.locale}-${list?.domain ?? "new"}`;
  return (
    <form
      onSubmit={(e) => {
        e.preventDefault();
        if (!invalid) void save(true);
      }}
      className="flex flex-col gap-2 rounded-md border bg-tool p-3"
      aria-label={list ? `Edit boost list ${list.domain}` : "New boost list"}
      data-slot="boost-editor"
    >
      <div className="grid grid-cols-[6rem_1fr] items-center gap-2">
        <label htmlFor={`boost-domain-${id}`} className="text-muted-foreground">
          List
        </label>
        <Input id={`boost-domain-${id}`} className="h-6 text-xs" disabled={!!list} placeholder="names" value={domain} onChange={(e) => setDomain(e.target.value)} aria-invalid={domain && domainError(domain) ? true : undefined} />
        <label htmlFor={`boost-weight-${id}`} className="text-muted-foreground">
          Weight
        </label>
        <Input id={`boost-weight-${id}`} className="h-6 w-24 text-xs tabular-nums" placeholder="default" value={weight} onChange={(e) => setWeight(e.target.value)} aria-invalid={weightError(weight) ? true : undefined} />
        <label htmlFor={`boost-terms-${id}`} className="self-start text-muted-foreground">
          Terms
        </label>
        <textarea
          id={`boost-terms-${id}`}
          dir={textDirection(pack.locale)}
          className="min-h-32 w-full resize-y rounded-md border bg-background p-2 text-xs leading-5"
          placeholder="One term per line"
          value={text}
          onChange={(e) => setText(e.target.value)}
        />
        <span />
        <span className="text-muted-foreground tabular-nums">{terms.length} distinct terms (blank lines and # comments are dropped)</span>
        <label htmlFor={`boost-msg-${id}`} className="text-muted-foreground">
          Message
        </label>
        <Input id={`boost-msg-${id}`} className="h-6 text-xs" placeholder={`boost: ${domain || "<list>"}`} value={message} onChange={(e) => setMessage(e.target.value)} />
      </div>
      <div className="flex gap-1">
        <Button type="submit" size="xs" variant="outline" disabled={busy || !!invalid} title={invalid}>
          Check
        </Button>
        <Button type="button" size="xs" disabled={busy || !!invalid || !checked.fresh} title={checked.fresh ? undefined : "Check the change first"} onClick={() => void save(false)} data-command="boost.edit">
          Commit to main
        </Button>
        <Button type="button" size="xs" variant="ghost" onClick={onDone}>
          Cancel
        </Button>
      </div>
      {invalid && (domain || text || weight) ? <p className="text-muted-foreground">{invalid}</p> : null}
      {note ? (
        <p role="status" className="text-muted-foreground">
          {note}
        </p>
      ) : null}
      <Problems error={error} fields={fields} />
    </form>
  );
}

/** Details: the project's other packs and the locales Cadence ships packs for. */
function Packs({ project, current }: { project: string; current: string }) {
  const q = useQuery(langpacksListOptions({ path: { p: project } }));
  return (
    <div className="flex flex-col gap-3 p-4 text-xs">
      {q.error ? <p className="text-destructive">{errorMessage(q.error)}</p> : null}
      <table className="w-full" aria-label="Language packs of the project">
        <thead className="text-left text-muted-foreground">
          <tr>
            <th className="font-normal">Locale</th>
            <th className="font-normal">Files</th>
            <th className="font-normal">Boost lists</th>
            <th className="font-normal">Scoring</th>
            <th className="font-normal">Commit</th>
          </tr>
        </thead>
        <tbody>
          {(q.data?.items ?? []).map((p) => (
            <tr key={p.locale} className={cn("h-7 border-t", p.locale === current && "font-medium")}>
              <td>
                <button type="button" className="underline-offset-2 hover:underline" onClick={() => openDocument(`language_pack:${p.locale}`)}>
                  {p.locale}
                </button>
              </td>
              <td className="tabular-nums">{p.files}</td>
              <td>{p.boost.join(", ") || "—"}</td>
              <td>{p.scoring.normalizer}</td>
              <td>
                <code className="text-[11px]">{p.sha.slice(0, 7)}</code>
              </td>
            </tr>
          ))}
        </tbody>
      </table>
      {q.data?.shipped.length ? <p className="text-muted-foreground">Cadence ships starter packs for: {q.data.shipped.join(", ")}.</p> : null}
    </div>
  );
}
