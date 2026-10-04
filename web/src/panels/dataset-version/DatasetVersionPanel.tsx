import { useEffect, useRef, useState } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { datasetsGetQueryKey } from "@/api/gen/@tanstack/react-query.gen";
import type { DatasetFreeze, DatasetPayload, DatasetPreview, DatasetPreviewRequest, DatasetVersion, Problem } from "@/api/gen/types.gen";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { AnalyticsChart } from "@/shell/charts";
import { datasetCharts, FREEZE_REQUEST, PREVIEW_REQUEST, UtteranceSearch } from "@/shell/data";
import { ActorBadge, EmptyState } from "@/shell/entity/primitives";
import { ADOPT_REQUEST, errorMessage, focusPipelineRun, openDocument, openPanelById, problemOf, runCommand, useEditRequest, useProject, type PanelProps } from "@/shell/panel";

// The Dataset version document (docs/spec/11-ui-panels.md "Panel catalogue", Dataset version; R53; the phase-4 plan's
// decisions 3–4): a draft ingested in place on a mount is previewed (datasets.preview) and frozen (datasets.freeze:
// the leakage check first, then the cut into the content store with the quality checks and the card); a frozen version
// shows its shards, quality checks, card and statistics charts, and is adopted into the project. Everything it shows
// is datasets.get, as an agent reads it.

export function DatasetVersionEmpty() {
  return <EmptyState step="prepare" title="No dataset version open" hint="Open a dataset version from the Library, a Source or a Mix." />;
}

export function DatasetVersionPanel({ tab, entity, doc }: PanelProps) {
  const v = entity?.datasetVersion as DatasetVersion | undefined;
  if (!entity || !v) return <DatasetVersionEmpty />;
  switch (tab) {
    case "details":
      return <Details v={v} />;
    case "lineage":
      return (
        <div className="flex flex-col gap-2 p-4 text-xs">
          <p>The sources, pipeline runs and golden sets around this version, and the mixes, runs and projects that use it, are drawn by the Lineage panel.</p>
          <Button size="xs" variant="outline" className="w-fit" onClick={() => openPanelById("lineage")}>
            Open Lineage
          </Button>
        </div>
      );
    case "activity":
    case "notes":
      return <EmptyState step="record" title="Registry versions are immutable" hint="A frozen dataset version never changes; a new ingest or import registers a new version." />;
    default:
      return <Overview v={v} doc={doc} />;
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

const hours = (h: number) => `${h.toFixed(2)} h`;
const bytes = (b: number) => (b >= 1e9 ? `${(b / 1e9).toFixed(2)} GB` : b >= 1e6 ? `${(b / 1e6).toFixed(1)} MB` : `${Math.round(b / 1e3)} kB`);
const short = (h: string) => (h.startsWith("b3:") ? `${h.slice(0, 15)}…` : h);

function Overview({ v, doc }: { v: DatasetVersion; doc?: string }) {
  const d = v.dataset;
  const project = useProject();
  const [freezeOpen, setFreezeOpen] = useState(false);
  const [previewOpen, setPreviewOpen] = useState(false);
  const [adoptOpen, setAdoptOpen] = useState(false);
  const [filter, setFilter] = useState<DatasetPreviewRequest | undefined>(undefined);
  const adopted = !!project && v.usedBy.some((u) => u.projectSlug === project);
  useEditRequest(doc ? `${FREEZE_REQUEST}${doc}` : undefined, () => setFreezeOpen(true));
  useEditRequest(doc ? `${PREVIEW_REQUEST}${doc}` : undefined, () => setPreviewOpen(true));
  useEditRequest(doc ? `${ADOPT_REQUEST}${doc}` : undefined, () => setAdoptOpen(true));
  const draft = v.state === "draft";
  const runId = d.freeze?.pipelineRunId;
  return (
    <div className="flex flex-col gap-5 p-4 text-xs @container" data-dataset-version={v.id} data-state={v.state}>
      <Section id={`ds-state-${v.id}`} title={draft ? "Draft" : v.state === "archived" ? "Archived" : "Frozen"} slot="dataset-state">
        {draft ? (
          <p>
            Indexed in place: its segments stay on the mount until the freeze cuts them into the content store. Preview its hours per language and split, then freeze it — the leakage
            check runs first, then the quality checks and the dataset card. Only frozen versions can be mixed, trained on, exported or adopted.
          </p>
        ) : (
          <p>
            Frozen{d.freeze?.frozenAt ? ` ${new Date(d.freeze.frozenAt).toLocaleString()}` : ""}: its content never changes.
            {d.evalOnly ? " Eval-only: it is evaluated on, never mixed or trained on." : ""}
          </p>
        )}
        {runId && draft ? (
          <p role="status">
            Freezing in pipeline run <code className="text-[11px]">{runId}</code>.{" "}
            <button
              type="button"
              className="underline underline-offset-2"
              onClick={() => {
                focusPipelineRun(runId);
                openPanelById("pipeline-run");
              }}
            >
              Open Pipeline run
            </button>
          </p>
        ) : null}
        <div className="flex flex-wrap gap-1">
          {draft && !freezeOpen && !runId ? (
            <Button size="xs" onClick={() => setFreezeOpen(true)} data-command="datasets.freeze">
              Freeze…
            </Button>
          ) : null}
          {!previewOpen ? (
            <Button size="xs" variant="outline" onClick={() => setPreviewOpen(true)} data-command="datasets.preview">
              Preview with filters…
            </Button>
          ) : null}
        </div>
        {freezeOpen ? <FreezeCard v={v} onClose={() => setFreezeOpen(false)} /> : null}
        {previewOpen ? <PreviewCard v={v} onFilter={setFilter} onClose={() => setPreviewOpen(false)} /> : null}
        {!draft ? <Leakage v={v} /> : null}
      </Section>

      <Section id={`ds-summary-${v.id}`} title="What it holds">
        <dl className="grid grid-cols-[9rem_1fr] gap-x-3 gap-y-1">
          <Row label="Size">
            {d.utterances.toLocaleString()} utterances · {hours(d.hours)}
            {d.speakers ? ` · ${d.speakers.toLocaleString()} speakers` : ""}
            {d.bytes ? ` · ${bytes(d.bytes)}` : ""}
          </Row>
          <Row label="Languages">{d.locales.join(", ") || "—"}</Row>
          <Row label="Source">
            {d.source}
            {d.sourceRevision ? ` @ ${d.sourceRevision}` : ""}
            {d.subset ? ` (${d.subset})` : ""}
          </Row>
          {d.sourceIds?.length ? (
            <Row label="Sources">
              <span className="flex flex-wrap gap-1">
                {d.sourceIds.map((s) => (
                  <button key={s} type="button" className="underline-offset-2 hover:underline" onClick={() => openDocument(`source:${s}`)}>
                    <code className="text-[11px]">{s}</code>
                  </button>
                ))}
              </span>
            </Row>
          ) : null}
          <Row label="Licence">{d.licence ?? v.licence ?? "—"}</Row>
          <Row label="Split rule">{d.splitRule ?? "—"}</Row>
          {d.domain ? <Row label="Domain">{d.domain}</Row> : null}
          {d.recipe ? (
            <Row label="Recipe">
              {d.recipe.pipeline ?? "—"}
              {d.recipe.commit ? ` @ ${d.recipe.commit.slice(0, 7)}` : ""}
              {d.recipe.stepKind ? ` · ${d.recipe.stepKind}` : ""}
            </Row>
          ) : null}
          {d.fixture ? <Row label="Fixture">metadata only: no audio is stored</Row> : null}
        </dl>
        <table className="w-full max-w-md text-xs" aria-label="Splits">
          <thead className="text-left text-muted-foreground">
            <tr>
              <th className="font-normal">Split</th>
              <th className="font-normal">Utterances</th>
              <th className="font-normal">Hours</th>
              <th className="font-normal">Speakers</th>
            </tr>
          </thead>
          <tbody>
            {d.splits.map((s) => (
              <tr key={s.name} className="h-6 border-t">
                <td>{s.name}</td>
                <td className="tabular-nums">{s.utterances.toLocaleString()}</td>
                <td className="tabular-nums">{s.hours.toFixed(2)}</td>
                <td className="tabular-nums">{s.speakers ?? "—"}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </Section>

      <Quality d={d} id={v.id} />
      <Statistics d={d} id={v.id} filter={filter} />
      <Shards d={d} id={v.id} />

      <Section id={`ds-used-${v.id}`} title="Used by" slot="dataset-used-by">
        {v.usedBy.length ? (
          <ul className="flex flex-wrap gap-2">
            {v.usedBy.map((u) => (
              <li key={u.projectId}>
                <button type="button" className="underline-offset-2 hover:underline" onClick={() => openDocument(`project:${u.projectSlug}`)}>
                  {u.projectSlug}
                </button>
                {u.aliases.length ? <span className="text-muted-foreground"> (@{u.aliases.join(", @")})</span> : null}
              </li>
            ))}
          </ul>
        ) : (
          <p className="text-muted-foreground">No project has adopted it yet.</p>
        )}
        {project && v.state === "frozen" && !adopted && !adoptOpen ? (
          <Button size="xs" variant="outline" className="w-fit" onClick={() => setAdoptOpen(true)} data-command="projects.adopt">
            Adopt into {project}…
          </Button>
        ) : null}
        {adoptOpen && project ? <AdoptCard v={v} project={project} adopted={adopted} onClose={() => setAdoptOpen(false)} /> : null}
      </Section>

      <Section id={`ds-utt-${v.id}`} title="Utterances" slot="dataset-utterances">
        <UtteranceSearch dataset={v.id} />
      </Section>
    </div>
  );
}

/** The leakage result of a frozen version: the freeze checked it against every golden set and refused an overlap. */
function Leakage({ v }: { v: DatasetVersion }) {
  return (
    <p className="text-muted-foreground" data-slot="dataset-leakage">
      <span aria-hidden>✓ </span>Leakage: {v.dataset.freeze ? "checked when it was frozen" : "checked when it was imported"} — none of its training or validation utterances is in a
      golden set (by content hash or fingerprint), and mixes keep refusing golden-set audio from now on.
    </p>
  );
}

function ProblemList({ problem, text }: { problem?: Problem; text: string }) {
  return (
    <div role="alert" className="text-destructive">
      <p>{text}</p>
      {problem?.errors?.length ? (
        <ul className="mt-1 list-disc pl-5">
          {problem.errors.slice(0, 20).map((e, i) => (
            <li key={i}>{e.message}</li>
          ))}
        </ul>
      ) : null}
    </div>
  );
}

/**
 * datasets.freeze: the dry run is the leakage check (every golden set); Freeze starts the cut (202, the job) and the
 * version turns frozen when the pipeline run ends.
 */
function FreezeCard({ v, onClose }: { v: DatasetVersion; onClose: () => void }) {
  const qc = useQueryClient();
  const [busy, setBusy] = useState(false);
  const [checked, setChecked] = useState<DatasetFreeze | null>(null);
  const [started, setStarted] = useState<string | null>(null);
  const [problem, setProblem] = useState<{ text: string; problem?: Problem } | null>(null);
  const act = async (dryRun: boolean) => {
    setBusy(true);
    setProblem(null);
    try {
      const res = await runCommand("datasets.freeze", { version: v.id, dryRun });
      if (!res) return;
      if ("jobId" in res) setStarted(res.jobId);
      else if (dryRun) setChecked(res);
      else setStarted("done");
      if (!dryRun) void qc.invalidateQueries({ queryKey: datasetsGetQueryKey({ path: { id: v.id } }) });
    } catch (err) {
      setProblem({ text: errorMessage(err), problem: problemOf(err) });
    } finally {
      setBusy(false);
    }
  };
  const asked = useRef(false);
  useEffect(() => {
    if (asked.current) return;
    asked.current = true;
    void act(true);
  });
  const leakage = problem?.problem?.type.endsWith("/golden-set-leakage");
  return (
    <div className="flex flex-col gap-1.5 rounded-md border bg-tool p-2" role="group" aria-label="Freeze dataset version" data-slot="freeze-card">
      <p>
        Freeze <span className="font-medium">{v.name}</span> {v.version}: a CPU pipeline run cuts every segment from its mount into the content store, verifies each hash, and
        writes the shards, the quality checks and the dataset card.
      </p>
      {busy && !checked && !problem ? <p className="text-muted-foreground">Checking leakage…</p> : null}
      {checked && !started ? (
        <p role="status" data-slot="leakage-result">
          <span aria-hidden>✓ </span>Leakage check passed against {checked.leakage.goldenSets} golden set{checked.leakage.goldenSets === 1 ? "" : "s"}.
        </p>
      ) : null}
      {started ? (
        <p role="status" className="text-status-done-foreground">
          {started === "done" ? "Frozen." : `Freezing (job ${started}); the version turns frozen when the cut ends.`}
        </p>
      ) : null}
      {problem ? (
        <div data-slot="freeze-problem" data-leakage={leakage || undefined}>
          <p className="font-medium text-destructive">{leakage ? "Refused: it shares utterances with a golden set" : "Not frozen"}</p>
          <ProblemList problem={problem.problem} text={problem.text} />
        </div>
      ) : null}
      <div className="flex gap-1">
        <Button size="xs" disabled={busy || !checked || !!started} onClick={() => void act(false)} data-command="datasets.freeze">
          Freeze
        </Button>
        <Button size="xs" variant="ghost" onClick={onClose}>
          {started ? "Close" : "Cancel"}
        </Button>
      </div>
    </div>
  );
}

const csv = (s: string) =>
  s
    .split(",")
    .map((x) => x.trim())
    .filter(Boolean);
const num = (s: string) => (s.trim() === "" || !Number.isFinite(Number(s)) ? undefined : Number(s));

/** datasets.preview: utterances and hours per language and split after filters, and what each filter drops. */
function PreviewCard({ v, onFilter, onClose }: { v: DatasetVersion; onFilter: (f: DatasetPreviewRequest | undefined) => void; onClose: () => void }) {
  const [f, setF] = useState({ minDuration: "", maxDuration: "", minCps: "", maxCps: "", languages: "", origins: "" });
  const [busy, setBusy] = useState(false);
  const [res, setRes] = useState<DatasetPreview | null>(null);
  const [err, setErr] = useState<string | null>(null);
  const body: DatasetPreviewRequest = {
    version: v.id,
    ...(num(f.minDuration) !== undefined ? { minDuration: num(f.minDuration) } : {}),
    ...(num(f.maxDuration) !== undefined ? { maxDuration: num(f.maxDuration) } : {}),
    ...(num(f.minCps) !== undefined ? { minCharsPerSecond: num(f.minCps) } : {}),
    ...(num(f.maxCps) !== undefined ? { maxCharsPerSecond: num(f.maxCps) } : {}),
    ...(csv(f.languages).length ? { languages: csv(f.languages) } : {}),
    ...(csv(f.origins).length ? { origins: csv(f.origins) } : {}),
  };
  const run = async () => {
    setBusy(true);
    setErr(null);
    try {
      const out = await runCommand("datasets.preview", { body });
      if (out) {
        setRes(out);
        onFilter(body);
      }
    } catch (e) {
      setErr(errorMessage(e));
    } finally {
      setBusy(false);
    }
  };
  const field = (k: keyof typeof f, label: string, placeholder: string) => (
    <>
      <label htmlFor={`pv-${k}-${v.id}`} className="text-muted-foreground">
        {label}
      </label>
      <Input id={`pv-${k}-${v.id}`} className="h-6 text-xs" placeholder={placeholder} value={f[k]} onChange={(e) => setF({ ...f, [k]: e.target.value })} />
    </>
  );
  const dropped = res ? Object.entries(res.dropped).filter(([, c]) => c > 0) : [];
  return (
    <form
      className="flex flex-col gap-2 rounded-md border bg-tool p-2"
      aria-label="Preview with filters"
      data-slot="preview-card"
      onSubmit={(e) => {
        e.preventDefault();
        void run();
      }}
    >
      <p>Hours per language and split after filters. Nothing is written; the duration and characters-per-second bounds show on the charts below.</p>
      <div className="grid grid-cols-[10rem_1fr] items-center gap-1.5 @md:grid-cols-[10rem_1fr_10rem_1fr]">
        {field("minDuration", "Min duration (s)", "e.g. 0.5")}
        {field("maxDuration", "Max duration (s)", "e.g. 30")}
        {field("minCps", "Min chars/s", "e.g. 3")}
        {field("maxCps", "Max chars/s", "e.g. 25")}
        {field("languages", "Languages", "sr, hr")}
        {field("origins", "Origins", "human, pseudo-label")}
      </div>
      <div className="flex gap-1">
        <Button type="submit" size="xs" disabled={busy} data-command="datasets.preview">
          Preview
        </Button>
        <Button
          type="button"
          size="xs"
          variant="ghost"
          onClick={() => {
            onFilter(undefined);
            onClose();
          }}
        >
          Close
        </Button>
      </div>
      {err ? (
        <p role="alert" className="text-destructive">
          {err}
        </p>
      ) : null}
      {res ? (
        <div className="flex flex-col gap-1" data-slot="preview-result">
          <p role="status">
            Kept {res.utterances.toLocaleString()} utterances · {hours(res.hours)}
            {dropped.length ? ` · dropped ${dropped.map(([k, c]) => `${c.toLocaleString()} by ${k}`).join(", ")}` : " · nothing dropped"}.
          </p>
          <table className="w-full max-w-lg text-xs" aria-label="Kept per language and split">
            <thead className="text-left text-muted-foreground">
              <tr>
                <th className="font-normal">Language</th>
                <th className="font-normal">Split</th>
                <th className="font-normal">Utterances</th>
                <th className="font-normal">Hours</th>
                <th className="font-normal">Speakers</th>
              </tr>
            </thead>
            <tbody>
              {res.cells.map((c) => (
                <tr key={`${c.language}/${c.split}`} className="h-6 border-t">
                  <td>{c.language}</td>
                  <td>{c.split}</td>
                  <td className="tabular-nums">{c.utterances.toLocaleString()}</td>
                  <td className="tabular-nums">{c.hours.toFixed(2)}</td>
                  <td className="tabular-nums">{c.speakers ?? "—"}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      ) : null}
    </form>
  );
}

const CHECK_LABEL: Record<string, string> = {
  silence_share: "Silence share",
  clipping_share: "Clipped segments",
  length_outliers: "Length outliers",
};

function Quality({ d, id }: { d: DatasetPayload; id: string }) {
  return (
    <Section id={`ds-quality-${id}`} title="Quality checks" slot="dataset-quality">
      {d.quality ? (
        <>
          <p>{d.quality.passed ? "Every check passed." : "Some checks warn; warnings never block a freeze, and the card lists them."}</p>
          <table className="w-full max-w-lg text-xs" aria-label="Quality checks">
            <thead className="text-left text-muted-foreground">
              <tr>
                <th className="font-normal">Check</th>
                <th className="font-normal">Result</th>
                <th className="font-normal">Value</th>
                <th className="font-normal">Threshold</th>
              </tr>
            </thead>
            <tbody>
              {d.quality.checks.map((c) => (
                <tr key={c.name} className="h-6 border-t" title={c.message}>
                  <td>{CHECK_LABEL[c.name] ?? c.name}</td>
                  <td className={c.status === "pass" ? "" : "text-status-warning-foreground"}>
                    <span aria-hidden>{c.status === "pass" ? "✓ " : "⚠ "}</span>
                    {c.status}
                  </td>
                  <td className="tabular-nums">{(c.value * 100).toFixed(1)} %</td>
                  <td className="tabular-nums">{(c.threshold * 100).toFixed(1)} %</td>
                </tr>
              ))}
            </tbody>
          </table>
        </>
      ) : (
        <p className="text-muted-foreground">The quality checks run when the version is frozen (dataset_freeze); imported versions have none.</p>
      )}
      {d.card ? (
        <p data-slot="dataset-card">
          Dataset card: <code className="text-[11px]">{short(d.card.hash)}</code>
          {d.card.bytes ? ` (${bytes(d.card.bytes)})` : ""}, a Markdown file in the content store beside the shards.
        </p>
      ) : null}
    </Section>
  );
}

function Statistics({ d, id, filter }: { d: DatasetPayload; id: string; filter?: DatasetPreviewRequest }) {
  const charts = datasetCharts(d, filter);
  return (
    <Section id={`ds-stats-${id}`} title="Statistics" slot="dataset-statistics">
      {charts.length ? (
        <div className="grid gap-4 @lg:grid-cols-2">
          {charts.map((spec) => (
            <figure key={spec.title} className="flex min-w-0 flex-col gap-1" data-chart={spec.title}>
              <figcaption className="text-[11px] text-muted-foreground">{spec.title}</figcaption>
              <div className="h-48">
                <AnalyticsChart spec={spec} hideTitle />
              </div>
            </figure>
          ))}
        </div>
      ) : (
        <p className="text-muted-foreground">No statistics yet: dataset_freeze computes them (duration, characters per second, level, origins, channel roles).</p>
      )}
    </Section>
  );
}

function Shards({ d, id }: { d: DatasetPayload; id: string }) {
  const shards = d.shards ?? [];
  return (
    <Section id={`ds-shards-${id}`} title="Shards" slot="dataset-shards">
      {shards.length ? (
        <table className="w-full text-xs" aria-label="Shards">
          <thead className="text-left text-muted-foreground">
            <tr>
              <th className="w-10 font-normal">#</th>
              <th className="font-normal">Cuts manifest</th>
              <th className="font-normal">Utterances</th>
              <th className="font-normal">Hours</th>
              <th className="font-normal">Audio</th>
              <th className="font-normal">Location</th>
              <th className="font-normal">Pinned</th>
            </tr>
          </thead>
          <tbody>
            {shards.map((s) => (
              <tr key={s.index} className="h-6 border-t">
                <td className="tabular-nums">{s.index}</td>
                <td title={s.hash}>
                  <code className="text-[11px]">{short(s.hash)}</code>
                </td>
                <td className="tabular-nums">{s.utterances.toLocaleString()}</td>
                <td className="tabular-nums">{(s.seconds / 3600).toFixed(2)}</td>
                <td className="tabular-nums">{bytes(s.bytes)}</td>
                <td>{s.location}</td>
                <td>{s.pinned ? <span>✓ pinned</span> : <span className="text-muted-foreground">no</span>}</td>
              </tr>
            ))}
          </tbody>
        </table>
      ) : (
        <p className="text-muted-foreground">
          {d.frozen === false ? "A draft has no shards: its audio is still on the mount (segments " + (d.segments ? short(d.segments.hash) : "—") + ")." : "Stored as one dataset artifact" + (d.artifact ? ` (${short(d.artifact.hash)})` : "") + "."}
        </p>
      )}
    </Section>
  );
}

/**
 * projects.adopt with its checks (licence, locale, and for a golden set the leakage): the dry run asks first; a
 * locale refusal offers adopting it as replay (another language kept to limit forgetting).
 */
function AdoptCard({ v, project, adopted, onClose }: { v: DatasetVersion; project: string; adopted: boolean; onClose: () => void }) {
  const qc = useQueryClient();
  const [busy, setBusy] = useState(false);
  const [checked, setChecked] = useState(false);
  const [done, setDone] = useState(false);
  const [purpose, setPurpose] = useState<"target" | "replay">("target");
  const [problem, setProblem] = useState<{ text: string; problem?: Problem } | null>(null);
  const act = async (dryRun: boolean, as: "target" | "replay" = purpose) => {
    setBusy(true);
    setProblem(null);
    try {
      await runCommand("projects.adopt", { project, version: v.id, dryRun, ...(as === "replay" ? { purpose: as } : {}) });
      setPurpose(as);
      if (dryRun) setChecked(true);
      else {
        setDone(true);
        void qc.invalidateQueries({ queryKey: datasetsGetQueryKey({ path: { id: v.id } }) });
      }
    } catch (err) {
      setChecked(false);
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
  const locale = problem?.problem?.type.endsWith("/locale-mismatch");
  return (
    <div className="flex flex-col gap-1.5 rounded-md border bg-tool p-2" role="group" aria-label="Adopt into project" data-slot="adopt-card">
      <p>
        Adopt <span className="font-medium">{v.name}</span> {v.version} into <span className="font-medium">{project}</span>
        {purpose === "replay" ? " as replay" : ""}: its mixes and pipelines may use it, and data.lock on main lists it.
      </p>
      {checked && !done && !problem ? <p role="status">Checked: its licence and languages allow it.</p> : null}
      {done ? (
        <p role="status" className="text-status-done-foreground">
          Adopted{purpose === "replay" ? " as replay" : ""}.
        </p>
      ) : null}
      {problem ? (
        <div data-slot="adopt-problem">
          <ProblemList problem={problem.problem} text={problem.text} />
        </div>
      ) : null}
      <div className="flex gap-1">
        <Button size="xs" disabled={busy || adopted || done || !checked} onClick={() => void act(false)} data-command="projects.adopt">
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

function Details({ v }: { v: DatasetVersion }) {
  const d = v.dataset;
  return (
    <dl className="grid grid-cols-[10rem_1fr] gap-x-4 gap-y-2 p-4 text-xs">
      <Row label="ID">
        <code className="font-mono text-[11px]">{v.id}</code>
      </Row>
      <Row label="Collection">
        {v.name} <span className="text-muted-foreground">({v.collectionId})</span>
      </Row>
      <Row label="Version">{v.version}</Row>
      <Row label="State">{v.state}</Row>
      <Row label="Tags">{v.tags.join(", ") || "—"}</Row>
      <Row label="Licence">{v.licence || "—"}</Row>
      <Row label="Fingerprint">
        <code className="text-[11px]">{v.fingerprint}</code>
      </Row>
      {d.contentFingerprint ? (
        <Row label="Content fingerprint">
          <code className="text-[11px]">{d.contentFingerprint}</code>
        </Row>
      ) : null}
      {d.artifact ? (
        <Row label="Artifact">
          <code className="text-[11px]">{d.artifact.hash}</code>
        </Row>
      ) : null}
      {d.segments ? (
        <Row label="Segments">
          <code className="text-[11px]">{d.segments.hash}</code>
        </Row>
      ) : null}
      {d.lineage?.pipelineRunId ? <Row label="Registered by">{d.lineage.pipelineRunId}</Row> : null}
      <Row label="Registered">
        <ActorBadge actor={v.actor} /> {new Date(v.createdAt).toLocaleString()}
      </Row>
    </dl>
  );
}
