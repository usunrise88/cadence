import type { AnchorHTMLAttributes } from "react";
import { Streamdown } from "streamdown";
import type { ModelVersion } from "@/api/gen/types.gen";
import { Button } from "@/components/ui/button";
import { cn } from "@/lib/utils";
import { ActorBadge, EmptyState } from "@/shell/entity/primitives";
import { GATE_CLASS, GATE_GLYPH, openDocument, openPanelById, type PanelProps } from "@/shell/panel";

// The Model document (docs/spec/11-ui-panels.md "Panel catalogue", Model; R22): a registered model version — the
// checkpoint it publishes, the gate verdict and the eval it was registered with, its lineage (run, mix, recipe,
// dataset versions), the projects that use it, and the model card. The card is Markdown written by the control plane
// from the eval; it renders without raw HTML, and links open in a new tab. Export, promote and roll back arrive with
// deployment (phase 5).

export function ModelEmpty() {
  return <EmptyState step="record" title="No model open" hint="Register a checkpoint from a passed Eval report, or open a model from the Library." />;
}

export function ModelPanel({ tab, entity }: PanelProps) {
  const m = entity?.model as ModelVersion | undefined;
  if (!entity || !m) return <ModelEmpty />;
  switch (tab) {
    case "details":
      return <Details m={m} />;
    case "lineage":
      return <LineageTab m={m} />;
    case "activity":
    case "notes":
      return <EmptyState step="record" title="Registry versions are immutable" hint="A model changes only by registering a new version." />;
    default:
      return <Overview m={m} />;
  }
}

function Row({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div className="contents">
      <dt className="text-muted-foreground">{label}</dt>
      <dd className="min-w-0 break-words">{children}</dd>
    </div>
  );
}

const link = "underline-offset-2 hover:underline";

function CardLink({ href, children, ...rest }: AnchorHTMLAttributes<HTMLAnchorElement> & { node?: unknown }) {
  const { node: _node, ...props } = rest as typeof rest & { node?: unknown };
  return (
    <a href={href} target="_blank" rel="noopener noreferrer" {...props}>
      {children}
    </a>
  );
}

const CARD_COMPONENTS = { a: CardLink };

function Overview({ m }: { m: ModelVersion }) {
  const p = m.model;
  return (
    <div className="flex flex-col gap-5 p-4 text-xs" data-model={m.id}>
      <section aria-labelledby={`model-gate-${m.id}`} className="flex flex-col gap-1.5">
        <h3 id={`model-gate-${m.id}`} className="text-[11px] font-medium tracking-wide text-muted-foreground uppercase">
          Gate and eval
        </h3>
        <p className="flex flex-wrap items-center gap-2" data-slot="model-gate" data-verdict={p.gate.verdict}>
          <span className={cn("text-[13px] font-medium", GATE_CLASS[p.gate.verdict])}>
            <span aria-hidden>{GATE_GLYPH[p.gate.verdict]} </span>
            Gate {p.gate.verdict}
          </span>
          <span className="text-muted-foreground">gates.yaml {p.gate.gatesSha ? <code className="text-[11px]">{p.gate.gatesSha.slice(0, 7)}</code> : "defaults"}</span>
          <Button size="xs" variant="outline" onClick={() => openDocument(`eval:${p.evalId}`)}>
            Open the eval report
          </Button>
        </p>
      </section>
      <section aria-labelledby={`model-what-${m.id}`} className="flex flex-col gap-1.5">
        <h3 id={`model-what-${m.id}`} className="text-[11px] font-medium tracking-wide text-muted-foreground uppercase">
          What it publishes
        </h3>
        <dl className="grid grid-cols-[9rem_1fr] gap-x-3 gap-y-1">
          <Row label="Checkpoint">
            <code className="text-[11px]">{p.checkpointId}</code>
          </Row>
          <Row label="Weights">
            <code className="text-[11px]">{p.weightsHash}</code>
          </Row>
          <Row label="Checkpoint artifact">
            <code className="text-[11px]">{p.checkpointHash}</code>
          </Row>
          <Row label="Family">{p.familyId}</Row>
          <Row label="Base model">
            <code className="text-[11px]">{p.baseModelVersionId}</code>
          </Row>
          <Row label="Run">
            {p.lineage.runId ? (
              <button type="button" className={link} onClick={() => openDocument(`run:${p.lineage.runId}`)}>
                {p.lineage.runId}
              </button>
            ) : (
              "—"
            )}
          </Row>
        </dl>
      </section>
      <section aria-labelledby={`model-used-${m.id}`} className="flex flex-col gap-1.5">
        <h3 id={`model-used-${m.id}`} className="text-[11px] font-medium tracking-wide text-muted-foreground uppercase">
          Used by
        </h3>
        {m.usedBy.length ? (
          <ul className="flex flex-col gap-0.5">
            {m.usedBy.map((u) => (
              <li key={u.projectId}>
                <button type="button" className={link} onClick={() => openDocument(`project:${u.projectSlug}`)}>
                  {u.projectSlug}
                </button>
                {u.aliases.length ? <span className="text-muted-foreground"> · @{u.aliases.join(", @")}</span> : null}
              </li>
            ))}
          </ul>
        ) : (
          <p className="text-muted-foreground">No project uses it.</p>
        )}
      </section>
      <section aria-labelledby={`model-card-${m.id}`} className="flex flex-col gap-1.5">
        <h3 id={`model-card-${m.id}`} className="text-[11px] font-medium tracking-wide text-muted-foreground uppercase">
          Model card
        </h3>
        {p.card ? (
          <div className="cadence-prose rounded-md border bg-background p-3 text-[13px]" data-slot="model-card">
            <Streamdown mode="static" skipHtml controls={false} components={CARD_COMPONENTS}>
              {p.card}
            </Streamdown>
          </div>
        ) : (
          <p className="text-muted-foreground">No model card.</p>
        )}
      </section>
    </div>
  );
}

function LineageTab({ m }: { m: ModelVersion }) {
  const l = m.model.lineage;
  return (
    <div className="flex flex-col gap-3 p-4 text-xs">
      <dl className="grid grid-cols-[9rem_1fr] gap-x-3 gap-y-1">
        <Row label="Run">
          {l.runId ? (
            <button type="button" className={link} onClick={() => openDocument(`run:${l.runId}`)}>
              {l.runId}
            </button>
          ) : (
            "—"
          )}
        </Row>
        <Row label="Mix">{l.mixSha ? <code className="text-[11px]">{l.mixSha}</code> : "—"}</Row>
        <Row label="Recipe commit">{l.recipeSha ? <code className="text-[11px]">{l.recipeSha.slice(0, 12)}</code> : "—"}</Row>
        <Row label="Dataset versions">
          {l.datasetVersionIds?.length ? (
            <ul>
              {l.datasetVersionIds.map((d) => (
                <li key={d}>
                  <code className="text-[11px]">{d}</code>
                </li>
              ))}
            </ul>
          ) : (
            "—"
          )}
        </Row>
      </dl>
      <Button size="xs" variant="outline" className="w-fit" onClick={() => openPanelById("lineage")}>
        Open Lineage
      </Button>
    </div>
  );
}

function Details({ m }: { m: ModelVersion }) {
  return (
    <dl className="grid grid-cols-[10rem_1fr] gap-x-4 gap-y-2 p-4 text-xs">
      <Row label="ID">
        <code className="font-mono text-[11px]">{m.id}</code>
      </Row>
      <Row label="Collection">
        {m.name} <span className="text-muted-foreground">({m.collectionId})</span>
      </Row>
      <Row label="Version">{m.version}</Row>
      <Row label="State">{m.state}</Row>
      <Row label="Project">{m.model.projectId}</Row>
      <Row label="Tags">{m.tags.join(", ") || "—"}</Row>
      <Row label="Licence">{m.licence || "—"}</Row>
      <Row label="Fingerprint">
        <code className="text-[11px]">{m.fingerprint}</code>
      </Row>
      <Row label="Registered by">
        <ActorBadge actor={m.actor} /> {new Date(m.createdAt).toLocaleString()}
      </Row>
    </dl>
  );
}
