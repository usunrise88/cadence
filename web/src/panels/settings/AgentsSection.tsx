import { useId, useState, type FormEvent, type ReactNode } from "react";
import { useQuery, useQueryClient, type QueryClient } from "@tanstack/react-query";
import { agentCredentialsListOptions, agentCredentialsListQueryKey, agentProvidersListOptions } from "@/api/gen/@tanstack/react-query.gen";
import type { AgentCredential, AgentCredentialList, AgentProvider, DefaultValue } from "@/api/gen/types.gen";
import { Button } from "@/components/ui/button";
import { NativeSelect } from "@/components/ui/native-select";
import { Input } from "@/components/ui/input";
import { errorMessage, lookupDefault, problemOf, runCommand, useDefaults, useTopic, WhyDefault } from "@/shell/panel";
import { WriteOnlyField } from "./SecretsSection";
import { Chip, Field, InlineConfirm, SectionHeading, Table, Td, when } from "./ui";

// Agents (docs/spec/08-resolutions.md R3, R6; docs/help/guides/agent-credentials.md): the agents' own model accounts,
// instance-wide and the admin's — the Claude Code subscription token and opencode's providers. Values are write-only:
// the control plane hands them to the agent host, which writes them into the agent-credentials volume; this page
// only ever sees metadata (last four characters, delivery, verification, models). Verify runs a tiny real request
// through the agent on the host.

export const CLAUDE_ID = "claude-code";
export const WARN_DAYS = 30;
const DAY = 24 * 60 * 60 * 1000;
const PROVIDER_ID = /^[a-z][a-z0-9-]{0,39}$/;

export type Expiry = { state: "ok" | "soon" | "expired"; days: number };

/** How close a token is to its expected expiry: "soon" within WARN_DAYS, "expired" past it. */
export function expiryState(expiresAt: string | undefined, now = Date.now()): Expiry | undefined {
  if (!expiresAt) return undefined;
  const days = Math.floor((new Date(expiresAt).getTime() - now) / DAY);
  return { state: days < 0 ? "expired" : days <= WARN_DAYS ? "soon" : "ok", days };
}

/** A credential counts as configured until it is archived. */
export const isLive = (c: AgentCredential | undefined): boolean => !!c && !c.archivedAt;

/** Where the value is: pending and removing wait for the agent host, which may not be connected. */
export function deliveryLabel(c: AgentCredential, hostConnected: boolean): string {
  const d = c.delivery;
  switch (d.state) {
    case "pending":
      return hostConnected ? "Being written by the agent host" : "Waiting for the agent host";
    case "removing":
      return hostConnected ? "Being removed by the agent host" : "Waiting for the agent host to remove it";
    case "written":
      return "In the agent host's credential volume";
    case "removed":
      return "Removed from the agent host";
    case "failed":
      return `The agent host could not write it${d.detail ? `: ${d.detail}` : ""}`;
  }
}

export function verificationLabel(c: AgentCredential, hostConnected: boolean): string {
  const v = c.verification;
  switch (v.state) {
    case "none":
      return "Not verified yet";
    case "pending":
      return hostConnected ? "Verifying…" : "Waiting for the agent host";
    case "ok":
      return `Verified${v.model ? ` with ${v.model}` : ""}`;
    case "failed":
      return `Verification failed${v.detail ? `: ${v.detail}` : ""}`;
  }
}

/** The models a default can be chosen from: the verified models of configured opencode providers. */
export function defaultModelOptions(items: AgentCredential[]): string[] {
  const out = new Set<string>();
  for (const c of items) if (c.agent === "opencode" && isLive(c)) for (const m of c.models) out.add(m);
  return [...out].sort((a, b) => a.localeCompare(b));
}

/** The configured opencode credential a provider/model belongs to (its provider prefixes the model). */
export function credentialForModel(items: AgentCredential[], model: string): AgentCredential | undefined {
  return items.find((c) => c.agent === "opencode" && isLive(c) && model.startsWith(`${c.provider}/`));
}

function invalidate(qc: QueryClient) {
  void qc.invalidateQueries({ queryKey: agentCredentialsListQueryKey() });
}

/** Folds a command's answer into the cached list, so the page moves before the event arrives. */
function patch(qc: QueryClient, c: AgentCredential) {
  qc.setQueryData<AgentCredentialList>(agentCredentialsListQueryKey(), (old) => {
    if (!old) return old;
    const items = old.items.some((x) => x.id === c.id) ? old.items.map((x) => (x.id === c.id ? c : x)) : [...old.items, c];
    return { ...old, items };
  });
  invalidate(qc);
}

function commandError(err: unknown): string {
  const p = problemOf(err);
  if (p?.status === 412) return "It changed meanwhile (another tab or the agent host). The latest state is loaded; try again.";
  return errorMessage(err);
}

function stateTone(state: string): "neutral" | "accent" | "warning" {
  if (state === "ok" || state === "written") return "accent";
  if (state === "failed") return "warning";
  return "neutral";
}

export function AgentsSection() {
  const qc = useQueryClient();
  const list = useQuery(agentCredentialsListOptions());
  const providers = useQuery(agentProvidersListOptions());
  useTopic(["entity.agent_credential.*"], () => invalidate(qc));
  const data = list.data;
  const catalogue = providers.data?.items ?? [];
  const hostConnected = data?.host.connected ?? false;
  return (
    <section aria-labelledby="settings-agents" className="flex flex-col gap-4">
      <SectionHeading
        id="settings-agents"
        title="Agents"
        hint="The model accounts Claude Code and opencode sessions use, for every project. Values are write-only: they go to the agent host's credential volume — never to the database, the API or an agent."
      />
      {list.isLoading ? <p className="text-xs text-muted-foreground">Loading…</p> : null}
      {list.error ? (
        <p role="alert" className="text-xs text-destructive">
          {errorMessage(list.error)}
        </p>
      ) : null}
      {data ? (
        <>
          {!hostConnected ? (
            <p role="status" data-testid="agents-host-banner" className="rounded-md border border-status-warning px-3 py-2 text-xs text-status-warning-foreground">
              Waiting for the agent host — values and checks are delivered when it connects.
            </p>
          ) : null}
          <ClaudeCard credential={data.items.find((c) => c.id === CLAUDE_ID)} provider={catalogue.find((p) => p.agent === "claude-code")} hostConnected={hostConnected} />
          <OpencodeCard list={data} catalogue={catalogue.filter((p) => p.agent === "opencode")} hostConnected={hostConnected} />
        </>
      ) : null}
    </section>
  );
}

function Status({ label, children }: { label: string; children: ReactNode }) {
  return (
    <>
      <dt className="text-muted-foreground">{label}</dt>
      <dd className="min-w-0 break-words">{children}</dd>
    </>
  );
}

function VerificationLine({ c, hostConnected }: { c: AgentCredential; hostConnected: boolean }) {
  return (
    <span className="inline-flex flex-wrap items-center gap-1">
      <Chip tone={stateTone(c.verification.state)}>{c.verification.state === "none" ? "not verified" : c.verification.state}</Chip>
      <span>{verificationLabel(c, hostConnected)}</span>
      {c.verification.at ? <span className="text-muted-foreground tabular-nums">· {when(c.verification.at)}</span> : null}
    </span>
  );
}

function DeliveryLine({ c, hostConnected }: { c: AgentCredential; hostConnected: boolean }) {
  return (
    <span className="inline-flex flex-wrap items-center gap-1">
      <Chip tone={stateTone(c.delivery.state)}>{c.delivery.state}</Chip>
      <span>{deliveryLabel(c, hostConnected)}</span>
    </span>
  );
}

function ClaudeCard({ credential, provider, hostConnected }: { credential: AgentCredential | undefined; provider: AgentProvider | undefined; hostConnected: boolean }) {
  const qc = useQueryClient();
  const uid = useId();
  const [value, setValue] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [saved, setSaved] = useState<string | null>(null);
  const live = credential && isLive(credential) ? credential : undefined;
  const prefix = provider?.keyPrefix ?? "sk-ant-";
  const valueError = value && !value.trim().startsWith(prefix) ? `A Claude token starts with ${prefix}` : undefined;
  const expiry = expiryState(live?.expiresAt);
  const verifyModel: DefaultValue = {
    value: provider?.verifyModel ?? "haiku",
    description: "Verify sends one tiny prompt (“Reply OK”) through Claude Code with this cheap model, run by the agent host as a sandboxed session user through the egress proxy.",
    source: "Cadence recommendation",
  };
  const lifetime: DefaultValue = {
    value: 365,
    unit: "days",
    description: `A token from claude setup-token is valid for about a year. Cadence counts from when it was set here and warns ${WARN_DAYS} days before; run setup-token again and paste the new token.`,
    source: "Claude Code (claude setup-token); the warning period is a Cadence recommendation",
  };

  const run = async (what: () => Promise<AgentCredential>, done: string) => {
    setBusy(true);
    setError(null);
    setSaved(null);
    try {
      patch(qc, await what());
      setSaved(done);
    } catch (err) {
      setError(commandError(err));
      invalidate(qc);
    } finally {
      setBusy(false);
    }
  };
  const submit = (e: FormEvent) => {
    e.preventDefault();
    const v = value.trim();
    setValue(""); // the value leaves the page with the request
    void run(() => runCommand("agentCredentials.set", { id: CLAUDE_ID, credential, body: { value: v } }), "Saved. The agent host writes it into its credential volume; Verify when it is written.");
  };

  return (
    <section aria-labelledby={`${uid}-claude`} className="flex flex-col gap-2 rounded-md border p-3" data-testid="agents-claude">
      <div className="flex flex-wrap items-center gap-2">
        <h4 id={`${uid}-claude`} className="text-xs font-semibold">
          Claude Code
        </h4>
        <Chip tone={live ? "accent" : "neutral"}>{live ? "connected" : "not connected"}</Chip>
        {expiry && expiry.state !== "ok" ? (
          <Chip tone="warning" title={live?.expiresAt ? `Expected expiry ${when(live.expiresAt)}` : undefined}>
            {expiry.state === "expired" ? "token expired" : `expires in ${expiry.days} days`}
          </Chip>
        ) : null}
        <span className="text-xs text-muted-foreground">The owner's Claude subscription, through a long-lived token.</span>
      </div>
      {live ? (
        <dl className="grid grid-cols-[8rem_1fr] gap-x-2 gap-y-1 text-xs">
          <Status label="Token">
            <span className="font-mono">{live.hint ?? "—"}</span>
          </Status>
          <Status label="Connected">
            <span className="tabular-nums">{when(live.setAt)}</span>
            {live.setBy ? <span className="text-muted-foreground"> by {live.setBy.name ?? live.setBy.id}</span> : null}
          </Status>
          <Status label="Expected expiry">
            <span className="inline-flex items-center gap-1">
              <span className={expiry?.state === "expired" ? "text-destructive tabular-nums" : expiry?.state === "soon" ? "text-status-warning-foreground tabular-nums" : "tabular-nums"}>
                {when(live.expiresAt)}
              </span>
              <WhyDefault label="Token lifetime" value={lifetime} />
            </span>
          </Status>
          <Status label="Delivery">
            <DeliveryLine c={live} hostConnected={hostConnected} />
          </Status>
          <Status label="Last check">
            <span className="inline-flex items-center gap-1">
              <VerificationLine c={live} hostConnected={hostConnected} />
              <WhyDefault label="Verify model" value={verifyModel} />
            </span>
          </Status>
        </dl>
      ) : credential?.archivedAt && (credential.delivery.state === "removing" || credential.delivery.state === "failed") ? (
        <p className="text-xs text-muted-foreground">
          Disconnected — <DeliveryLine c={credential} hostConnected={hostConnected} />
        </p>
      ) : null}
      <details className="text-xs" open={!live}>
        <summary className="cursor-pointer py-1 text-muted-foreground">How to get the token</summary>
        <ol className="list-decimal pl-5 text-muted-foreground">
          <li>
            On any machine with a browser and Claude Code installed, run <code className="font-mono">claude setup-token</code> once.
          </li>
          <li>Sign in with the Claude account whose subscription the agents should use.</li>
          <li>
            Paste the token it prints (<code className="font-mono">sk-ant-oat…</code>) below. It is valid for about a year.
          </li>
        </ol>
      </details>
      <form onSubmit={submit} aria-label="Claude Code token" className="flex flex-col gap-2">
        <WriteOnlyField
          id={`${uid}-token`}
          label={live ? "Replace token" : "Token"}
          value={value}
          onChange={setValue}
          action={
            <Button type="submit" size="sm" className="shrink-0" disabled={busy || !value.trim() || !!valueError} data-command="agentCredentials.set">
              {live ? "Replace" : "Connect"}
            </Button>
          }
        />
        {valueError ? (
          <span role="alert" className="text-xs text-destructive">
            {valueError}
          </span>
        ) : null}
      </form>
      <div className="flex flex-wrap items-center gap-2">
        <Button
          size="xs"
          variant="outline"
          disabled={busy || !live?.hasValue}
          data-command="agentCredentials.verify"
          onClick={() => live && void run(() => runCommand("agentCredentials.verify", { credential: live }), "Verification requested.")}
        >
          Verify
        </Button>
        {live ? (
          <InlineConfirm
            label="Disconnect"
            confirmLabel="Disconnect Claude Code"
            busyLabel="Disconnecting…"
            disabled={busy}
            onConfirm={() => run(() => runCommand("agentCredentials.archive", { credential: live }), "Disconnected. The agent host removes the token from its volume.")}
          />
        ) : null}
        {saved ? (
          <span role="status" className="text-xs text-muted-foreground">
            {saved}
          </span>
        ) : null}
        {error ? (
          <span role="alert" className="text-xs text-destructive">
            {error}
          </span>
        ) : null}
      </div>
    </section>
  );
}

function OpencodeCard({ list, catalogue, hostConnected }: { list: AgentCredentialList; catalogue: AgentProvider[]; hostConnected: boolean }) {
  const uid = useId();
  const items = list.items.filter((c) => c.agent === "opencode" && (isLive(c) || c.delivery.state === "removing" || c.delivery.state === "failed"));
  return (
    <section aria-labelledby={`${uid}-opencode`} className="flex flex-col gap-2 rounded-md border p-3" data-testid="agents-opencode">
      <div className="flex flex-wrap items-center gap-2">
        <h4 id={`${uid}-opencode`} className="text-xs font-semibold">
          opencode
        </h4>
        <span className="text-xs text-muted-foreground">Providers and their API keys; several may be configured. Each provider's API hosts join the egress allowlist.</span>
      </div>
      {items.length === 0 ? <p className="text-xs text-muted-foreground">No provider yet. Add MiniMax (the Token Plan) or another below.</p> : null}
      {items.length > 0 ? (
        <Table label="opencode providers" head={["Provider", "Key", "Delivery", "Last check", "Models", "Hosts", ""]}>
          {items.map((c) => (
            <ProviderRow key={c.id} c={c} hostConnected={hostConnected} />
          ))}
        </Table>
      ) : null}
      <DefaultModel list={list} />
      <AddProvider list={list} catalogue={catalogue} />
    </section>
  );
}

function ProviderRow({ c, hostConnected }: { c: AgentCredential; hostConnected: boolean }) {
  const qc = useQueryClient();
  const uid = useId();
  const [replacing, setReplacing] = useState(false);
  const [value, setValue] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const live = isLive(c);
  const run = async (what: () => Promise<AgentCredential>) => {
    setBusy(true);
    setError(null);
    try {
      patch(qc, await what());
      return true;
    } catch (err) {
      setError(commandError(err));
      invalidate(qc);
      return false;
    } finally {
      setBusy(false);
    }
  };
  const replace = async (e: FormEvent) => {
    e.preventDefault();
    const v = value.trim();
    setValue("");
    if (await run(() => runCommand("agentCredentials.set", { id: c.id, credential: c, body: { value: v } }))) setReplacing(false);
  };
  return (
    <>
      <tr data-testid={`agents-provider-${c.provider}`}>
        <Td>
          <span className="font-medium">{c.name}</span> <span className="font-mono text-muted-foreground">{c.provider}</span>
          {c.baseUrl ? <div className="font-mono text-muted-foreground">{c.baseUrl}</div> : null}
          {c.defaultModel ? <Chip tone="accent">default</Chip> : null}
          {!live ? <Chip>removed</Chip> : null}
        </Td>
        <Td className="font-mono">{c.hasValue ? (c.hint ?? "set") : "none"}</Td>
        <Td>
          <DeliveryLine c={c} hostConnected={hostConnected} />
        </Td>
        <Td title={c.verification.detail}>{live ? <VerificationLine c={c} hostConnected={hostConnected} /> : "—"}</Td>
        <Td className="tabular-nums" title={c.models.join("\n")}>
          {c.models.length}
        </Td>
        <Td className="font-mono">{c.hosts.join(" ")}</Td>
        <Td>
          {live ? (
            <span className="inline-flex flex-wrap items-center gap-1">
              <Button size="xs" variant="outline" disabled={busy} data-command="agentCredentials.verify" aria-label={`Verify ${c.name}`} onClick={() => void run(() => runCommand("agentCredentials.verify", { credential: c }))}>
                Verify
              </Button>
              <Button size="xs" variant="ghost" disabled={busy} aria-expanded={replacing} aria-controls={`${uid}-replace`} onClick={() => setReplacing((r) => !r)}>
                Replace key
              </Button>
              <InlineConfirm label="Remove" confirmLabel={`Remove ${c.name}`} busyLabel="Removing…" disabled={busy} onConfirm={() => run(() => runCommand("agentCredentials.archive", { credential: c }))} />
            </span>
          ) : null}
        </Td>
      </tr>
      {replacing || error ? (
        <tr id={`${uid}-replace`}>
          <td colSpan={7} className="px-2 py-1.5">
            {replacing ? (
              <form onSubmit={replace} aria-label={`Replace the key of ${c.name}`}>
                <WriteOnlyField
                  id={`${uid}-key`}
                  label={`New key for ${c.name}`}
                  value={value}
                  onChange={setValue}
                  action={
                    <Button type="submit" size="sm" className="shrink-0" disabled={busy || !value.trim()} data-command="agentCredentials.set">
                      Save key
                    </Button>
                  }
                />
              </form>
            ) : null}
            {error ? (
              <span role="alert" className="text-xs text-destructive">
                {error}
              </span>
            ) : null}
          </td>
        </tr>
      ) : null}
    </>
  );
}

function DefaultModel({ list }: { list: AgentCredentialList }) {
  const qc = useQueryClient();
  const uid = useId();
  const defaults = useDefaults();
  const options = defaultModelOptions(list.items);
  const current = list.opencodeDefault.model;
  const minimax = list.items.some((c) => c.agent === "opencode" && c.provider === "minimax" && isLive(c));
  const preselect = list.opencodeDefault.source === "defaults" && minimax && options.includes("minimax/MiniMax-M3") ? "minimax/MiniMax-M3" : current;
  const [choice, setChoice] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [saved, setSaved] = useState<string | null>(null);
  const selected = choice ?? preselect;
  const all = options.includes(selected) || !selected ? options : [selected, ...options];
  const target = credentialForModel(list.items, selected);
  const fromYaml = lookupDefault(defaults.data, "wizard.opencode_model");
  const why: DefaultValue | undefined =
    list.opencodeDefault.source === "configured"
      ? { value: current, description: "The model new opencode projects and sessions start from, chosen here from a verified provider's models.", source: "Settings → Agents (the admin's choice)" }
      : (fromYaml ?? { value: current, description: "The model new opencode projects start from until one is chosen here.", source: "defaults.yaml wizard.opencode_model" });
  const unchanged = list.opencodeDefault.source === "configured" && selected === current;

  const save = async (e: FormEvent) => {
    e.preventDefault();
    if (!target) return;
    setBusy(true);
    setError(null);
    setSaved(null);
    try {
      patch(qc, await runCommand("agentCredentials.set", { id: target.id, credential: target, body: { defaultModel: selected } }));
      setChoice(null);
      setSaved(`New opencode projects default to ${selected}.`);
    } catch (err) {
      setError(commandError(err));
      invalidate(qc);
    } finally {
      setBusy(false);
    }
  };
  return (
    <form onSubmit={save} aria-label="Default opencode model" className="flex flex-wrap items-end gap-2">
      <Field
        label="Default model for new projects"
        htmlFor={`${uid}-model`}
        extra={<WhyDefault label="Default model for new projects" value={why} />}
        hint={`${list.opencodeDefault.source === "configured" ? "Chosen here" : "From defaults.yaml"}: ${current}. The wizard and the agent profile start from it.`}
      >
        <NativeSelect
          id={`${uid}-model`}
          value={selected}
          disabled={all.length === 0}
          onChange={(e) => setChoice(e.target.value)}
          className="h-7 min-w-64 rounded-md border border-input bg-background px-2 font-mono text-xs"
        >
          {all.length === 0 ? <option value="">Verify a provider to list its models</option> : null}
          {all.map((m) => (
            <option key={m} value={m}>
              {m}
            </option>
          ))}
        </NativeSelect>
      </Field>
      <Button type="submit" size="xs" disabled={busy || !target || unchanged} data-command="agentCredentials.set" title={!target ? "Configure and verify the model's provider first" : undefined}>
        Use as default
      </Button>
      {saved ? (
        <span role="status" className="text-xs text-muted-foreground">
          {saved}
        </span>
      ) : null}
      {error ? (
        <span role="alert" className="text-xs text-destructive">
          {error}
        </span>
      ) : null}
    </form>
  );
}

function AddProvider({ list, catalogue }: { list: AgentCredentialList; catalogue: AgentProvider[] }) {
  const qc = useQueryClient();
  const uid = useId();
  const configured = new Set(list.items.filter(isLive).map((c) => c.id));
  const choices = catalogue.filter((p) => p.custom || !configured.has(`opencode.${p.id}`));
  const [pick, setPick] = useState("");
  const [value, setValue] = useState("");
  const [pid, setPid] = useState("");
  const [name, setName] = useState("");
  const [baseUrl, setBaseUrl] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [saved, setSaved] = useState<string | null>(null);
  const entry = choices.find((p) => p.id === pick) ?? choices[0];
  if (!entry) return null;
  const custom = entry.custom;
  const providerId = custom ? pid : entry.id;
  const id = `opencode.${providerId}`;
  const pidError = custom && pid && !PROVIDER_ID.test(pid) ? "Lowercase letters, digits and dashes; starts with a letter" : custom && configured.has(id) ? `opencode.${pid} is configured already` : undefined;
  const urlError = custom && baseUrl && !/^https?:\/\/[^/\s]+/.test(baseUrl) ? "An http:// or https:// URL, e.g. http://vllm.lan:8000/v1" : undefined;
  const keyError = value && entry.keyPrefix && !value.trim().startsWith(entry.keyPrefix) ? `A ${entry.name} key starts with ${entry.keyPrefix}` : undefined;
  const ready = !!providerId && !pidError && !keyError && !urlError && (entry.keyRequired ? !!value.trim() : true) && (!custom || !!baseUrl);

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    const v = value.trim();
    setValue("");
    setBusy(true);
    setError(null);
    setSaved(null);
    try {
      const existing = list.items.find((c) => c.id === id);
      const c = await runCommand("agentCredentials.set", {
        id,
        credential: existing,
        body: { ...(v ? { value: v } : {}), ...(custom ? { catalogueId: entry.id, baseUrl, name: name || pid } : {}) },
      });
      patch(qc, c);
      setSaved(`Added ${c.name}. Verify it once the agent host has written it, to list its models.`);
      setPid("");
      setName("");
      setBaseUrl("");
      setPick("");
    } catch (err) {
      setError(commandError(err));
    } finally {
      setBusy(false);
    }
  };
  return (
    <form onSubmit={submit} aria-label="Add opencode provider" className="grid gap-2 rounded-md border border-dashed p-3 @md:grid-cols-2">
      <Field label="Provider" htmlFor={`${uid}-provider`} hint={entry.description}>
        <NativeSelect id={`${uid}-provider`} value={entry.id} onChange={(e) => setPick(e.target.value)} className="text-xs">
          {choices.map((p) => (
            <option key={p.id} value={p.id}>
              {p.name}
            </option>
          ))}
        </NativeSelect>
      </Field>
      <WriteOnlyField id={`${uid}-key`} label={`${entry.keyLabel}${entry.keyRequired ? "" : " (optional)"}`} value={value} onChange={setValue} required={entry.keyRequired} />
      {custom ? (
        <>
          <Field label="Provider id" htmlFor={`${uid}-pid`} error={pidError} hint="opencode's name for it: models are <id>/<model>.">
            <Input id={`${uid}-pid`} value={pid} onChange={(e) => setPid(e.target.value)} placeholder="vllm" className="h-7 font-mono text-xs" autoComplete="off" />
          </Field>
          <Field label="Display name" htmlFor={`${uid}-name`}>
            <Input id={`${uid}-name`} value={name} onChange={(e) => setName(e.target.value)} placeholder="Self-hosted vLLM" className="h-7 text-xs" autoComplete="off" maxLength={80} />
          </Field>
          <Field label="Base URL" htmlFor={`${uid}-url`} error={urlError} hint="OpenAI-compatible API root; its host joins the egress allowlist.">
            <Input id={`${uid}-url`} value={baseUrl} onChange={(e) => setBaseUrl(e.target.value)} placeholder="http://vllm.lan:8000/v1" className="h-7 font-mono text-xs" autoComplete="off" />
          </Field>
        </>
      ) : null}
      {entry.help ? <p className="text-xs text-muted-foreground @md:col-span-2">{entry.help}</p> : null}
      {!custom && entry.hosts.length ? (
        <p className="text-xs text-muted-foreground @md:col-span-2">
          Adds <span className="font-mono">{entry.hosts.join(", ")}</span> to the egress allowlist.
        </p>
      ) : null}
      {keyError ? (
        <span role="alert" className="text-xs text-destructive @md:col-span-2">
          {keyError}
        </span>
      ) : null}
      <div className="flex flex-wrap items-center gap-2 @md:col-span-2">
        <Button type="submit" size="xs" disabled={busy || !ready} data-command="agentCredentials.set">
          Add provider
        </Button>
        {saved ? (
          <span role="status" className="text-xs text-muted-foreground">
            {saved}
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
