import { useEffect, useRef, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { goldenSetsGetQueryKey, normalizersGetOptions, normalizersListOptions } from "@/api/gen/@tanstack/react-query.gen";
import type { GoldenSetFreeze, GoldenSetVersion, NormalizerPayload, Problem } from "@/api/gen/types.gen";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { NativeSelect } from "@/components/ui/native-select";
import { ActorBadge, EmptyState } from "@/shell/entity/primitives";
import { ADOPT_REQUEST, errorMessage, openDocument, openPanelById, problemOf, runCommand, useEditRequest, useProject, type PanelProps } from "@/shell/panel";

// The Golden set document (docs/spec/11-ui-panels.md "Panel catalogue", Golden set; R21): a frozen golden set version
// — the eval-only dataset version and scoring normalizer version it pins, locale, domain, size, the bootstrap's
// resampling unit, the projects that adopted it — and the freeze form (goldenSets.freeze: a dry run shows what would
// be registered; the real call always waits for an admin's approval).

export function GoldenSetEmpty() {
  return <EmptyState step="record" title="No golden set open" hint="Open a golden set from the Library or an Eval report." />;
}

export function GoldenSetPanel({ tab, entity, doc }: PanelProps) {
  const g = entity?.goldenSet as GoldenSetVersion | undefined;
  if (!entity || !g) return <GoldenSetEmpty />;
  switch (tab) {
    case "details":
      return <Details g={g} />;
    case "lineage":
      return (
        <div className="flex flex-col gap-2 p-4 text-xs">
          <p>The dataset version and normalizer this golden set pins, and the projects and evals that use it, are drawn by the Lineage panel.</p>
          <Button size="xs" variant="outline" className="w-fit" onClick={() => openPanelById("lineage")}>
            Open Lineage
          </Button>
        </div>
      );
    case "activity":
    case "notes":
      return <EmptyState step="record" title="Registry versions are immutable" hint="A golden set changes only by freezing a new version." />;
    default:
      return <Overview g={g} doc={doc} />;
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

function Overview({ g, doc }: { g: GoldenSetVersion; doc?: string }) {
  const p = g.goldenSet;
  const [freezeOpen, setFreezeOpen] = useState(false);
  const [adoptOpen, setAdoptOpen] = useState(false);
  const project = useProject();
  const adopted = !!project && g.usedBy.some((u) => u.projectSlug === project);
  useEditRequest(doc, () => setFreezeOpen(true));
  useEditRequest(doc ? `${ADOPT_REQUEST}${doc}` : undefined, () => setAdoptOpen(true));
  return (
    <div className="flex flex-col gap-5 p-4 text-xs" data-golden-set={g.id}>
      {freezeOpen ? <FreezeForm g={g} onClose={() => setFreezeOpen(false)} /> : null}
      {adoptOpen && project ? <AdoptCard g={g} project={project} adopted={adopted} onClose={() => setAdoptOpen(false)} /> : null}
      <section aria-labelledby={`gs-pins-${g.id}`} className="flex flex-col gap-1.5">
        <h3 id={`gs-pins-${g.id}`} className="text-[11px] font-medium tracking-wide text-muted-foreground uppercase">
          What it pins
        </h3>
        <dl className="grid grid-cols-[9rem_1fr] gap-x-3 gap-y-1">
          <Row label="Dataset version">
            <code className="text-[11px]">{p.datasetVersionId}</code>
          </Row>
          <Row label="Dataset hash">
            <code className="text-[11px]">{p.datasetHash}</code>
          </Row>
          <Row label="Normalizer">
            <code className="text-[11px]">{p.normalizerVersionId}</code>
          </Row>
          <Row label="Locale · domain">
            {p.locale}
            {p.domain ? ` · ${p.domain}` : ""}
          </Row>
          <Row label="Size">
            {p.utterances.toLocaleString()} utterances · {p.hours.toFixed(2)} h
          </Row>
          <Row label="Resampled by">
            {p.groups} <span className="text-muted-foreground">(the bootstrap's unit for confidence intervals)</span>
          </Row>
          <Row label="Fingerprint">
            <code className="text-[11px]">{p.fingerprint}</code>
          </Row>
          <Row label="Leakage">
            <span className="text-muted-foreground">Checked at freeze: none of its utterances is in a training dataset version, and mixes refuse them from now on.</span>
          </Row>
          {p.approvalId ? <Row label="Approved by">{p.approvalId}</Row> : null}
        </dl>
        {!freezeOpen ? (
          <Button size="xs" variant="outline" className="w-fit" onClick={() => setFreezeOpen(true)} data-command="goldenSets.freeze">
            Freeze a new version…
          </Button>
        ) : null}
      </section>
      <Normalizer id={p.normalizerVersionId} />
      <section aria-labelledby={`gs-used-${g.id}`} className="flex flex-col gap-1.5">
        <h3 id={`gs-used-${g.id}`} className="text-[11px] font-medium tracking-wide text-muted-foreground uppercase">
          Used by
        </h3>
        {g.usedBy.length ? (
          <table className="w-full" aria-label="Projects that adopted it">
            <thead className="text-left text-muted-foreground">
              <tr>
                <th className="font-normal">Project</th>
                <th className="font-normal">Adopted</th>
                <th className="font-normal">Aliases</th>
              </tr>
            </thead>
            <tbody>
              {g.usedBy.map((u) => (
                <tr key={u.projectId} className="h-7 border-t">
                  <td>
                    <button type="button" className="underline-offset-2 hover:underline" onClick={() => openDocument(`project:${u.projectSlug}`)}>
                      {u.projectSlug}
                    </button>
                  </td>
                  <td className="text-muted-foreground">{new Date(u.adoptedAt).toLocaleDateString()}</td>
                  <td>{u.aliases.join(", ") || "—"}</td>
                </tr>
              ))}
            </tbody>
          </table>
        ) : (
          <p className="text-muted-foreground">No project has adopted it yet. Adopt it into a project and name it in gates.yaml so evals score on it.</p>
        )}
        {project && !adopted && !adoptOpen ? (
          <Button size="xs" variant="outline" className="w-fit" onClick={() => setAdoptOpen(true)} data-command="projects.adopt">
            Adopt into {project}…
          </Button>
        ) : null}
      </section>
    </div>
  );
}

/**
 * projects.adopt (R21, the leakage check): the dry run asks first whether the project may adopt the golden set — a
 * project whose training data already holds some of its utterances is refused (golden-set-leakage, with the
 * overlapping dataset versions) — then Adopt does it. A golden set in none of the project's languages is refused as a
 * target (locale-mismatch, phase 4); a replay set is checked and adopted with purpose replay.
 */
function AdoptCard({ g, project, adopted, onClose }: { g: GoldenSetVersion; project: string; adopted: boolean; onClose: () => void }) {
  const qc = useQueryClient();
  const [busy, setBusy] = useState(false);
  const [checked, setChecked] = useState(false);
  const [done, setDone] = useState(false);
  const [problem, setProblem] = useState<{ text: string; problem?: Problem } | null>(null);
  const [purpose, setPurpose] = useState<"target" | "replay">("target");
  const act = async (dryRun: boolean, as: "target" | "replay" = purpose) => {
    setBusy(true);
    setProblem(null);
    try {
      await runCommand("projects.adopt", { project, version: g.id, dryRun, ...(as === "replay" ? { purpose: as } : {}) });
      setPurpose(as);
      if (dryRun) setChecked(true);
      else {
        setDone(true);
        void qc.invalidateQueries({ queryKey: goldenSetsGetQueryKey({ path: { id: g.id } }) });
      }
    } catch (err) {
      setProblem({ text: errorMessage(err), problem: problemOf(err) });
    } finally {
      setBusy(false);
    }
  };
  const asked = useRef(false);
  useEffect(() => {
    if (asked.current || adopted) return;
    asked.current = true;
    void act(true);
  });
  const leakage = problem?.problem?.type.endsWith("/golden-set-leakage");
  const locale = problem?.problem?.type.endsWith("/locale-mismatch");
  return (
    <div className="flex flex-col gap-1.5 rounded-md border bg-tool p-2" role="group" aria-label="Adopt into project" data-slot="adopt-card">
      <p>
        Adopt <span className="font-medium">{g.name}</span> {g.version} into <span className="font-medium">{project}</span>: evals of the project score on it, and its
        utterances can never enter the project's training mixes.
      </p>
      {adopted && !done ? <p className="text-muted-foreground">{project} already adopted it.</p> : null}
      {checked && !done && !problem ? (
        <p role="status">Checked{purpose === "replay" ? " as a replay set" : ""}: no training data of the project overlaps it.</p>
      ) : null}
      {done ? (
        <p role="status" className="text-status-done-foreground">
          Adopted. Name it in gates.yaml (Project home → Gate) to make it a target or replay set.
        </p>
      ) : null}
      {problem ? (
        <div role="alert" className="text-destructive" data-slot="adopt-problem" data-leakage={leakage || undefined}>
          <p className="font-medium">{leakage ? "Refused: the project's training data overlaps this golden set" : "Not adopted"}</p>
          <p>{problem.text}</p>
          {problem.problem?.errors?.length ? (
            <ul className="mt-1 list-disc pl-5">
              {problem.problem.errors.map((e, i) => (
                <li key={i}>{e.message}</li>
              ))}
            </ul>
          ) : null}
        </div>
      ) : null}
      <div className="flex gap-1">
        <Button size="xs" disabled={busy || adopted || done || !!problem || !checked} onClick={() => void act(false)} data-command="projects.adopt">
          Adopt
        </Button>
        {locale && !done ? (
          <Button size="xs" variant="outline" disabled={busy} onClick={() => void act(true, "replay")}>
            Check as replay
          </Button>
        ) : null}
        <Button size="xs" variant="ghost" onClick={onClose}>
          {done ? "Close" : "Cancel"}
        </Button>
      </div>
    </div>
  );
}

/** The scoring normalizer's rules: the text both sides of a WER are compared after (R21). */
export function NormalizerRules({ n }: { n: NormalizerPayload }) {
  return (
    <dl className="grid grid-cols-[9rem_1fr] gap-x-3 gap-y-1" data-slot="normalizer-rules">
      <Row label="Locale">{n.locale}</Row>
      <Row label="Unicode">{n.unicode}</Row>
      <Row label="Case folding">{n.casefold ? "yes" : "no"}</Row>
      <Row label="Punctuation">{n.punctuation === "strip" ? "stripped (becomes a space)" : "kept"}</Row>
      <Row label="Combining marks">{n.removeMarks ? "removed (niqqud, accents)" : "kept"}</Row>
      <Row label="Numbers">{n.numbers === "keep" ? "compared as written" : n.numbers}</Row>
      <Row label="Mappings">
        {n.mappings.length ? (
          <ul className="flex flex-col">
            {n.mappings.map((m, i) => (
              <li key={i}>
                <bdi className="rounded bg-hover px-1 font-mono">{m.from}</bdi> → <bdi className="rounded bg-hover px-1 font-mono">{m.to}</bdi>
              </li>
            ))}
          </ul>
        ) : (
          "none"
        )}
      </Row>
      {n.description ? <Row label="Description">{n.description}</Row> : null}
    </dl>
  );
}

function Normalizer({ id }: { id: string }) {
  const q = useQuery(normalizersGetOptions({ path: { id } }));
  return (
    <section aria-labelledby={`gs-norm-${id}`} className="flex flex-col gap-1.5">
      <h3 id={`gs-norm-${id}`} className="text-[11px] font-medium tracking-wide text-muted-foreground uppercase">
        Scoring normalizer {q.data ? `· ${q.data.name} ${q.data.version}` : ""}
      </h3>
      {q.isLoading ? <p className="text-muted-foreground">Loading…</p> : null}
      {q.error ? <p className="text-destructive">{errorMessage(q.error)}</p> : null}
      {q.data ? <NormalizerRules n={q.data.normalizer} /> : null}
    </section>
  );
}

/** goldenSets.freeze: an eval-only dataset version and a normalizer become a golden set (an admin approves). */
function FreezeForm({ g, onClose }: { g: GoldenSetVersion; onClose: () => void }) {
  const p = g.goldenSet;
  const normalizers = useQuery(normalizersListOptions({ query: { state: "frozen" } }));
  const [dataset, setDataset] = useState(p.datasetVersionId);
  const [normalizer, setNormalizer] = useState(p.normalizerVersionId);
  const [name, setName] = useState(g.name);
  const [domain, setDomain] = useState(p.domain ?? "");
  const [groups, setGroups] = useState<"" | GoldenSetFreeze["groups"]>("");
  const [busy, setBusy] = useState(false);
  const [preview, setPreview] = useState<GoldenSetVersion | null>(null);
  const [message, setMessage] = useState<{ error: boolean; text: string; approval?: boolean } | null>(null);
  const first = useRef<HTMLInputElement>(null);
  useEffect(() => first.current?.focus(), []);
  const body: GoldenSetFreeze = {
    datasetVersionId: dataset.trim(),
    ...(normalizer ? { normalizerVersionId: normalizer } : {}),
    ...(name.trim() ? { name: name.trim() } : {}),
    ...(domain.trim() ? { domain: domain.trim() } : {}),
    ...(groups ? { groups } : {}),
  };
  const changed = () => {
    setPreview(null);
    setMessage(null);
  };
  const act = async (dryRun: boolean) => {
    setBusy(true);
    setMessage(null);
    try {
      const res = await runCommand("goldenSets.freeze", { body, dryRun });
      if (!res) return;
      if ("approvalId" in res) setMessage({ error: false, approval: true, text: `The freeze waits for an admin's approval (${res.approvalId}).` });
      else if (dryRun) setPreview(res);
      else {
        openDocument(`golden_set:${res.id}`);
        onClose();
      }
    } catch (err) {
      setMessage({ error: true, text: errorMessage(err) });
    } finally {
      setBusy(false);
    }
  };
  const id = g.id;
  return (
    <form
      onSubmit={(e) => {
        e.preventDefault();
        if (body.datasetVersionId) void act(true);
      }}
      className="flex flex-col gap-2 rounded-md border bg-tool p-3"
      aria-label="Freeze golden set"
      data-slot="freeze-form"
    >
      <p>Freeze an eval-only dataset version with a scoring normalizer into a golden set. Its utterances are then kept out of every training mix.</p>
      <div className="grid grid-cols-[8rem_1fr] items-center gap-2">
        <label htmlFor={`freeze-ds-${id}`} className="text-muted-foreground">
          Dataset version
        </label>
        <Input ref={first} id={`freeze-ds-${id}`} className="h-6 text-xs" placeholder="ver_… or dataset/<name>" value={dataset} onChange={(e) => (setDataset(e.target.value), changed())} />
        <label htmlFor={`freeze-norm-${id}`} className="text-muted-foreground">
          Normalizer
        </label>
        <NativeSelect id={`freeze-norm-${id}`} className="h-6 w-auto text-xs" value={normalizer} onChange={(e) => (setNormalizer(e.target.value), changed())}>
          <option value="">The default (defaults.yaml eval.normalizer)</option>
          {(normalizers.data?.items ?? []).map((n) => (
            <option key={n.id} value={n.id}>
              {n.name} {n.version} ({n.normalizer.locale})
            </option>
          ))}
        </NativeSelect>
        <label htmlFor={`freeze-name-${id}`} className="text-muted-foreground">
          Collection
        </label>
        <Input id={`freeze-name-${id}`} className="h-6 text-xs" placeholder="golden-set/<name>" value={name} onChange={(e) => (setName(e.target.value), changed())} />
        <label htmlFor={`freeze-domain-${id}`} className="text-muted-foreground">
          Domain
        </label>
        <Input id={`freeze-domain-${id}`} className="h-6 text-xs" placeholder="telephone, read-speech, …" value={domain} onChange={(e) => (setDomain(e.target.value), changed())} />
        <label htmlFor={`freeze-groups-${id}`} className="text-muted-foreground">
          Resample by
        </label>
        <NativeSelect id={`freeze-groups-${id}`} className="h-6 w-auto text-xs" value={groups ?? ""} onChange={(e) => (setGroups(e.target.value as typeof groups), changed())}>
          <option value="">Speaker when known, else utterance</option>
          <option value="call">Call</option>
          <option value="speaker">Speaker</option>
          <option value="utterance">Utterance</option>
        </NativeSelect>
      </div>
      {preview ? (
        <p className="text-muted-foreground" data-slot="freeze-preview">
          Would register <span className="font-medium text-foreground">{preview.name}</span> {preview.version}: {preview.goldenSet.utterances.toLocaleString()} utterances,{" "}
          {preview.goldenSet.hours.toFixed(2)} h, resampled by {preview.goldenSet.groups}, normalizer {preview.goldenSet.normalizerVersionId}.
        </p>
      ) : null}
      <div className="flex gap-1">
        <Button type="submit" size="xs" variant="outline" disabled={busy || !body.datasetVersionId}>
          Check
        </Button>
        <Button type="button" size="xs" disabled={busy || !preview} title={preview ? undefined : "Check first"} onClick={() => void act(false)} data-command="goldenSets.freeze">
          Freeze (asks the admin)
        </Button>
        <Button type="button" size="xs" variant="ghost" onClick={onClose}>
          Cancel
        </Button>
      </div>
      {message ? (
        <p role={message.error ? "alert" : "status"} className={message.error ? "text-destructive" : "text-muted-foreground"}>
          {message.text}
          {message.approval ? (
            <>
              {" "}
              <button type="button" className="underline underline-offset-2" onClick={() => openPanelById("approvals")}>
                Open Approvals
              </button>
            </>
          ) : null}
        </p>
      ) : null}
    </form>
  );
}

function Details({ g }: { g: GoldenSetVersion }) {
  return (
    <dl className="grid grid-cols-[10rem_1fr] gap-x-4 gap-y-2 p-4 text-xs">
      <Row label="ID">
        <code className="font-mono text-[11px]">{g.id}</code>
      </Row>
      <Row label="Collection">
        {g.name} <span className="text-muted-foreground">({g.collectionId})</span>
      </Row>
      <Row label="Version">{g.version}</Row>
      <Row label="State">{g.state}</Row>
      <Row label="Tags">{g.tags.join(", ") || "—"}</Row>
      <Row label="Licence">{g.licence || "—"}</Row>
      <Row label="Fingerprint">
        <code className="text-[11px]">{g.fingerprint}</code>
      </Row>
      <Row label="Frozen by">
        <ActorBadge actor={g.actor} /> {new Date(g.createdAt).toLocaleString()}
      </Row>
    </dl>
  );
}
