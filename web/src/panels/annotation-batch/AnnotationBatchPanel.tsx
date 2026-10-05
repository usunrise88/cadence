import { useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Copy, Lock, Plus, UserPlus } from "iconoir-react";
import { batchItemsListOptions, batchesGetQueryKey, eventsListOptions, invitationsListOptions } from "@/api/gen/@tanstack/react-query.gen";
import type { Batch, BatchItem, BatchNew, InvitationCreated } from "@/api/gen/types.gen";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { NativeSelect } from "@/components/ui/native-select";
import { Textarea } from "@/components/ui/textarea";
import { cn } from "@/lib/utils";
import { AudioView } from "@/shell/audio";
import { EmptyState, StatusChip } from "@/shell/entity/primitives";
import { channelLabel, errorMessage, GuidelinesPane, INVITE_REQUEST, openDocument, runCommand, seconds, useEditRequest, useProject, type PanelProps } from "@/shell/panel";

// The Annotation batch document (docs/spec/11-ui-panels.md "Panel catalogue", Annotation batch; docs/spec/04-blocks.md
// "Annotation workflow"): the sample and its strata, progress per state, the inter-annotator WER against its target,
// the end-of-utterance gaps, the guidelines commit, the reviewers (invite one: a link that opens this batch only), the
// adjudication queue (the two transcripts side by side; take one, write the final text, or exclude the item) and the
// freeze (an approval; then the cut and the golden set). Live on entity.annotation_batch.{id} through the entity
// manifest.

const pct = (v: number | undefined) => (v === undefined ? "—" : `${(v * 100).toFixed(2)} %`);

export function AnnotationBatchEmpty() {
  return (
    <EmptyState
      step="prepare"
      title="No annotation batch open"
      hint="A batch samples segments of one channel from an ingested dataset for people to transcribe: a golden set (double annotation) or training data."
      action={<NewBatchForm />}
    />
  );
}

export function AnnotationBatchPanel({ tab, entity, doc }: PanelProps) {
  const b = entity?.batch as Batch | undefined;
  if (!entity || !b) return <AnnotationBatchEmpty />;
  switch (tab) {
    case "activity":
      return <Activity id={b.id} />;
    case "details":
      return <Strata b={b} />;
    case "lineage":
      return <EmptyState step="record" title="Lineage" hint={b.freeze?.datasetVersionId ? `Frozen into ${b.freeze.datasetVersionId}${b.freeze.goldenSetVersionId ? ` and ${b.freeze.goldenSetVersionId}` : ""}.` : "Not frozen yet."} />;
    case "notes":
      return <EmptyState step="record" title="Notes live in NOTES.md" hint="Record what the batch taught about the guidelines as a project note." />;
    default:
      return <Overview b={b} doc={doc} />;
  }
}

function Section({ id, title, children, actions }: { id: string; title: string; children: React.ReactNode; actions?: React.ReactNode }) {
  return (
    <section aria-labelledby={id} className="flex flex-col gap-1.5">
      <div className="flex min-h-6 items-center gap-2">
        <h3 id={id} className="text-[11px] font-medium tracking-wide text-muted-foreground uppercase">
          {title}
        </h3>
        {actions ? <span className="ml-auto flex flex-wrap gap-1">{actions}</span> : null}
      </div>
      {children}
    </section>
  );
}

function Overview({ b, doc }: { b: Batch; doc?: string }) {
  const qc = useQueryClient();
  const refresh = () => void qc.invalidateQueries({ queryKey: batchesGetQueryKey({ path: { id: b.id } }) });
  const p = b.progress;
  const parts: { key: string; n: number; cls: string }[] = [
    { key: "agreed", n: p.agreed, cls: "bg-status-done" },
    { key: "adjudicated", n: p.adjudicated, cls: "bg-accent-line" },
    { key: "excluded", n: p.excluded, cls: "bg-muted-foreground" },
    { key: "disputed", n: p.disputed, cls: "bg-status-warning" },
    { key: "pending", n: p.pending, cls: "bg-border" },
  ];
  return (
    <div className="flex flex-col gap-5 p-4 text-xs" data-batch={b.id}>
      {b.description ? <p className="text-[13px] leading-relaxed">{b.description}</p> : null}
      <dl className="grid grid-cols-[9rem_1fr] gap-x-2 gap-y-0.5">
        <dt className="text-muted-foreground">Purpose</dt>
        <dd>
          {b.purpose === "golden-set" ? `golden set golden-set/${b.goldenSet}` : `training data dataset/${b.goldenSet}-annotated`} · target channel {b.role}
        </dd>
        <dt className="text-muted-foreground">Frame</dt>
        <dd>
          {b.frame.datasetVersionId ? (
            <span className="font-mono">{b.frame.datasetVersionId}</span>
          ) : (
            <span className="font-mono">{b.frame.segmentsHash.slice(0, 15)}…</span>
          )}{" "}
          · source {b.frame.source} · {b.frame.segments} {b.role} segments
        </dd>
        <dt className="text-muted-foreground">Guidelines</dt>
        <dd>
          {b.guidelines.path} at <span className="font-mono">{b.guidelines.commit.slice(0, 12)}</span>
        </dd>
        <dt className="text-muted-foreground">Sample</dt>
        <dd>
          {p.items} items · {Math.round(b.doubleShare * 100)} % annotated twice · seed {b.seed} · stratified by {b.stratify.join(", ")}
        </dd>
        <dt className="text-muted-foreground">Due</dt>
        <dd>{b.dueAt ? new Date(b.dueAt).toLocaleDateString() : "—"}</dd>
      </dl>
      <div className="overflow-hidden rounded-md border">
        <GuidelinesPane batchId={b.id} />
      </div>
      <Section id={`progress-${b.id}`} title="Progress">
        <div className="flex h-2 w-full overflow-hidden rounded bg-border" role="img" aria-label={parts.map((x) => `${x.n} ${x.key}`).join(", ")}>
          {parts.map((x) => (x.n > 0 ? <span key={x.key} className={x.cls} style={{ width: `${(x.n / Math.max(1, p.items)) * 100}%` }} /> : null))}
        </div>
        <p className="text-muted-foreground" data-slot="progress">
          {parts.map((x) => `${x.n} ${x.key}`).join(" · ")} · {p.annotations} annotations · double items {p.doubleDone} of {p.doubleItems} done
        </p>
      </Section>
      <Section id={`agreement-${b.id}`} title="Agreement">
        <p data-slot="agreement">
          Inter-annotator WER <span className={cn("font-medium", b.agreement.iaaWer !== undefined && !b.agreement.meets && "text-destructive")}>{pct(b.agreement.iaaWer)}</span> over{" "}
          {b.agreement.pairs} double item(s) ({b.agreement.edits} edits in {b.agreement.refWords} words) · target ≤ {pct(b.agreement.target)}
        </p>
        <p className="text-muted-foreground">
          End of utterance (the target stops → the other party answers):{" "}
          {b.eou.items ? `p50 ${seconds(b.eou.p50GapS)} · p90 ${seconds(b.eou.p90GapS)} over ${b.eou.items} items${b.eou.overlaps ? ` · ${b.eou.overlaps} overlaps` : ""}` : "not measured (single-channel audio)"}
        </p>
      </Section>
      <Reviewers b={b} doc={doc} />
      <Adjudication b={b} onChange={refresh} />
      <Freeze b={b} onChange={refresh} />
    </div>
  );
}

function Reviewers({ b, doc }: { b: Batch; doc?: string }) {
  const [open, setOpen] = useState(false);
  const [name, setName] = useState("");
  const [role, setRole] = useState<"annotator" | "adjudicator">("annotator");
  const [created, setCreated] = useState<InvitationCreated | undefined>();
  const [error, setError] = useState<string | undefined>();
  useEditRequest(doc ? `${INVITE_REQUEST}${doc}` : undefined, () => setOpen(true));
  const invitations = useQuery({ ...invitationsListOptions({ path: { id: b.id } }), retry: false });
  const invite = async () => {
    setError(undefined);
    try {
      const r = await runCommand("invitations.new", { batch: b.id, body: { name, role } });
      if (r && "token" in r) setCreated(r);
      void invitations.refetch();
    } catch (err) {
      setError(errorMessage(err));
    }
  };
  const link = created ? new URL(created.url, window.location.origin).toString() : "";
  return (
    <Section
      id={`reviewers-${b.id}`}
      title="Reviewers"
      actions={
        b.state === "open" ? (
          <Button size="xs" variant="outline" onClick={() => setOpen((o) => !o)} data-command="invitations.new">
            <UserPlus aria-hidden />
            Invite a reviewer
          </Button>
        ) : undefined
      }
    >
      <ul className="flex flex-col gap-0.5">
        {b.reviewers.map((r) => (
          <li key={r.id}>
            {r.name} <span className="text-muted-foreground">· {r.role} · {r.annotations} annotations{r.expiresAt ? ` · until ${new Date(r.expiresAt).toLocaleDateString()}` : ""}</span>
          </li>
        ))}
        {b.reviewers.length === 0 ? <li className="text-muted-foreground">Nobody has annotated yet.</li> : null}
      </ul>
      {invitations.data && invitations.data.items.length ? (
        <p className="text-muted-foreground">
          {invitations.data.items.length} invitation(s): {invitations.data.items.map((i) => `${i.reviewer.name}${i.revokedAt ? " (revoked)" : ""}`).join(", ")}
        </p>
      ) : null}
      {open ? (
        <div className="flex flex-wrap items-end gap-2 rounded border p-2">
          <label className="flex flex-col gap-1">
            <span className="text-muted-foreground">Reviewer name</span>
            <Input className="h-7 w-40" value={name} onChange={(e) => setName(e.target.value)} placeholder="ana" pattern="[a-z][a-z0-9._\-]{1,31}" />
          </label>
          <label className="flex flex-col gap-1">
            <span className="text-muted-foreground">Role</span>
            <NativeSelect className="h-7 w-32" value={role} onChange={(e) => setRole(e.target.value as "annotator" | "adjudicator")}>
              <option value="annotator">annotator</option>
              <option value="adjudicator">adjudicator</option>
            </NativeSelect>
          </label>
          <Button size="xs" onClick={() => void invite()} disabled={!name}>
            Create the link
          </Button>
          {error ? (
            <p role="alert" className="w-full text-destructive">
              {error}
            </p>
          ) : null}
          {created ? (
            <div className="flex w-full flex-col gap-1">
              <span className="text-muted-foreground">
                Send this link to {created.reviewer.name} (shown once). It opens this batch only — its items and their audio, no download — until{" "}
                {new Date(created.expiresAt).toLocaleString()}.
              </span>
              <span className="flex items-center gap-1">
                <Input readOnly className="h-7 font-mono" value={link} aria-label="Invitation link" />
                <Button size="icon-xs" variant="ghost" aria-label="Copy the link" onClick={() => void navigator.clipboard?.writeText(link)}>
                  <Copy aria-hidden />
                </Button>
              </span>
            </div>
          ) : null}
        </div>
      ) : null}
    </Section>
  );
}

function Adjudication({ b, onChange }: { b: Batch; onChange: () => void }) {
  const q = useQuery({ ...batchItemsListOptions({ path: { id: b.id }, query: { queue: "adjudication" } }), enabled: b.adjudication.queue > 0 });
  const [sel, setSel] = useState<string | undefined>();
  const items = q.data?.items ?? [];
  const item = items.find((i) => i.id === sel) ?? items[0];
  return (
    <Section id={`adjudication-${b.id}`} title={`Adjudication (${b.adjudication.queue})`}>
      {b.adjudication.queue === 0 ? (
        <p className="text-muted-foreground">No disputed item.</p>
      ) : (
        <div className="flex gap-2">
          <ul className="w-28 shrink-0">
            {items.map((it) => (
              <li key={it.id}>
                <button type="button" className={cn("w-full px-1 text-left hover:bg-hover", it.id === item?.id && "bg-selected")} onClick={() => setSel(it.id)}>
                  #{it.position} · WER {it.wer !== undefined ? it.wer.toFixed(2) : "—"}
                </button>
              </li>
            ))}
          </ul>
          <div className="min-w-0 flex-1">{item ? <Adjudicate key={item.id} batch={b.id} item={item} onDone={() => (onChange(), void q.refetch())} /> : null}</div>
        </div>
      )}
    </Section>
  );
}

function Adjudicate({ batch, item, onDone }: { batch: string; item: BatchItem; onDone: () => void }) {
  const [text, setText] = useState(item.annotations[0]?.text ?? "");
  const [error, setError] = useState<string | undefined>();
  const accept = async (body: { from?: string; text?: string; exclude?: boolean }) => {
    setError(undefined);
    try {
      await runCommand("batchItems.accept", { batch, item: { id: item.id, rev: item.rev }, body });
      onDone();
    } catch (err) {
      setError(errorMessage(err));
    }
  };
  const span = { start: Math.max(0, item.segment.start - item.window.start), end: Math.max(0, item.segment.end - item.window.start) };
  return (
    <div className="flex flex-col gap-2" data-item={item.id}>
      <AudioView utterance={item.id} channel={item.segment.channel >= 0 ? item.segment.channel : undefined} span={span} compact title={`Item ${item.position}, ${channelLabel(item, item.segment.channel)}`} />
      <div className="grid grid-cols-2 gap-2">
        {item.annotations
          .filter((a) => a.status !== "skipped")
          .map((a) => (
            <div key={a.id} className="flex flex-col gap-1 rounded border p-2">
              <span className="text-muted-foreground">
                {a.annotator.name ?? a.annotator.id} · {a.status}
                {a.tags.length ? ` · ${a.tags.join(", ")}` : ""}
              </span>
              <p dir="auto">
                <bdi>{a.text}</bdi>
              </p>
              <Button size="xs" variant="outline" onClick={() => void accept({ from: a.id })}>
                Take this transcript
              </Button>
            </div>
          ))}
      </div>
      <Textarea dir="auto" rows={2} value={text} onChange={(e) => setText(e.target.value)} aria-label="The final transcript" />
      <div className="flex gap-1">
        <Button size="xs" onClick={() => void accept({ text: text.trim() })} disabled={!text.trim()} data-command="batchItems.accept">
          Accept this text
        </Button>
        <Button size="xs" variant="ghost" onClick={() => void accept({ exclude: true })}>
          Exclude the item
        </Button>
      </div>
      {error ? (
        <p role="alert" className="text-destructive">
          {error}
        </p>
      ) : null}
    </div>
  );
}

function Freeze({ b, onChange }: { b: Batch; onChange: () => void }) {
  const [msg, setMsg] = useState<string | undefined>();
  const [error, setError] = useState<string | undefined>();
  const freeze = async (dryRun: boolean) => {
    setError(undefined);
    try {
      const r = await runCommand("batches.freeze", { batch: { id: b.id, rev: b.rev }, dryRun });
      if (r && "approvalId" in r) setMsg(`Waiting for the admin's approval (${r.approvalId}).`);
      else if (r && "items" in r) setMsg(dryRun ? `${r.items} item(s) would freeze (${r.hours.toFixed(3)} h), ${r.excluded} excluded.` : `Freezing: pipeline run ${r.pipelineRunId ?? "—"}.`);
      onChange();
    } catch (err) {
      setError(errorMessage(err));
    }
  };
  const f = b.freeze;
  return (
    <Section
      id={`freeze-${b.id}`}
      title="Freeze"
      actions={
        b.state === "open" || b.state === "failed" ? (
          <>
            <Button size="xs" variant="ghost" onClick={() => void freeze(true)}>
              Check
            </Button>
            <Button size="xs" variant="outline" disabled={!b.canFreeze.ok} title={b.canFreeze.reasons[0]} onClick={() => void freeze(false)} data-command="batches.freeze">
              <Lock aria-hidden />
              Freeze (approval)
            </Button>
          </>
        ) : undefined
      }
    >
      <p className="flex items-center gap-2">
        <StatusChip state={b.state === "frozen" ? "frozen" : b.state === "failed" ? "failed" : b.state === "freezing" ? "running" : "draft"} />
        {b.canFreeze.ok ? "Ready to freeze." : b.canFreeze.reasons.join(" · ")}
      </p>
      {f ? (
        <p className="text-muted-foreground" data-slot="freeze">
          {f.pipelineRunId ? `Cut: pipeline run ${f.pipelineRunId}. ` : ""}
          {f.datasetVersionId ? `Dataset version ${f.datasetVersionId}. ` : ""}
          {f.goldenSetVersionId ? (
            <button type="button" className="underline-offset-2 hover:underline" onClick={() => openDocument(`golden_set:${f.goldenSetVersionId}`)}>
              Golden set {f.goldenSetVersionId}
            </button>
          ) : null}
          {f.error ? <span className="text-destructive"> {f.error}</span> : null}
        </p>
      ) : null}
      {msg ? <p>{msg}</p> : null}
      {error ? (
        <p role="alert" className="text-destructive">
          {error}
        </p>
      ) : null}
    </Section>
  );
}

function Strata({ b }: { b: Batch }) {
  return (
    <div className="p-4 text-xs">
      <table className="w-full text-left" aria-label="Strata of the sample">
        <thead className="text-muted-foreground">
          <tr>
            {b.stratify.map((d) => (
              <th key={d} className="font-normal">
                {d}
              </th>
            ))}
            <th className="font-normal">In the frame</th>
            <th className="font-normal">Sampled</th>
          </tr>
        </thead>
        <tbody>
          {b.strata.map((s, i) => (
            <tr key={i}>
              {b.stratify.map((d) => (
                <td key={d}>{s.key[d] ?? "—"}</td>
              ))}
              <td className="tabular-nums">{s.frame}</td>
              <td className="tabular-nums">{s.sampled}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

function Activity({ id }: { id: string }) {
  const q = useQuery(eventsListOptions({ query: { topics: `entity.annotation_batch.${id}`, limit: 100 } }));
  const items = q.data?.items ?? [];
  return (
    <ul className="flex flex-col gap-0.5 p-4 text-xs">
      {items.length === 0 ? <li className="text-muted-foreground">No activity yet.</li> : null}
      {items.map((e) => (
        <li key={e.seq}>
          <span className="text-muted-foreground">{new Date(e.at).toLocaleString()}</span> {e.type.replace("annotation_batch.", "").replaceAll("_", " ")}
          {e.actor?.name ? <span className="text-muted-foreground"> · {e.actor.name}</span> : null}
        </li>
      ))}
    </ul>
  );
}

function NewBatchForm() {
  const project = useProject();
  const [body, setBody] = useState<BatchNew>({ name: "", purpose: "golden-set" });
  const [plan, setPlan] = useState<Batch | undefined>();
  const [error, setError] = useState<string | undefined>();
  const set = (patch: Partial<BatchNew>) => {
    setBody((b) => ({ ...b, ...patch }));
    setPlan(undefined);
  };
  const run = async (dryRun: boolean) => {
    if (!project) return;
    setError(undefined);
    try {
      const r = await runCommand("batches.new", { project, body, dryRun });
      if (dryRun) setPlan(r);
      else if (r) openDocument(`annotation_batch:${r.id}`);
    } catch (err) {
      setError(errorMessage(err));
    }
  };
  const frame = (v: string) => (v.startsWith("b3:") ? { segments: v, dataset: undefined } : { dataset: v || undefined, segments: undefined });
  return (
    <form
      className="flex w-80 flex-col gap-2 text-left text-xs"
      onSubmit={(e) => {
        e.preventDefault();
        void run(!plan);
      }}
    >
      <label className="flex flex-col gap-1">
        <span className="text-muted-foreground">Name</span>
        <Input className="h-7" value={body.name} onChange={(e) => set({ name: e.target.value })} placeholder="calls-sr-1" required />
      </label>
      <label className="flex flex-col gap-1">
        <span className="text-muted-foreground">Frame: a dataset version (ver_… or dataset/name) or a segments artifact (b3:…)</span>
        <Input className="h-7" value={body.dataset ?? body.segments ?? ""} onChange={(e) => set(frame(e.target.value.trim()))} required />
      </label>
      <div className="flex gap-2">
        <label className="flex flex-1 flex-col gap-1">
          <span className="text-muted-foreground">Purpose</span>
          <NativeSelect className="h-7" value={body.purpose} onChange={(e) => set({ purpose: e.target.value as BatchNew["purpose"] })}>
            <option value="golden-set">golden set</option>
            <option value="training">training data</option>
          </NativeSelect>
        </label>
        <label className="flex w-20 flex-col gap-1">
          <span className="text-muted-foreground">Items</span>
          <Input className="h-7" type="number" min={1} max={5000} value={body.size ?? ""} placeholder="200" onChange={(e) => set({ size: e.target.value ? Number(e.target.value) : undefined })} />
        </label>
        <label className="flex w-20 flex-col gap-1">
          <span className="text-muted-foreground">Double %</span>
          <Input className="h-7" type="number" min={0} max={100} value={body.doubleShare !== undefined ? Math.round(body.doubleShare * 100) : ""} placeholder="10" onChange={(e) => set({ doubleShare: e.target.value ? Number(e.target.value) / 100 : undefined })} />
        </label>
      </div>
      <label className="flex flex-col gap-1">
        <span className="text-muted-foreground">Guidelines (annotation/guidelines/&lt;name&gt;.md)</span>
        <Input className="h-7" value={body.guidelines ?? ""} placeholder="default" onChange={(e) => set({ guidelines: e.target.value || undefined })} />
      </label>
      {plan ? (
        <p className="rounded border p-2" data-slot="batch-plan">
          {plan.sample?.length ?? 0} item(s) from {plan.frame.segments} {plan.role} segments of {plan.frame.source}, {plan.strata.length} strata; guidelines at{" "}
          {plan.guidelines.commit.slice(0, 8)}.
        </p>
      ) : null}
      {error ? (
        <p role="alert" className="text-destructive">
          {error}
        </p>
      ) : null}
      <Button size="xs" type="submit" disabled={!project || !body.name}>
        <Plus aria-hidden />
        {plan ? "Create the batch" : "Preview the sample"}
      </Button>
    </form>
  );
}
