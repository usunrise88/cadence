import { useDeferredValue, useId, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { auditListOptions, projectsListOptions } from "@/api/gen/@tanstack/react-query.gen";
import type { AuditEntry } from "@/api/gen/types.gen";
import { operations } from "@/api/operations.gen";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { cn } from "@/lib/utils";
import { Field, SectionHeading, Table, Td, when } from "./ui";

// Audit (docs/spec/05-agents.md "Guardrails": every command with actor and causedBy, so any production change
// traces back to a person's approval). Read-only, newest first, filtered by actor, operation and project; older
// pages load on demand. The spec names no panel of its own, so the log is a Settings section (admin only).

type Filters = { actor: string; operation: string; project: string };

export function outcomeTone(o: string): string {
  if (o === "ok") return "text-status-done-foreground";
  if (o === "approval") return "text-status-warning-foreground";
  return "text-status-failed-foreground";
}

export function AuditSection() {
  const uid = useId();
  const [filters, setFilters] = useState<Filters>({ actor: "", operation: "", project: "" });
  const deferred = useDeferredValue(filters);
  const [pages, setPages] = useState<(string | undefined)[]>([undefined]);
  const projects = useQuery(projectsListOptions());
  const slugs = new Map((projects.data?.items ?? []).map((p) => [p.id, p.slug]));
  const set = (k: keyof Filters, v: string) => {
    setFilters((f) => ({ ...f, [k]: v }));
    setPages([undefined]);
  };
  return (
    <section aria-labelledby="settings-audit" className="flex flex-col gap-3">
      <SectionHeading id="settings-audit" title="Audit log" hint="Every command, denial and failed attempt with its actor and cause. Kept one year." />
      <div className="grid gap-2 @md:grid-cols-3" role="search" aria-label="Filter the audit log">
        <Field label="Actor id" htmlFor={`${uid}-actor`}>
          <Input id={`${uid}-actor`} value={filters.actor} onChange={(e) => set("actor", e.target.value.trim())} placeholder="usr_admin, crd_…" className="h-7 text-xs" />
        </Field>
        <Field label="Operation" htmlFor={`${uid}-op`}>
          <Input id={`${uid}-op`} list={`${uid}-ops`} value={filters.operation} onChange={(e) => set("operation", e.target.value.trim())} placeholder="approvals.approve" className="h-7 text-xs" />
          <datalist id={`${uid}-ops`}>
            {Object.keys(operations).map((o) => (
              <option key={o} value={o} />
            ))}
          </datalist>
        </Field>
        <Field label="Project" htmlFor={`${uid}-project`}>
          <select id={`${uid}-project`} value={filters.project} onChange={(e) => set("project", e.target.value)} className="h-7 rounded-md border border-input bg-background px-2 text-xs">
            <option value="">all</option>
            {(projects.data?.items ?? []).map((p) => (
              <option key={p.id} value={p.slug}>
                {p.slug}
              </option>
            ))}
          </select>
        </Field>
      </div>
      <Table label="Audit entries" head={["When", "Operation", "Actor", "Outcome", "Status", "Rule", "Project", "Caused by"]}>
        {pages.map((before, i) => (
          <AuditPage key={before ?? "first"} filters={deferred} before={before} last={i === pages.length - 1} onMore={(next) => setPages((p) => [...p, next])} slugs={slugs} />
        ))}
      </Table>
    </section>
  );
}

function AuditPage({ filters, before, last, onMore, slugs }: { filters: Filters; before?: string; last: boolean; onMore: (next: string) => void; slugs: Map<string, string> }) {
  const q = useQuery(
    auditListOptions({
      query: {
        ...(filters.actor ? { actor: filters.actor } : {}),
        ...(filters.operation ? { operation: filters.operation } : {}),
        ...(filters.project ? { project: filters.project } : {}),
        ...(before ? { before } : {}),
        limit: 100,
      },
    }),
  );
  const items = q.data?.items ?? [];
  return (
    <>
      {items.map((e) => (
        <AuditRow key={e.id} e={e} project={e.projectId ? (slugs.get(e.projectId) ?? e.projectId) : undefined} />
      ))}
      {last ? (
        <tr>
          <td colSpan={8} className="px-2 py-1.5 text-muted-foreground">
            {q.isLoading ? "Loading…" : q.error ? "Could not load the audit log." : items.length === 0 && !before ? "No entries match." : null}
            {q.data?.next ? (
              <Button size="xs" variant="outline" onClick={() => onMore(q.data!.next!)}>
                Load older
              </Button>
            ) : null}
          </td>
        </tr>
      ) : null}
    </>
  );
}

function AuditRow({ e, project }: { e: AuditEntry; project?: string }) {
  const cause = e.causedBy;
  return (
    <tr>
      <Td className="tabular-nums whitespace-nowrap">{when(e.at)}</Td>
      <Td className="font-mono whitespace-nowrap">{e.operation}</Td>
      <Td title={e.actor.id}>
        {e.actor.kind === "user" ? "" : `${e.actor.kind} · `}
        {e.actor.name ?? e.actor.id}
      </Td>
      <Td className={cn("font-medium", outcomeTone(e.outcome))}>{e.outcome}</Td>
      <Td className="tabular-nums">{e.status}</Td>
      <Td className="font-mono whitespace-nowrap">{e.rule ?? "—"}</Td>
      <Td className="font-mono whitespace-nowrap" title={e.projectId}>
        {project ?? "—"}
      </Td>
      <Td className="font-mono">{cause?.approvalId ? `approval ${cause.approvalId}` : cause?.toolCallId ? `tool call ${cause.toolCallId}` : "—"}</Td>
    </tr>
  );
}
