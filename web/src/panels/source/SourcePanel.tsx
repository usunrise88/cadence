import { useState } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { sourcesGetQueryKey } from "@/api/gen/@tanstack/react-query.gen";
import type { Source } from "@/api/gen/types.gen";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { CLEAR_REQUEST, UtteranceSearch } from "@/shell/data";
import { ActorBadge, EmptyState } from "@/shell/entity/primitives";
import { errorMessage, openDocument, openPanelById, runCommand, useEditRequest, useProject, type PanelProps } from "@/shell/panel";

// The Source document (docs/spec/11-ui-panels.md "Panel catalogue", Source; spec 02 "Data entities"; R18, R26): a
// corpus's licence, kind and languages, whether it is cleared for training (a person's decision), its clearing history
// (sources.get clearances) and ingest history (ingests: the dataset versions imports and ingests registered from it),
// and its utterances. Ingesting runs the project's pipelines/data-ingest.yaml with this source named.

export function SourceEmpty() {
  return <EmptyState step="prepare" title="No source open" hint="Open a source from the Library or a dataset version." />;
}

export function SourcePanel({ tab, entity, doc }: PanelProps) {
  const s = entity?.source as Source | undefined;
  if (!entity || !s) return <SourceEmpty />;
  switch (tab) {
    case "details":
      return <Details s={s} />;
    case "lineage":
      return (
        <div className="flex flex-col gap-2 p-4 text-xs">
          <p>The dataset versions built from this source, and what was built from them, are drawn by the Lineage panel.</p>
          <Button size="xs" variant="outline" className="w-fit" onClick={() => openPanelById("lineage")}>
            Open Lineage
          </Button>
        </div>
      );
    case "activity":
      return <History s={s} />;
    case "notes":
      return <EmptyState step="record" title="No notes on sources" hint="The description and the clearing history are the source's record." />;
    default:
      return <Overview s={s} doc={doc} />;
  }
}

function Section({ id, title, children, slot }: { id: string; title: string; children: React.ReactNode; slot?: string }) {
  return (
    <section aria-labelledby={id} className="flex min-w-0 flex-col gap-1.5" data-slot={slot}>
      <h3 id={id} className="text-[11px] font-medium tracking-wide text-muted-foreground uppercase">
        {title}
      </h3>
      {children}
    </section>
  );
}

function Row({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div className="contents">
      <dt className="text-muted-foreground">{label}</dt>
      <dd className="min-w-0 break-words">{children}</dd>
    </div>
  );
}

const CHANGE: Record<string, string> = { created: "registered", licence: "licence changed", cleared: "cleared for training", uncleared: "made eval-only" };

function Overview({ s, doc }: { s: Source; doc?: string }) {
  const project = useProject();
  const [clearOpen, setClearOpen] = useState(false);
  useEditRequest(doc ? `${CLEAR_REQUEST}${doc}` : undefined, () => setClearOpen(true));
  return (
    <div className="flex flex-col gap-5 p-4 text-xs @container" data-source={s.id}>
      <Section id={`src-licence-${s.id}`} title="Licence and clearance" slot="source-licence">
        <dl className="grid grid-cols-[9rem_1fr] gap-x-3 gap-y-1">
          <Row label="Licence">{s.licence}</Row>
          <Row label="Kind">{s.kind}</Row>
          <Row label="Languages">{s.languages.join(", ") || "—"}</Row>
          <Row label="URL">{s.url || "—"}</Row>
          <Row label="Training">
            {s.trainingCleared ? (
              <span>
                <span aria-hidden>✓ </span>cleared{s.clearedBy ? " by " : ""}
                {s.clearedBy ? <ActorBadge actor={s.clearedBy} /> : null}
                {s.clearedAt ? ` ${new Date(s.clearedAt).toLocaleDateString()}` : ""}
              </span>
            ) : (
              <span>eval-only: its dataset versions are evaluated on, never mixed or trained on, until a person clears it</span>
            )}
          </Row>
          {s.archived ? <Row label="Archived">yes — it takes no new ingests</Row> : null}
          {s.description ? <Row label="Description">{s.description}</Row> : null}
        </dl>
        {!s.archived && !clearOpen ? (
          <Button size="xs" variant="outline" className="w-fit" onClick={() => setClearOpen(true)} data-command="sources.edit">
            {s.trainingCleared ? "Change licence or clearance…" : "Clear for training…"}
          </Button>
        ) : null}
        {clearOpen ? <ClearCard s={s} onClose={() => setClearOpen(false)} /> : null}
      </Section>

      <Section id={`src-ingests-${s.id}`} title="Ingest history" slot="source-ingests">
        {s.ingests?.length ? (
          <table className="w-full text-xs" aria-label="Ingests">
            <thead className="text-left text-muted-foreground">
              <tr>
                <th className="font-normal">When</th>
                <th className="font-normal">Dataset version</th>
                <th className="font-normal">Step</th>
                <th className="font-normal">Utterances</th>
                <th className="font-normal">Hours</th>
                <th className="font-normal">State</th>
              </tr>
            </thead>
            <tbody>
              {s.ingests.map((i) => (
                <tr key={i.datasetVersionId} className="h-6 border-t">
                  <td className="text-muted-foreground">{new Date(i.at).toLocaleString()}</td>
                  <td>
                    <button type="button" className="underline-offset-2 hover:underline" onClick={() => openDocument(`dataset_version:${i.datasetVersionId}`)}>
                      <code className="text-[11px]">{i.datasetVersionId}</code>
                    </button>
                  </td>
                  <td>{i.stepKind ?? "—"}</td>
                  <td className="tabular-nums">{i.utterances.toLocaleString()}</td>
                  <td className="tabular-nums">{i.hours.toFixed(2)}</td>
                  <td>{i.frozen === false ? "draft" : "frozen"}</td>
                </tr>
              ))}
            </tbody>
          </table>
        ) : (
          <p className="text-muted-foreground">Nothing ingested from it yet.</p>
        )}
        {!s.archived ? (
          <p className="text-muted-foreground">
            To ingest it, set <code>source: {s.name}</code> in the project's pipelines/data-ingest.yaml and run the pipeline (no licence, no ingest).{" "}
            {project ? (
              <button type="button" className="underline underline-offset-2" onClick={() => openDocument("recipe:pipelines/data-ingest.yaml")}>
                Open data-ingest
              </button>
            ) : null}
          </p>
        ) : null}
      </Section>

      <Section id={`src-utt-${s.id}`} title="Utterances" slot="source-utterances">
        <p className="text-muted-foreground">
          {s.utterances.toLocaleString()} utterances · {s.hours.toFixed(2)} h
        </p>
        <UtteranceSearch source={s.id} />
      </Section>
    </div>
  );
}

/**
 * sources.edit: the licence, and clearing for training — a person's decision (R18); an agent's call waits for an
 * approval, a person's applies at once. Unclearing makes the source eval-only again.
 */
function ClearCard({ s, onClose }: { s: Source; onClose: () => void }) {
  const qc = useQueryClient();
  const [licence, setLicence] = useState(s.licence);
  const [busy, setBusy] = useState(false);
  const [message, setMessage] = useState<{ error: boolean; text: string } | null>(null);
  const act = async (body: { licence?: string; trainingCleared?: boolean }) => {
    setBusy(true);
    setMessage(null);
    try {
      const res = await runCommand("sources.edit", { source: s, body });
      if (res && "approvalId" in res) setMessage({ error: false, text: `Waits for an approval (${res.approvalId}).` });
      else {
        setMessage({ error: false, text: "Saved." });
        void qc.invalidateQueries({ queryKey: sourcesGetQueryKey({ path: { id: s.id } }) });
      }
    } catch (err) {
      setMessage({ error: true, text: errorMessage(err) });
    } finally {
      setBusy(false);
    }
  };
  return (
    <div className="flex flex-col gap-2 rounded-md border bg-tool p-2" role="group" aria-label="Licence and clearance" data-slot="clear-card">
      <p>
        Clearing <span className="font-medium">{s.name}</span> for training says its licence allows training a model you ship. Check the corpus's own licence first (its card, or
        SOURCE.yaml on the mount); its dataset versions can then be mixed.
      </p>
      <div className="flex items-center gap-1.5">
        <label htmlFor={`src-lic-${s.id}`} className="text-muted-foreground">
          Licence
        </label>
        <Input id={`src-lic-${s.id}`} className="h-6 w-48 text-xs" value={licence} onChange={(e) => setLicence(e.target.value)} />
        <Button size="xs" variant="outline" disabled={busy || !licence.trim() || licence.trim() === s.licence} onClick={() => void act({ licence: licence.trim() })}>
          Save licence
        </Button>
      </div>
      <div className="flex gap-1">
        {s.trainingCleared ? (
          <Button size="xs" variant="outline" disabled={busy} onClick={() => void act({ trainingCleared: false })} data-command="sources.edit">
            Make eval-only
          </Button>
        ) : (
          <Button size="xs" disabled={busy} onClick={() => void act({ trainingCleared: true })} data-command="sources.edit">
            Clear for training
          </Button>
        )}
        <Button size="xs" variant="ghost" onClick={onClose}>
          Close
        </Button>
      </div>
      {message ? (
        <p role={message.error ? "alert" : "status"} className={message.error ? "text-destructive" : "text-muted-foreground"}>
          {message.text}
        </p>
      ) : null}
    </div>
  );
}

function History({ s }: { s: Source }) {
  const list = s.clearances ?? [];
  return (
    <div className="p-4 text-xs" data-slot="source-clearances">
      {list.length ? (
        <table className="w-full" aria-label="Clearing history">
          <thead className="text-left text-muted-foreground">
            <tr>
              <th className="font-normal">When</th>
              <th className="font-normal">Change</th>
              <th className="font-normal">Licence</th>
              <th className="font-normal">Training</th>
              <th className="font-normal">By</th>
            </tr>
          </thead>
          <tbody>
            {list.map((c, i) => (
              <tr key={i} className="h-7 border-t">
                <td className="text-muted-foreground">{new Date(c.at).toLocaleString()}</td>
                <td>{c.change ? (CHANGE[c.change] ?? c.change) : "—"}</td>
                <td>{c.licence}</td>
                <td>{c.trainingCleared ? "✓ cleared" : "eval-only"}</td>
                <td>
                  <ActorBadge actor={c.actor} />
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      ) : (
        <p className="text-muted-foreground">No history recorded.</p>
      )}
    </div>
  );
}

function Details({ s }: { s: Source }) {
  return (
    <dl className="grid grid-cols-[10rem_1fr] gap-x-4 gap-y-2 p-4 text-xs">
      <Row label="ID">
        <code className="font-mono text-[11px]">{s.id}</code>
      </Row>
      <Row label="Name">{s.name}</Row>
      <Row label="Revision">{s.rev}</Row>
      <Row label="Dataset versions">
        <span className="flex flex-wrap gap-1">
          {s.datasets.length
            ? s.datasets.map((d) => (
                <button key={d} type="button" className="underline-offset-2 hover:underline" onClick={() => openDocument(`dataset_version:${d}`)}>
                  <code className="text-[11px]">{d}</code>
                </button>
              ))
            : "—"}
        </span>
      </Row>
      <Row label="Registered">
        <ActorBadge actor={s.createdBy} /> {new Date(s.createdAt).toLocaleString()}
      </Row>
      <Row label="Updated">{new Date(s.updatedAt).toLocaleString()}</Row>
    </dl>
  );
}
