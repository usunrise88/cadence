import { useState } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { recipesGetQueryKey } from "@/api/gen/@tanstack/react-query.gen";
import type { ProblemFieldError, Recipe } from "@/api/gen/types.gen";
import { Button } from "@/components/ui/button";
import { errorMessage, problemOf, runCommand } from "@/shell/panel";

// The Recipe document's text editor: any UTF-8 file of the project repository on main, committed with recipes.edit
// (If-Match on the commit that last changed the file, history[0].sha). The server plans a pipeline file
// (pipelines/<name>.yaml) before it commits it, so a pipeline that would not run never reaches main; its problems
// come back as pipeline-invalid with one entry per field. The files the agent profile renders change only through
// agentProfile.edit (the server answers 409), so they offer no editor.

const PROFILE_FILES = new Set(["AGENTS.md", "CLAUDE.md", ".claude/settings.json", "opencode.json"]);

/** Whether the editor offers to edit path (the agent profile's rendered files do not). */
export function editable(path: string): boolean {
  return !PROFILE_FILES.has(path);
}

/** The default commit message for an edit of path. */
export function defaultMessage(path: string): string {
  return `edit ${path}`;
}

export function TextEditor({ project, recipe, onDone }: { project: string; recipe: Recipe; onDone: () => void }) {
  const qc = useQueryClient();
  const [text, setText] = useState(recipe.content);
  const [message, setMessage] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [fields, setFields] = useState<ProblemFieldError[]>([]);
  const [conflict, setConflict] = useState(false);
  const [checked, setChecked] = useState(false);
  const base = recipe.history[0]?.sha;
  const dirty = text !== recipe.content;
  const key = recipesGetQueryKey({ path: { p: project, path: recipe.path } });
  const save = async (dryRun: boolean) => {
    if (!base) return;
    setBusy(true);
    setError(null);
    setFields([]);
    setChecked(false);
    try {
      const next = await runCommand("recipes.edit", {
        project,
        path: recipe.path,
        expect: base,
        dryRun,
        body: { content: text, message: message.trim() || defaultMessage(recipe.path) },
      });
      if (dryRun) {
        setChecked(true);
        return;
      }
      qc.setQueryData(key, next);
      onDone();
    } catch (err) {
      const p = problemOf(err);
      if (p?.status === 412) {
        setConflict(true);
        void qc.invalidateQueries({ queryKey: key });
      } else {
        setError(errorMessage(err));
        setFields(p?.errors ?? []);
      }
    } finally {
      setBusy(false);
    }
  };
  return (
    <section aria-label={`Edit ${recipe.path}`} className="flex flex-col gap-2 p-3 text-xs" data-slot="recipe-editor">
      <textarea
        className="min-h-80 w-full resize-y rounded-md border bg-background p-2 font-mono text-xs leading-5 focus-visible:ring-2 focus-visible:ring-ring focus-visible:outline-none"
        value={text}
        spellCheck={false}
        aria-label={`${recipe.path} content`}
        data-testid="recipe-editor-text"
        onChange={(e) => {
          setText(e.target.value);
          setChecked(false);
        }}
        onKeyDown={(e) => {
          // Tab indents (two spaces, YAML) instead of leaving the field; Esc then Tab still moves focus on.
          if (e.key === "Tab" && !e.shiftKey && !e.altKey && !e.ctrlKey && !e.metaKey) {
            e.preventDefault();
            const t = e.currentTarget;
            const { selectionStart: a, selectionEnd: b } = t;
            const next = `${text.slice(0, a)}  ${text.slice(b)}`;
            setText(next);
            requestAnimationFrame(() => t.setSelectionRange(a + 2, a + 2));
          }
        }}
      />
      <div className="flex flex-wrap items-center gap-2">
        <input
          className="h-7 min-w-48 flex-1 rounded-md border bg-background px-2 text-xs"
          placeholder={defaultMessage(recipe.path)}
          aria-label="Commit message"
          value={message}
          onChange={(e) => setMessage(e.target.value)}
        />
        <Button size="xs" variant="outline" disabled={busy || !dirty || conflict} onClick={() => void save(true)} data-command="recipes.edit">
          Check
        </Button>
        <Button size="xs" disabled={busy || !dirty || conflict || !base} onClick={() => void save(false)} data-command="recipes.edit">
          Commit to main
        </Button>
        <Button size="xs" variant="ghost" disabled={busy} onClick={onDone}>
          Cancel
        </Button>
      </div>
      {conflict ? (
        <p role="alert" className="text-status-warning-foreground">
          The file changed on main while you edited it; nothing was committed. Copy your text if you need it, then{" "}
          <button type="button" className="underline underline-offset-2" onClick={onDone}>
            reload
          </button>
          .
        </p>
      ) : null}
      {checked ? (
        <p role="status" className="text-muted-foreground">
          Checked: it would commit cleanly{recipe.path.startsWith("pipelines/") ? ", and the pipeline plans" : ""}.
        </p>
      ) : null}
      {error ? (
        <div role="alert" className="text-destructive">
          <p>{error}</p>
          {fields.length > 0 ? (
            <ul className="mt-1 list-disc pl-5" data-slot="recipe-editor-problems">
              {fields.map((f, i) => (
                <li key={i}>
                  {f.path ? <code className="mr-1">{f.path}</code> : null}
                  {f.message}
                </li>
              ))}
            </ul>
          ) : null}
        </div>
      ) : null}
    </section>
  );
}
