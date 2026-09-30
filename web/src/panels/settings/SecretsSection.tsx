import { useId, useState, type FormEvent } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { projectsListOptions, secretsListOptions, secretsListQueryKey } from "@/api/gen/@tanstack/react-query.gen";
import type { Secret, SecretKind, SecretList } from "@/api/gen/types.gen";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { errorMessage, problemOf, runCommand, useTopic } from "@/shell/panel";
import { Field, SectionHeading, Table, Td, when } from "./ui";

// Secrets (docs/spec/08-resolutions.md R9): names, kind, scope and last use. The value is write-only — typed once,
// sent once, never returned by any operation and never shown again; jobs and bootstrap read it on the server.

export const SECRET_KINDS: SecretKind[] = ["huggingface", "ngc", "github", "s3", "judge-api", "other"];
const NAME = /^[a-z][a-z0-9_-]{1,62}$/;

export function upsertSecret(list: SecretList | undefined, s: Secret): SecretList | undefined {
  if (!list) return list;
  const items = list.items.some((x) => x.id === s.id) ? list.items.map((x) => (x.id === s.id ? s : x)) : [...list.items, s];
  return { ...list, items: items.sort((a, b) => a.name.localeCompare(b.name)) };
}

/**
 * A write-only value: a password field that never echoes what was stored. The parent clears it after the command
 * went through; nothing reads it back.
 */
export function WriteOnlyField({ id, label, value, onChange, required }: { id: string; label: string; value: string; onChange: (v: string) => void; required?: boolean }) {
  const hint = `${id}-hint`;
  return (
    <Field label={label} htmlFor={id} hint="Write-only: stored encrypted, never shown again — not here, not in the API, not to agents.">
      <Input
        id={id}
        name={id}
        type="password"
        autoComplete="new-password"
        spellCheck={false}
        value={value}
        required={required}
        onChange={(e) => onChange(e.target.value)}
        aria-describedby={hint}
        data-write-only=""
        className="h-7 font-mono text-xs"
      />
      <span id={hint} className="sr-only">
        Write-only value
      </span>
    </Field>
  );
}

export function SecretForm({ projects, onStored }: { projects: string[]; onStored?: (s: Secret) => void }) {
  const qc = useQueryClient();
  const uid = useId();
  const [name, setName] = useState("");
  const [kind, setKind] = useState<SecretKind>("huggingface");
  const [scope, setScope] = useState("instance");
  const [value, setValue] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [stored, setStored] = useState<string | null>(null);
  const nameError = name && !NAME.test(name) ? "Lowercase letters, digits, dash and underscore; starts with a letter" : undefined;

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    setBusy(true);
    setError(null);
    setStored(null);
    try {
      const s = await runCommand("secrets.new", { body: { name, kind, scope, value } });
      setValue(""); // the value leaves the page with the request
      setName("");
      setStored(s.name);
      qc.setQueryData<SecretList>(secretsListQueryKey(), (old) => upsertSecret(old, s));
      onStored?.(s);
    } catch (err) {
      setError(problemOf(err)?.status === 409 ? `A secret named “${name}” exists already.` : errorMessage(err));
    } finally {
      setBusy(false);
    }
  };

  return (
    <form onSubmit={submit} aria-label="Add secret" className="grid gap-2 rounded-md border p-3 @md:grid-cols-2">
      <Field label="Name" htmlFor={`${uid}-name`} error={nameError}>
        <Input id={`${uid}-name`} value={name} onChange={(e) => setName(e.target.value)} required placeholder="hf-token" className="h-7 text-xs" autoComplete="off" />
      </Field>
      <Field label="Kind" htmlFor={`${uid}-kind`}>
        <select id={`${uid}-kind`} value={kind} onChange={(e) => setKind(e.target.value as SecretKind)} className="h-7 rounded-md border border-input bg-background px-2 text-xs">
          {SECRET_KINDS.map((k) => (
            <option key={k} value={k}>
              {k}
            </option>
          ))}
        </select>
      </Field>
      <Field label="Scope" htmlFor={`${uid}-scope`} hint="Instance: every project's jobs may read it. A project: only that project's jobs and bootstrap.">
        <select id={`${uid}-scope`} value={scope} onChange={(e) => setScope(e.target.value)} className="h-7 rounded-md border border-input bg-background px-2 text-xs">
          <option value="instance">instance</option>
          {projects.map((p) => (
            <option key={p} value={`project:${p}`}>
              project:{p}
            </option>
          ))}
        </select>
      </Field>
      <WriteOnlyField id={`${uid}-value`} label="Value" value={value} onChange={setValue} required />
      <div className="flex items-center gap-2 @md:col-span-2">
        <Button type="submit" size="xs" disabled={busy || !name || !value || !!nameError} data-command="secrets.new">
          Add secret
        </Button>
        {stored ? (
          <span role="status" className="text-xs text-muted-foreground">
            Stored “{stored}”. Its value is never shown again.
          </span>
        ) : null}
        {error ? (
          <span role="alert" className="text-xs text-destructive">
            {error}
          </span>
        ) : null}
      </div>
    </form>
  );
}

export function SecretsSection() {
  const qc = useQueryClient();
  const { data, isLoading } = useQuery(secretsListOptions());
  const projects = useQuery(projectsListOptions());
  useTopic(["entity.secret.*"], (batch) => {
    for (const e of batch) {
      const s = (e.payload as { secret?: Secret } | undefined)?.secret;
      if (s) qc.setQueryData<SecretList>(secretsListQueryKey(), (old) => upsertSecret(old, s));
    }
  });
  const items = data?.items ?? [];
  return (
    <section aria-labelledby="settings-secrets" className="flex flex-col gap-3">
      <SectionHeading id="settings-secrets" title="Secrets" hint="Named credentials for Hugging Face, NGC, GitHub, S3 and the judge API. Values never enter an agent context." />
      <SecretForm projects={(projects.data?.items ?? []).map((p) => p.slug)} />
      {isLoading ? <p className="text-xs text-muted-foreground">Loading…</p> : null}
      {!isLoading && items.length === 0 ? <p className="text-xs text-muted-foreground">No secrets yet.</p> : null}
      {items.length > 0 ? (
        <Table label="Secrets" head={["Name", "Kind", "Scope", "Added", "By", "Last use"]}>
          {items.map((s) => (
            <tr key={s.id}>
              <Td className="font-mono">{s.name}</Td>
              <Td>{s.kind}</Td>
              <Td className="font-mono">{s.scope}</Td>
              <Td className="tabular-nums">{when(s.createdAt)}</Td>
              <Td>{s.actor.name ?? s.actor.id}</Td>
              <Td className="tabular-nums">{s.lastUsedAt ? when(s.lastUsedAt) : "never"}</Td>
            </tr>
          ))}
        </Table>
      ) : null}
    </section>
  );
}
