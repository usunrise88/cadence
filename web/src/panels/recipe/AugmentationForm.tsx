import { useMemo, useState } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { recipesGetQueryKey } from "@/api/gen/@tanstack/react-query.gen";
import type { Recipe } from "@/api/gen/types.gen";
import { Button } from "@/components/ui/button";
import {
  AUGMENT_DIR,
  AUGMENTATION_PROFILE_SCHEMA,
  departuresOf,
  errorMessage,
  newProfile,
  openDocument,
  parseProfile,
  problemOf,
  recommended,
  runCommand,
  SchemaForm,
  useDefaults,
  writeProfile,
  type Values,
} from "@/shell/panel";

// The augmentation profile as a form (docs/spec/03-pipelines-defaults.md "Augmentation": "the Recipe document renders
// a profile as a schema-driven form"): codec, band-limit, level and speed, each with its probability and range, and
// the seed. Values, descriptions, sources and safe ranges come from defaults.yaml (augment.*); "Reset to recommended"
// writes them back. Save commits the file to main (recipes.edit, If-Match: the commit that last changed it), keeping
// the file's comments and any keys the form does not know.

export function AugmentationForm({ project, recipe }: { project: string; recipe: Recipe }) {
  const qc = useQueryClient();
  const defaults = useDefaults().data;
  const parsed = useMemo(() => parseProfile(recipe.content), [recipe.content]);
  const [local, setLocal] = useState<Values | null>(null);
  const [message, setMessage] = useState<{ error: boolean; text: string } | null>(null);
  const [conflict, setConflict] = useState(false);
  const [busy, setBusy] = useState(false);
  const base = recipe.history[0]?.sha;
  if (parsed.error !== undefined) {
    return (
      <p role="alert" className="border-b px-3 py-2 text-xs text-status-warning-foreground" data-slot="augmentation-form">
        This profile does not parse ({parsed.error}); edit the file as text.
      </p>
    );
  }
  const values = local ?? parsed.values;
  const dirty = local !== null;
  const departures = departuresOf(AUGMENTATION_PROFILE_SCHEMA, values, defaults);
  const reset = () => setLocal({ ...values, ...recommended(AUGMENTATION_PROFILE_SCHEMA, defaults) });
  const save = async () => {
    if (!local || !base) return;
    setBusy(true);
    setMessage(null);
    try {
      const next = await runCommand("recipes.edit", {
        project,
        path: recipe.path,
        expect: base,
        body: { content: writeProfile(recipe.content, local), message: `augment: update ${recipe.path.slice(AUGMENT_DIR.length)}` },
      });
      qc.setQueryData(recipesGetQueryKey({ path: { p: project, path: recipe.path } }), next);
      setLocal(null);
      setConflict(false);
      setMessage({ error: false, text: `Committed ${next.history[0]?.sha.slice(0, 7) ?? ""} to main.` });
    } catch (err) {
      if (problemOf(err)?.status === 412) {
        setConflict(true);
        void qc.invalidateQueries({ queryKey: recipesGetQueryKey({ path: { p: project, path: recipe.path } }) });
      } else setMessage({ error: true, text: errorMessage(err) });
    } finally {
      setBusy(false);
    }
  };
  return (
    <section aria-labelledby="augmentation-form" className="flex flex-col gap-2 border-b p-3 text-xs" data-slot="augmentation-form">
      <div className="flex flex-wrap items-center gap-2">
        <h3 id="augmentation-form" className="text-[11px] font-medium tracking-wide text-muted-foreground uppercase">
          Augmentation profile{typeof values.name === "string" ? ` · ${values.name}` : ""}
        </h3>
        <span className="text-muted-foreground" data-slot="departure-count">
          {departures.length ? `${departures.length} departure${departures.length === 1 ? "" : "s"} from recommended` : "at recommended values"}
        </span>
        <span className="ml-auto flex flex-wrap gap-1">
          <Button size="xs" variant="outline" onClick={reset} disabled={busy || departures.length === 0}>
            Reset to recommended
          </Button>
          <Button size="xs" variant="ghost" onClick={() => setLocal(null)} disabled={!dirty || busy}>
            Discard
          </Button>
          <Button size="xs" onClick={() => void save()} disabled={!dirty || busy || conflict || !base} data-command="recipes.edit">
            Save
          </Button>
        </span>
      </div>
      <SchemaForm schema={AUGMENTATION_PROFILE_SCHEMA} value={values} onChange={setLocal} label="Augmentation profile" disabled={busy} />
      {conflict ? (
        <div role="alert" className="flex flex-wrap items-center gap-2 border-l-2 border-status-warning py-1 pl-2">
          <span className="text-status-warning-foreground">The file changed on main while you edited it; nothing was committed.</span>
          <Button size="xs" variant="outline" className="ml-auto" onClick={() => (setLocal(null), setConflict(false))}>
            Reload
          </Button>
        </div>
      ) : null}
      {message ? (
        <p role={message.error ? "alert" : "status"} className={message.error ? "text-destructive" : "text-muted-foreground"}>
          {message.text}
        </p>
      ) : null}
    </section>
  );
}

/** "New augmentation profile": commits augment/<name>.yaml at the recommended values and opens it. */
export function NewProfileButton({ project, existing }: { project: string; existing: string[] }) {
  const defaults = useDefaults().data;
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const name = ["telephony", "telephony-2", "telephony-3"].find((n) => !existing.includes(`${AUGMENT_DIR}${n}.yaml`)) ?? `profile-${existing.length + 1}`;
  const path = `${AUGMENT_DIR}${name}.yaml`;
  const create = async () => {
    setBusy(true);
    setError(null);
    try {
      await runCommand("recipes.new", { project, body: { path, content: newProfile(name, recommended(AUGMENTATION_PROFILE_SCHEMA, defaults)), message: `augment: add ${name}` } });
      openDocument(`recipe:${path}`);
    } catch (err) {
      setError(errorMessage(err));
    } finally {
      setBusy(false);
    }
  };
  return (
    <>
      <Button size="xs" variant="ghost" className="ml-auto" disabled={busy || !defaults} onClick={() => void create()} title={`Create ${path} at the recommended values`} data-command="recipes.new">
        New augmentation profile
      </Button>
      {error ? (
        <span role="alert" className="text-[11px] text-destructive">
          {error}
        </span>
      ) : null}
    </>
  );
}
