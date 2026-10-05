import { useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { deploymentTargetsListOptions, deploymentTargetsListQueryKey } from "@/api/gen/@tanstack/react-query.gen";
import type { DeploymentTarget, SigningKey } from "@/api/gen/types.gen";
import { Button } from "@/components/ui/button";
import { cn } from "@/lib/utils";
import { StatusChip } from "@/shell/entity/primitives";
import { errorMessage, useTopic } from "@/shell/panel";
import { SectionHeading, Table, Td } from "./ui";

// Deployment targets (docs/spec/02-domain-projects-registry.md "Deployment targets", R46; help panels/deployment-targets):
// where models are served. Staging targets are the server Cadence reaches — its health from the control plane's
// check and the models the serve steps loaded; delivery targets are production servers only a person's delivery
// script reaches — what they serve, their slots and the head of their signed promotion chain. The instance's public
// signing key is the file a person installs once on a production host as /etc/cadence/instance.pub. Creating or
// changing a target is an approval the admin decides (deploymentTargets.new and edit, from the CLI or an agent).

function when(iso?: string): string {
  return iso ? new Date(iso).toLocaleString() : "—";
}

function serves(t: DeploymentTarget): string {
  if (t.serves.length === 0) return "nothing yet";
  return t.serves.map((s) => `${s.family} (${s.formats.join(", ")}; ${s.profiles.join(", ")})`).join("; ");
}

export function DeploymentTargetsSection() {
  const qc = useQueryClient();
  const q = useQuery(deploymentTargetsListOptions({ query: { state: "all" } }));
  useTopic(["entity.deployment_target.*"], () => void qc.invalidateQueries({ queryKey: deploymentTargetsListQueryKey({ query: { state: "all" } }) }));
  const items = q.data?.items ?? [];
  const staging = items.filter((t) => t.kind === "staging");
  const delivery = items.filter((t) => t.kind === "delivery");
  return (
    <section aria-labelledby="settings-targets" className="flex flex-col gap-4">
      <SectionHeading
        id="settings-targets"
        title="Deployment targets"
        hint="Staging targets are the servers Cadence reaches for parity, benchmarks and shadow replay; delivery targets are production servers only a person's delivery script reaches. Creating or changing a target is an approval the admin decides (deploymentTargets.new, deploymentTargets.edit)."
      >
        <Button size="xs" variant="outline" disabled={q.isFetching} onClick={() => void q.refetch()}>
          Refresh
        </Button>
      </SectionHeading>
      {q.isLoading ? <p className="text-xs text-muted-foreground">Loading…</p> : null}
      {q.error ? (
        <p role="alert" className="text-xs text-destructive">
          {errorMessage(q.error)}
        </p>
      ) : null}
      {q.data ? (
        <>
          <div className="flex flex-col gap-2">
            <h4 className="text-xs font-medium">Staging</h4>
            {staging.length === 0 ? (
              <p className="text-xs text-muted-foreground">No staging target (defaults.yaml serving.staging_target seeds one at first start).</p>
            ) : (
              staging.map((t) => <StagingTarget key={t.id} t={t} />)
            )}
          </div>
          <div className="flex flex-col gap-2">
            <h4 className="text-xs font-medium">Delivery</h4>
            {delivery.length === 0 ? (
              <p className="text-xs text-muted-foreground">No delivery target yet. The admin creates one (deploymentTargets.new): it names the production server, its repository path and slots.</p>
            ) : (
              <Table label="Delivery targets" head={["Name", "Serves", "Server", "Slots", "Repository path", "Concurrency", "Card class", "Chain", "State"]}>
                {delivery.map((t) => (
                  <tr key={t.id} data-testid={`target-${t.name}`}>
                    <Td className="font-medium">{t.name}</Td>
                    <Td>{serves(t)}</Td>
                    <Td>
                      {t.server.kind} {t.server.version}
                    </Td>
                    <Td>{t.slots.join(", ") || "—"}</Td>
                    <Td className="font-mono">{t.repositoryPath ?? "—"}</Td>
                    <Td className="tabular-nums">{t.concurrency ?? "—"}</Td>
                    <Td>{t.cardClass ?? "—"}</Td>
                    <Td className="tabular-nums" title={t.chain?.headHash}>
                      {t.chain ? `${t.chain.records} records${t.chain.pending ? `, ${t.chain.pending} pending` : ""}` : "—"}
                    </Td>
                    <Td>
                      <StatusChip state={t.state} />
                    </Td>
                  </tr>
                ))}
              </Table>
            )}
          </div>
          <SigningKeys keys={q.data.signingKeys} />
        </>
      ) : null}
    </section>
  );
}

function StagingTarget({ t }: { t: DeploymentTarget }) {
  const h = t.health;
  const models = t.servedModels ?? [];
  return (
    <div className="flex flex-col gap-2 rounded-md border p-3 text-xs" data-testid={`target-${t.name}`}>
      <div className="flex flex-wrap items-center gap-2">
        <span className="text-[13px] font-medium">{t.name}</span>
        <span className="font-mono text-muted-foreground">{t.endpoint}</span>
        <span className="text-muted-foreground">
          {t.server.kind} {t.server.version}
        </span>
        {t.state !== "active" ? <StatusChip state={t.state} /> : null}
        <span className="ml-auto flex items-center gap-1" data-testid="target-health">
          <span className="text-muted-foreground">Server</span>
          <span className={cn("font-medium", h?.state === "down" && "text-status-failed-foreground", h?.state === "up" && "text-status-done-foreground")}>{h?.state ?? "unknown"}</span>
          {h?.since ? <span className="text-muted-foreground">since {when(h.since)}</span> : null}
          {h?.latencyMs !== undefined ? <span className="text-muted-foreground tabular-nums">· {h.latencyMs} ms</span> : null}
        </span>
      </div>
      {h?.detail ? <p className="text-status-failed-foreground">{h.detail}</p> : null}
      <p className="text-muted-foreground">Serves {serves(t)}</p>
      {models.length === 0 ? (
        <p className="text-muted-foreground">No model loaded.</p>
      ) : (
        <Table label={`Models served by ${t.name}`} head={["Model", "Deployable", "Memory", "State", "Leases", "Last used"]}>
          {models.map((m) => (
            <tr key={m.model}>
              <Td className="font-mono">{m.model}</Td>
              <Td className="font-mono" title={m.deployableHash}>
                {m.deployableHash.slice(0, 15)}…
              </Td>
              <Td className="tabular-nums">{(m.memoryMb / 1024).toFixed(1)} GB</Td>
              <Td>{m.state}</Td>
              <Td className="tabular-nums">{m.leases}</Td>
              <Td>{when(m.lastUsedAt)}</Td>
            </tr>
          ))}
        </Table>
      )}
    </div>
  );
}

function SigningKeys({ keys }: { keys: SigningKey[] }) {
  const [copied, setCopied] = useState<string | null>(null);
  const copy = async (el: HTMLElement, k: SigningKey) => {
    const win = el.ownerDocument.defaultView ?? window;
    try {
      await win.navigator.clipboard.writeText(k.publicKeyPem);
      setCopied(k.id);
    } catch {
      setCopied(null);
    }
  };
  return (
    <div className="flex flex-col gap-2">
      <h4 className="text-xs font-medium">Instance signing key</h4>
      <p className="text-xs text-muted-foreground">
        Every promotion record is signed with it. Install the current public key once on each production host as /etc/cadence/instance.pub; the delivery
        script refuses records signed by another key.
      </p>
      {keys.length === 0 ? (
        <p className="text-xs text-muted-foreground">No key yet: it is created with the first delivery target.</p>
      ) : (
        keys.map((k) => (
          <div key={k.id} className="flex flex-col gap-1 rounded-md border p-2 text-xs" data-testid="signing-key">
            <div className="flex items-center gap-2">
              <span className="font-mono">{k.id}</span>
              <StatusChip state={k.state} />
              <span className="text-muted-foreground">since {when(k.createdAt)}</span>
              <Button size="xs" variant="outline" className="ml-auto" onClick={(e) => void copy(e.currentTarget, k)}>
                {copied === k.id ? "Copied" : "Copy public key"}
              </Button>
            </div>
            <pre className="overflow-auto rounded bg-tool p-2 font-mono text-[11px]">{k.publicKeyPem}</pre>
          </div>
        ))
      )}
    </div>
  );
}
