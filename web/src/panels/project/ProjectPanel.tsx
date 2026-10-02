import { useState, type FormEvent, type ReactNode } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Streamdown } from "streamdown";
import {
  adoptionsListOptions,
  agentProfileGetOptions,
  aliasesGetOptions,
  branchesListOptions,
  evalsListOptions,
  eventsListOptions,
  mixesListOptions,
  playbooksListOptions,
  projectsGetOptions,
  recipesGetOptions,
  runsListOptions,
} from "@/api/gen/@tanstack/react-query.gen";
import type { Project } from "@/api/gen/types.gen";
import { Button } from "@/components/ui/button";
import { Textarea } from "@/components/ui/textarea";
import { cn } from "@/lib/utils";
import { EmptyState, StatusChip } from "@/shell/entity/primitives";
import { estimateLine, GATE_CLASS, GATE_GLYPH, openDocument, openPanelById, PlaybookLauncher, runCommand, useTopic, type PanelProps } from "@/shell/panel";
import { evaluationSummary } from "./gate";
import { GateSection } from "./GateSection";

// The Project document is the home: the project's facts (locales, base model, repository, agent profile, budgets),
// the five blocks as a checklist with counts, gates and notes (docs/spec/11-ui-panels.md "Panel catalogue", Project).

type Block = { name: string; what: string; count?: number; detail?: ReactNode; phase?: number };

export function ProjectEmpty() {
  return <EmptyState step="prepare" title="No project selected" hint="Pick a project in the menu bar, or create one." />;
}

export function ProjectPanel({ tab, entity }: PanelProps) {
  const slug = entity?.id ?? "";
  const q = useQuery({ ...projectsGetOptions({ path: { p: slug } }), enabled: !!slug });
  if (!entity) return <ProjectEmpty />;
  const project = q.data;
  switch (tab) {
    case "details":
      return <Details entity={entity} />;
    case "activity":
      return <Activity projectId={String(entity.projectId ?? "")} />;
    case "lineage":
      return <EmptyState step="record" title="Lineage arrives with the registry" hint="Adopted versions and what the project produced will draw here." />;
    case "notes":
      return project ? <Notes project={project} /> : null;
    default:
      return project ? <Overview project={project} /> : null;
  }
}

function Heading({ id, children }: { id: string; children: ReactNode }) {
  return (
    <h3 id={id} className="mb-2 text-[11px] font-medium tracking-wide text-muted-foreground uppercase">
      {children}
    </h3>
  );
}

function Facts({ rows }: { rows: [string, ReactNode][] }) {
  return (
    <dl className="grid grid-cols-[8rem_1fr] gap-x-3 gap-y-1.5 text-xs">
      {rows.map(([k, v]) => (
        <div key={k} className="contents">
          <dt className="text-muted-foreground">{k}</dt>
          <dd className="min-w-0 break-all">{v ?? "—"}</dd>
        </div>
      ))}
    </dl>
  );
}

const sha = (s?: string) => (s ? <code className="tabular-nums">{s.slice(0, 12)}</code> : "—");

function Overview({ project }: { project: Project }) {
  const qc = useQueryClient();
  const ready = project.state === "active" && !project.archivedAt;
  const profile = useQuery({ ...agentProfileGetOptions({ path: { p: project.slug } }), enabled: ready });
  const branches = useQuery({ ...branchesListOptions({ path: { p: project.slug } }), enabled: ready });
  useTopic([`entity.project.${project.id}`], () => void qc.invalidateQueries({ queryKey: agentProfileGetOptions({ path: { p: project.slug } }).queryKey }));
  const repo = project.repository;
  const clone = repo?.cloneUrl ? new URL(repo.cloneUrl, window.location.origin).toString() : undefined;
  const bm = project.baseModel;
  const p = profile.data;
  return (
    <div className="@container">
      {project.state === "bootstrapping" ? (
        <p role="status" className="mx-4 mt-3 rounded-md border px-3 py-2 text-xs">
          <StatusChip state="bootstrapping" /> The bootstrap job is writing the repository, adopting the base model and creating the default workspaces.
        </p>
      ) : null}
      {project.state === "failed" ? (
        <p role="alert" className="mx-4 mt-3 rounded-md border border-destructive/40 px-3 py-2 text-xs text-destructive">
          The bootstrap failed: {project.bootstrapError ?? "no reason recorded"}. Check the job{project.bootstrapJobId ? ` ${project.bootstrapJobId}` : ""} and archive
          the project to start again.
        </p>
      ) : null}
      <div className="grid gap-6 p-4 @3xl:grid-cols-[3fr_2fr]">
        <div className="flex flex-col gap-6">
          <section aria-labelledby="project-facts">
            <Heading id="project-facts">Project</Heading>
            <Facts
              rows={[
                ["Locales", project.locales.join(", ") || "—"],
                ["Domain", project.domain || "—"],
                ["Base model", bm ? `${bm.hfRepo} @ ${bm.revision.slice(0, 12)}` : "—"],
                ["Registry version", bm ? `${bm.name} ${bm.version}` : "—"],
                ["Budgets", `${project.budgets.gpuHoursPerDay} GPU-h/day · ${project.budgets.agentTokensPerDay.toLocaleString()} agent tokens/day`],
              ]}
            />
          </section>
          <section aria-labelledby="project-repository">
            <div className="mb-2 flex items-center gap-2">
              <Heading id="project-repository">Repository</Heading>
              <Button size="xs" variant="outline" className="ml-auto" disabled={!ready} onClick={() => openDocument("recipe:project.yaml")}>
                Browse files
              </Button>
            </div>
            <Facts
              rows={[
                ["Kind", repo?.kind ?? "—"],
                ["Branch", repo?.branch ?? "main"],
                ["Head of main", sha(branches.data?.main)],
                ["Clone", clone ? <code className="select-all">{clone}</code> : "—"],
                ["Remote", repo?.remote ?? "—"],
                ...(repo?.pushError ? ([["Last push", <span className="text-destructive">{repo.pushError}</span>]] as [string, ReactNode][]) : []),
              ]}
            />
            {clone ? <p className="mt-1.5 text-[11px] text-muted-foreground">git clone with an API key (cdk_…) as the password; pushes to main appear here as recipe changes.</p> : null}
          </section>
          <Blocks project={project} ready={ready} />
        </div>
        <div className="flex flex-col gap-6">
          {ready ? <Playbooks project={project} /> : null}
          <section aria-labelledby="project-agent">
            <div className="mb-2 flex items-center gap-2">
              <Heading id="project-agent">Agent profile</Heading>
              <Button size="xs" variant="outline" className="ml-auto" onClick={() => openPanelById("agent-settings")}>
                Open Agent settings
              </Button>
            </div>
            {p ? (
              <Facts
                rows={[
                  ["Driver", p.driver === "claude-code" ? "Claude Code" : "opencode"],
                  ["Model", p.model],
                  ["Permission preset", p.permissionPreset],
                  ["Instructions", p.instructionsTemplate],
                  ["Session branches", p.autoMerge === "when-clean" ? "merge when clean" : "always wait for a person"],
                ]}
              />
            ) : (
              <p className="text-xs text-muted-foreground">{ready ? "Loading…" : "Available once the repository is bootstrapped."}</p>
            )}
          </section>
          <GateSection slug={project.slug} ready={ready} />
          <section aria-labelledby="decisions">
            <Heading id="decisions">Decisions and approvals</Heading>
            <p className="text-xs text-muted-foreground">Open approvals for this project appear in the Approvals panel.</p>
          </section>
          {project.description ? (
            <section aria-labelledby="about">
              <Heading id="about">About</Heading>
              <p className="text-[13px]">{project.description}</p>
            </section>
          ) : null}
        </div>
      </div>
    </div>
  );
}

// The five blocks with what the project holds in each (Data, Deployment and the flywheel show the phase they ship
// in until then); Evaluation reads the adopted golden sets, the evals with the newest verdict, and @baseline.
function Blocks({ project, ready }: { project: Project; ready: boolean }) {
  const p = { path: { p: project.slug } };
  const mixes = useQuery({ ...mixesListOptions(p), enabled: ready });
  const runs = useQuery({ ...runsListOptions({ ...p, query: { limit: 200 } }), enabled: ready });
  const golden = useQuery({ ...adoptionsListOptions({ ...p, query: { kind: "golden_set" } }), enabled: ready });
  const evals = useQuery({ ...evalsListOptions({ ...p, query: { limit: 200 } }), enabled: ready });
  const baseline = useQuery({ ...aliasesGetOptions({ path: { p: project.slug, name: "baseline" } }), enabled: ready, retry: false });
  useTopic(ready ? ["entity.mix.*", "entity.run.*", "entity.eval.*", `entity.project.${project.id}`] : null, () => {
    void mixes.refetch();
    void runs.refetch();
    void evals.refetch();
    void golden.refetch();
    void baseline.refetch();
  });
  const ev = evaluationSummary(golden.data?.items.length ?? 0, evals.data?.items ?? []);
  const b = baseline.data?.version;
  const blocks: Block[] = [
    { name: "Data", what: "Sources, dataset versions, golden sets", phase: 4 },
    {
      name: "Training",
      what: "Mixes, runs, checkpoints",
      count: runs.data?.items.length ?? 0,
      detail: `${mixes.data?.items.length ?? 0} mixes · ${runs.data?.items.length ?? 0} runs · ${(runs.data?.items ?? []).filter((r) => r.status === "done").length} finished`,
    },
    {
      name: "Evaluation",
      what: "Eval matrix, gates, baselines",
      count: ev.count,
      detail: (
        <>
          {ev.text}
          {ev.verdict ? (
            <span className={cn("ml-1", GATE_CLASS[ev.verdict])}>
              · <span aria-hidden>{GATE_GLYPH[ev.verdict]} </span>last gate {ev.verdict}
            </span>
          ) : null}
          {" · "}
          {b ? `@baseline ${b.name} ${b.version}` : "no @baseline (the base model)"}
        </>
      ),
    },
    { name: "Deployment", what: "Model versions, shadow, canary, production", phase: 5 },
    { name: "Flywheel", what: "Captured samples, triage, corrections", phase: 5 },
  ];
  return (
    <section aria-labelledby="blocks">
      <Heading id="blocks">The five blocks</Heading>
      <ul className="divide-y rounded-md border">
        {blocks.map((bl) => (
          <li key={bl.name} className="grid min-h-10 grid-cols-[6.5rem_1fr_auto] items-center gap-3 px-3 py-1.5" data-block={bl.name}>
            <span className="text-[13px] font-medium">{bl.name}</span>
            <span className="min-w-0 text-xs text-muted-foreground" title={bl.what}>
              {bl.detail ?? bl.what}
            </span>
            <span className="flex items-center gap-2 text-xs">
              {bl.count !== undefined ? <span className="tabular-nums">{bl.count}</span> : null}
              {bl.phase ? <span className="rounded-full border px-1.5 text-[11px] text-muted-foreground">phase {bl.phase}</span> : null}
            </span>
          </li>
        ))}
      </ul>
    </section>
  );
}

// The Project home's playbook button (docs/spec/10-ui-shell.md: playbooks on the Project home): the playbook this
// phase runs, with its estimate; the others are listed with the phase they run from.
function Playbooks({ project }: { project: Project }) {
  const list = useQuery(playbooksListOptions({ query: { project: project.slug } }));
  const [open, setOpen] = useState<string | null>(null);
  const items = list.data?.items ?? [];
  const next = items.find((p) => p.runnable);
  return (
    <section aria-labelledby="project-playbooks" data-slot="project-playbooks">
      <Heading id="project-playbooks">Playbooks</Heading>
      {list.isLoading ? <p className="text-xs text-muted-foreground">Loading…</p> : null}
      {next ? (
        <div className="flex flex-col gap-2 rounded-md border p-3">
          <div className="flex items-center gap-2">
            <span className="text-[13px] font-medium">{next.title}</span>
            <Button size="xs" className="ml-auto" onClick={() => setOpen(open === next.name ? null : next.name)} aria-expanded={open === next.name} data-command="playbooks.run">
              Start playbook
            </Button>
          </div>
          <p className="text-xs text-muted-foreground">
            {next.estimate ? `Estimate with defaults: ${estimateLine(next.estimate)}.` : next.estimateError}
          </p>
          {open === next.name ? <PlaybookLauncher project={project.slug} initial={next.name} onStarted={() => setOpen(null)} onCancel={() => setOpen(null)} /> : null}
        </div>
      ) : null}
      {items.some((p) => !p.runnable) ? (
        <ul className="mt-2 flex flex-col gap-1 text-xs text-muted-foreground">
          {items
            .filter((p) => !p.runnable)
            .map((p) => (
              <li key={p.name} className="flex items-center gap-2">
                <span className="truncate">{p.title}</span>
                <span className="ml-auto shrink-0 rounded-full border px-1.5 text-[11px]">phase {p.availableFrom}</span>
              </li>
            ))}
        </ul>
      ) : null}
    </section>
  );
}

function Notes({ project }: { project: Project }) {
  const qc = useQueryClient();
  const ready = project.state === "active" && !project.archivedAt;
  const opts = recipesGetOptions({ path: { p: project.slug, path: "NOTES.md" } });
  const notes = useQuery({ ...opts, enabled: ready, retry: false });
  useTopic(["recipe.*"], (batch) => {
    if (batch.some((e) => e.topic === "recipe.NOTES.md")) void qc.invalidateQueries({ queryKey: opts.queryKey });
  });
  const [text, setText] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const submit = async (e: FormEvent) => {
    e.preventDefault();
    setBusy(true);
    setError(null);
    try {
      await runCommand("projects.note", { project: project.slug, text, rev: project.rev });
      setText("");
      await qc.invalidateQueries({ queryKey: opts.queryKey });
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      setBusy(false);
    }
  };
  return (
    <div className="flex flex-col gap-4 p-4">
      {ready ? (
        <form onSubmit={submit} className="flex flex-col gap-2" aria-label="Add a note">
          <label className="flex flex-col gap-1 text-xs">
            <span className="text-muted-foreground">New note — one learning; filed under today's date in NOTES.md and committed to main</span>
            <Textarea value={text} onChange={(e) => setText(e.target.value)} rows={3} maxLength={4000} className="text-[13px]" name="note" />
          </label>
          {error ? (
            <p role="alert" className="text-xs text-destructive">
              {error}
            </p>
          ) : null}
          <div>
            <Button type="submit" size="sm" disabled={busy || !text.trim()}>
              Add note
            </Button>
          </div>
        </form>
      ) : null}
      {notes.data ? (
        <article className="prose-sm max-w-none text-[13px]" data-testid="project-notes">
          <Streamdown>{notes.data.encoding === "utf-8" ? notes.data.content : ""}</Streamdown>
        </article>
      ) : (
        <EmptyState step="record" title="No notes yet" hint="Notes are dated learnings committed to NOTES.md; every agent session reads them." />
      )}
    </div>
  );
}

function Details({ entity }: Pick<Required<PanelProps>, "entity">) {
  return (
    <dl className="grid grid-cols-[9rem_1fr] gap-x-4 gap-y-1.5 p-4 text-xs">
      {Object.entries(entity).map(([k, v]) => (
        <div key={k} className="contents">
          <dt className="text-muted-foreground">{k}</dt>
          <dd className="break-all">{k === "state" ? <StatusChip state={String(v)} /> : v === undefined ? "—" : String(v)}</dd>
        </div>
      ))}
    </dl>
  );
}

function Activity({ projectId }: { projectId: string }) {
  const topic = `entity.project.${projectId}`;
  const q = useQuery(eventsListOptions({ query: { topics: topic, limit: 50 } }));
  useTopic([topic], () => void q.refetch());
  const items = [...(q.data?.items ?? [])].reverse();
  if (items.length === 0) return <EmptyState step="record" title="No activity yet" />;
  return (
    <ol className="flex flex-col px-4 py-2 text-xs">
      {items.map((e) => (
        <li key={e.seq} className="flex h-8 items-center gap-3 border-b last:border-0">
          <time className="text-muted-foreground tabular-nums">{new Date(e.at).toLocaleString()}</time>
          <span className="font-medium">{e.type}</span>
          <span className="text-muted-foreground">{e.actor.name ?? e.actor.id}</span>
          {e.entity ? <span className="ml-auto text-muted-foreground">rev {e.entity.rev}</span> : null}
        </li>
      ))}
    </ol>
  );
}
