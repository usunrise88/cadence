import { useQuery } from "@tanstack/react-query";
import { eventsListOptions } from "@/api/gen/@tanstack/react-query.gen";
import { EmptyState, StatusChip } from "@/shell/entity/primitives";
import { useTopic, type PanelProps } from "@/shell/panel";

// The Project document is the home: the five blocks as a checklist with counts, the last decisions, open approvals
// and notes (docs/spec/10-ui-shell.md "Lists, compare, drafts, empty states"). In phase 0 the blocks are empty.

const BLOCKS = [
  { name: "Data", what: "Sources, dataset versions, golden sets", phase: 4 },
  { name: "Training", what: "Mixes, runs, checkpoints", phase: 2 },
  { name: "Evaluation", what: "Eval matrix, gates, baselines", phase: 3 },
  { name: "Deployment", what: "Model versions, shadow, canary, production", phase: 5 },
  { name: "Flywheel", what: "Captured samples, triage, corrections", phase: 5 },
];

export function ProjectEmpty() {
  return <EmptyState step="prepare" title="No project selected" hint="Pick a project in the menu bar, or create one." />;
}

export function ProjectPanel({ tab, entity }: PanelProps) {
  if (!entity) return <ProjectEmpty />;
  switch (tab) {
    case "details":
      return <Details entity={entity} />;
    case "activity":
      return <Activity projectId={String(entity.projectId ?? "")} />;
    case "lineage":
      return <EmptyState step="record" title="Lineage arrives with the registry" hint="Adopted versions and what the project produced will draw here (phase 1)." />;
    case "notes":
      return <EmptyState step="record" title="No notes yet" hint="Notes are dated learnings committed to the project repository (phase 1)." />;
    default:
      return (
        <div className="@container">
          <div className="grid gap-6 p-4 @3xl:grid-cols-[3fr_2fr]">
            <section aria-labelledby="blocks">
              <h3 id="blocks" className="mb-2 text-[11px] font-medium tracking-wide text-muted-foreground uppercase">
                The five blocks
              </h3>
              <ul className="divide-y rounded-md border">
                {BLOCKS.map((b) => (
                  <li key={b.name} className="grid h-10 grid-cols-[6.5rem_1fr_auto] items-center gap-3 px-3">
                    <span className="text-[13px] font-medium">{b.name}</span>
                    <span className="truncate text-xs text-muted-foreground" title={b.what}>
                      {b.what}
                    </span>
                    <span className="flex items-center gap-2 text-xs">
                      <span className="tabular-nums">0</span>
                      <span className="rounded-full border px-1.5 text-[11px] text-muted-foreground">phase {b.phase}</span>
                    </span>
                  </li>
                ))}
              </ul>
            </section>
            <section aria-labelledby="decisions" className="flex flex-col gap-4">
              <div>
                <h3 id="decisions" className="mb-2 text-[11px] font-medium tracking-wide text-muted-foreground uppercase">
                  Decisions and approvals
                </h3>
                <p className="text-xs text-muted-foreground">None yet — approvals arrive with the agent loop (phase 1).</p>
              </div>
              {entity.description ? (
                <div>
                  <h3 className="mb-2 text-[11px] font-medium tracking-wide text-muted-foreground uppercase">About</h3>
                  <p className="text-[13px]">{String(entity.description)}</p>
                </div>
              ) : null}
            </section>
          </div>
        </div>
      );
  }
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
