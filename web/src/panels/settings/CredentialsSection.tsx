import { useId, useState, type FormEvent } from "react";
import { useQuery, useQueryClient, type QueryClient } from "@tanstack/react-query";
import { credentialsListOptions, projectsListOptions } from "@/api/gen/@tanstack/react-query.gen";
import type { Credential, CredentialCreated, CredentialKind, CredentialList } from "@/api/gen/types.gen";
import { Button } from "@/components/ui/button";
import { cn } from "@/lib/utils";
import { NativeSelect } from "@/components/ui/native-select";
import { Input } from "@/components/ui/input";
import { errorMessage, runCommand, useTopic } from "@/shell/panel";
import { Chip, Field, InlineConfirm, SectionHeading, Table, Td, when } from "./ui";

// Credentials (docs/spec/06-platform.md "Authentication and access"): personal API keys (cdk_) scoped to one project
// or to registry read — the token is shown once, at creation — plus active browser sessions and agent session
// tokens. Revoke is irreversible and confirms inline.

const KIND_LABEL: Record<CredentialKind, string> = { session: "Session", api_key: "API key", agent: "Agent token", agent_host: "Agent host", egress_proxy: "Egress proxy", invitation: "Invitation", worker: "Worker" };

export function isActive(c: Credential, now = Date.now()): boolean {
  return !c.revokedAt && (!c.expiresAt || new Date(c.expiresAt).getTime() > now);
}

/** Folds a changed credential into a cached list: active-only lists drop it once revoked. */
export function mergeCredential(list: CredentialList, c: Credential, includeRevoked: boolean): CredentialList {
  const rest = list.items.filter((x) => x.id !== c.id);
  const items = includeRevoked || isActive(c) ? [c, ...rest] : rest;
  return { ...list, items: items.sort((a, b) => b.createdAt.localeCompare(a.createdAt)) };
}

function patchCredential(qc: QueryClient, c: Credential): void {
  for (const q of qc.getQueryCache().findAll({ predicate: (q) => (q.queryKey[0] as { _id?: string } | undefined)?._id === "credentialsList" })) {
    const old = q.state.data as CredentialList | undefined;
    const query = (q.queryKey[0] as { query?: { revoked?: boolean; kind?: CredentialKind } }).query;
    if (!old || (query?.kind && query.kind !== c.kind)) continue;
    qc.setQueryData(q.queryKey, mergeCredential(old, c, !!query?.revoked));
  }
}

function scopeLabel(c: Credential): string {
  const s = c.scope;
  if (s.all) return "everything";
  const parts = [s.project ? `project ${s.project}` : s.projectId ? `project ${s.projectId}` : undefined, s.registryRead ? "registry read" : undefined, s.preset ? `preset ${s.preset}` : undefined, s.agentSessions ? "agent sessions" : undefined];
  return parts.filter(Boolean).join(" · ") || "—";
}

export function TokenOnce({ created, onDone }: { created: CredentialCreated; onDone: () => void }) {
  const [copied, setCopied] = useState(false);
  const id = useId();
  if (!created.token) return null;
  return (
    <div role="region" aria-labelledby={id} className="flex flex-col gap-2 rounded-md border border-status-warning p-3 text-xs" data-testid="token-once">
      <p id={id} className="font-medium">
        API key “{created.credential.name}” — copy the token now. It is shown only this once; Cadence keeps only its hash.
      </p>
      <div className="flex items-center gap-2">
        <Input readOnly value={created.token} aria-label="API key token" className="h-7 font-mono text-xs" onFocus={(e) => e.currentTarget.select()} />
        <Button
          size="xs"
          variant="outline"
          onClick={async () => {
            try {
              await navigator.clipboard.writeText(created.token!);
              setCopied(true);
            } catch {
              setCopied(false);
            }
          }}
        >
          {copied ? "Copied" : "Copy"}
        </Button>
        <Button size="xs" onClick={onDone}>
          I have stored it
        </Button>
      </div>
      <p className="text-muted-foreground">Use it as `Authorization: Bearer {created.token.slice(0, 8)}…` from scripts and the CLI.</p>
    </div>
  );
}

function NewKeyForm({ projects, onCreated }: { projects: string[]; onCreated: (c: CredentialCreated) => void }) {
  const qc = useQueryClient();
  const uid = useId();
  const [name, setName] = useState("");
  const [project, setProject] = useState("");
  const [registryRead, setRegistryRead] = useState(false);
  const [agentSessions, setAgentSessions] = useState(false);
  const [expires, setExpires] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const noScope = !project && !registryRead;
  const submit = async (e: FormEvent) => {
    e.preventDefault();
    setBusy(true);
    setError(null);
    try {
      const created = await runCommand("credentials.new", {
        body: {
          name,
          scope: { ...(project ? { project } : {}), ...(registryRead ? { registryRead } : {}), ...(project && agentSessions ? { agentSessions } : {}) },
          ...(expires ? { expiresAt: new Date(`${expires}T23:59:59`).toISOString() } : {}),
        },
      });
      patchCredential(qc, created.credential);
      setName("");
      onCreated(created);
    } catch (err) {
      setError(errorMessage(err));
    } finally {
      setBusy(false);
    }
  };
  return (
    <form onSubmit={submit} aria-label="Create API key" className="grid gap-2 rounded-md border p-3 @md:grid-cols-2">
      <Field label="Name" htmlFor={`${uid}-name`}>
        <Input id={`${uid}-name`} value={name} onChange={(e) => setName(e.target.value)} required maxLength={120} placeholder="ci-evals" className="h-7 text-xs" autoComplete="off" />
      </Field>
      <Field label="Project" htmlFor={`${uid}-project`} hint="The one project the key reaches (none: registry read only).">
        <NativeSelect id={`${uid}-project`} value={project} onChange={(e) => setProject(e.target.value)} className="h-7 rounded-md border border-input bg-background px-2 text-xs">
          <option value="">none</option>
          {projects.map((p) => (
            <option key={p} value={p}>
              {p}
            </option>
          ))}
        </NativeSelect>
      </Field>
      <div className="flex flex-col gap-1 text-xs" role="group" aria-label="Permissions">
        <label className="flex min-h-6 items-center gap-1.5">
          <input type="checkbox" className="size-3.5 accent-primary" checked={registryRead} onChange={(e) => setRegistryRead(e.target.checked)} />
          May read the registry
        </label>
        <label className={cn("flex min-h-6 items-center gap-1.5", !project && "text-muted-foreground")} title={project ? undefined : "Pick the project first"}>
          <input type="checkbox" className="size-3.5 accent-primary" checked={!!project && agentSessions} disabled={!project} onChange={(e) => setAgentSessions(e.target.checked)} />
          May run agent sessions in the project
        </label>
        {project && agentSessions ? (
          <span className="text-muted-foreground">Start, message, stop and merge sessions (automation such as the agent evals); the agents use the accounts in Settings → Agents.</span>
        ) : null}
      </div>
      <Field label="Expires (optional)" htmlFor={`${uid}-expires`}>
        <Input id={`${uid}-expires`} type="date" value={expires} onChange={(e) => setExpires(e.target.value)} className="h-7 text-xs" />
      </Field>
      <div className="flex items-center gap-2 @md:col-span-2">
        <Button type="submit" size="xs" disabled={busy || !name || noScope} data-command="credentials.new">
          Create API key
        </Button>
        {noScope ? <span className="text-xs text-muted-foreground">Pick a project, registry read, or both.</span> : null}
        {error ? (
          <span role="alert" className="text-xs text-destructive">
            {error}
          </span>
        ) : null}
      </div>
    </form>
  );
}

export function CredentialsSection() {
  const qc = useQueryClient();
  const [showRevoked, setShowRevoked] = useState(false);
  const [created, setCreated] = useState<CredentialCreated | null>(null);
  const { data, isLoading } = useQuery(credentialsListOptions({ query: { revoked: showRevoked } }));
  const projects = useQuery(projectsListOptions());
  useTopic(["entity.credential.*"], (batch) => {
    for (const e of batch) {
      const c = (e.payload as { credential?: Credential } | undefined)?.credential;
      if (c) patchCredential(qc, c);
    }
  });
  const items = data?.items ?? [];
  const groups: { title: string; kinds: CredentialKind[] }[] = [
    { title: "API keys", kinds: ["api_key"] },
    { title: "Active sessions", kinds: ["session"] },
    { title: "Agent session tokens", kinds: ["agent"] },
    { title: "Other", kinds: ["invitation", "worker"] },
  ];
  return (
    <section aria-labelledby="settings-credentials" className="flex flex-col gap-3">
      <SectionHeading id="settings-credentials" title="Credentials" hint="API keys for scripts and the CLI, your browser sessions and the agents' session tokens.">
        <label className="flex items-center gap-1.5 text-xs text-muted-foreground">
          <input type="checkbox" className="size-3.5 accent-primary" checked={showRevoked} onChange={(e) => setShowRevoked(e.target.checked)} />
          Show revoked and expired
        </label>
      </SectionHeading>
      {created ? <TokenOnce created={created} onDone={() => setCreated(null)} /> : null}
      <NewKeyForm projects={(projects.data?.items ?? []).map((p) => p.slug)} onCreated={setCreated} />
      {isLoading ? <p className="text-xs text-muted-foreground">Loading…</p> : null}
      {groups.map((g) => {
        const rows = items.filter((c) => g.kinds.includes(c.kind));
        if (rows.length === 0) return null;
        return (
          <div key={g.title} className="flex flex-col gap-1">
            <h4 className="text-xs font-medium">{g.title}</h4>
            <Table label={g.title} head={["Name", "Kind", "Scope", "Created", "Last use", "Expires", ""]}>
              {rows.map((c) => (
                <CredentialRow key={c.id} c={c} onRevoked={(r) => patchCredential(qc, r)} />
              ))}
            </Table>
          </div>
        );
      })}
    </section>
  );
}

function CredentialRow({ c, onRevoked }: { c: Credential; onRevoked: (c: Credential) => void }) {
  const [error, setError] = useState<string | null>(null);
  const active = isActive(c);
  return (
    <tr data-testid={`credential-${c.name}`}>
      <Td>
        <span className="font-medium">{c.name}</span> {c.current ? <Chip tone="accent">this browser</Chip> : null}
        {error ? (
          <span role="alert" className="block text-destructive">
            {error}
          </span>
        ) : null}
      </Td>
      <Td>{KIND_LABEL[c.kind]}</Td>
      <Td>{scopeLabel(c)}</Td>
      <Td className="tabular-nums">{when(c.createdAt)}</Td>
      <Td className="tabular-nums">{c.lastUsedAt ? when(c.lastUsedAt) : "never"}</Td>
      <Td className="tabular-nums">{c.revokedAt ? `revoked ${when(c.revokedAt)}` : c.expiresAt ? when(c.expiresAt) : "never"}</Td>
      <Td className="text-right">
        {active ? (
          <InlineConfirm
            label="Revoke"
            confirmLabel={`Revoke ${c.name}`}
            busyLabel="Revoking…"
            disabled={c.current ? "This is your current session — sign out instead" : false}
            onConfirm={async () => {
              setError(null);
              try {
                onRevoked(await runCommand("credentials.revoke", { credential: c }));
              } catch (err) {
                setError(errorMessage(err));
              }
            }}
          />
        ) : null}
      </Td>
    </tr>
  );
}
