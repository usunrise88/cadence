import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { registrySearchOptions } from "@/api/gen/@tanstack/react-query.gen";
import type { RegistryKind } from "@/api/gen/types.gen";
import { Input } from "@/components/ui/input";
import { cn } from "@/lib/utils";
import { EmptyState, EntityList, type ListRow } from "@/shell/entity/primitives";
import { useProject, useSelection, useTopic, type PanelProps } from "@/shell/panel";

// Library: browses the Cadence-wide registry with a this-project / all filter (docs/spec/11-ui-panels.md).
// Registry kinds register in phase 1; until then the list is empty and says what comes next.

export function LibraryEmpty() {
  return (
    <EmptyState
      step="prepare"
      title="The registry is empty"
      hint="Sources, dataset versions, golden sets and models appear here once registered. Registration arrives with the agent loop (phase 1)."
    />
  );
}

export function LibraryPanel(_props: PanelProps) {
  const project = useProject();
  const [scope, setScope] = useState<"project" | "all">("all");
  const [q, setQ] = useState("");
  const [kind, setKind] = useState<RegistryKind | undefined>(undefined);
  const query = registrySearchOptions({ query: { q: q || undefined, kind, project: scope === "project" ? project : undefined, limit: 500 } });
  const { data, refetch } = useQuery(query);
  useTopic(["entity.base_model.*", "entity.dataset_version.*", "entity.template.*"], () => void refetch());
  const rows: ListRow[] = (data?.items ?? []).map((r) => ({ id: `${r.kind}:${r.id}`, name: r.name, version: r.version, state: r.state, tags: r.tags, actor: r.actor, updatedAt: r.updatedAt }));
  const select = useSelection((s) => s.select);
  return (
    <div className="flex h-full min-h-0 flex-col">
      <div className="flex shrink-0 flex-col gap-2 border-b p-2">
        <Input value={q} onChange={(e) => setQ(e.target.value)} placeholder="Filter — text, kind:, tag:, locale:" aria-label="Filter the library" />
        <div className="flex flex-wrap items-center gap-1 text-xs">
          <div role="radiogroup" aria-label="Scope" className="inline-flex rounded-md border bg-background p-0.5">
            {(["project", "all"] as const).map((s) => (
              <button
                key={s}
                role="radio"
                aria-checked={scope === s}
                type="button"
                onClick={() => setScope(s)}
                className={cn("h-6 rounded-[4px] px-2.5", scope === s ? "bg-selected font-medium text-foreground" : "text-muted-foreground hover:text-foreground")}
              >
                {s === "project" ? "This project" : "All"}
              </button>
            ))}
          </div>
          {(data?.kinds ?? []).map((k) => (
            <button
              key={k.kind}
              type="button"
              aria-pressed={kind === k.kind}
              onClick={() => setKind(kind === k.kind ? undefined : k.kind)}
              className={cn("h-6 rounded-full border px-2", kind === k.kind ? "border-accent-line bg-accent-soft text-accent-text" : "text-muted-foreground hover:bg-hover")}
            >
              {k.kind} {k.count}
            </button>
          ))}
        </div>
      </div>
      <div className="min-h-0 flex-1">
        {rows.length === 0 ? (
          <LibraryEmpty />
        ) : (
          <EntityList rows={rows} label="Registry versions" onOpen={(r) => select(`registry:${r.id}`, undefined)} onPreview={(r) => select(`registry:${r.id}`, undefined)} />
        )}
      </div>
    </div>
  );
}
