import { useMemo, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { Copy } from "iconoir-react";
import { evalsGetOptions, registryLineageOptions } from "@/api/gen/@tanstack/react-query.gen";
import type { LineageNode } from "@/api/gen/types.gen";
import { Button } from "@/components/ui/button";
import { NativeSelect } from "@/components/ui/native-select";
import { cn } from "@/lib/utils";
import { EmptyState, PanelToolbar } from "@/shell/entity/primitives";
import { errorMessage, evalIdOfDoc, focusPipelineRun, kindNoun, openDocument, openPanelById, openRef, useFollowedDoc, type PanelProps } from "@/shell/panel";
import { DEFAULT_LAYOUT, layoutLineage, lineageRootOf } from "./layout";

// Lineage (docs/spec/11-ui-panels.md "Panel catalogue", Lineage): the graph around the active document from
// registry.lineage, both ways — sources → dataset versions → mix → run → checkpoint → model version; golden set →
// dataset version and normalizer; "used by" for registry entries. An Eval report shows its subject's lineage. Nodes
// open their documents (or the Inspector for kinds without one); a list view gives every node with its id to copy.

export function LineageEmpty() {
  return <EmptyState step="record" title="No lineage to show" hint="Open a golden set, model, run, mix or eval; the graph follows the active document." />;
}

export function LineagePanel({ instanceId }: PanelProps) {
  const { doc } = useFollowedDoc(instanceId);
  const evalId = evalIdOfDoc(doc);
  // An eval is not a lineage node of its own: its subject is (a checkpoint, model or base model version).
  const ev = useQuery({ ...evalsGetOptions({ path: { id: evalId ?? "" } }), enabled: !!evalId });
  const root = evalId ? ev.data?.subject.id : lineageRootOf(doc);
  if (!root) return <LineageEmpty />;
  return <Graph key={root} root={root} />;
}

/** Opens a node: its document when its kind has one, the pipeline run in its panel, else the Inspector. */
function openNode(n: LineageNode): void {
  if (n.kind === "project") openDocument(`project:${n.label}`);
  else if (n.kind === "pipeline_run") {
    focusPipelineRun(n.id);
    openPanelById("pipeline-run");
  } else openRef(`${n.kind}:${n.id}`);
}

function Graph({ root }: { root: string }) {
  const [direction, setDirection] = useState<"both" | "upstream" | "downstream">("both");
  const [depth, setDepth] = useState(3);
  const [view, setView] = useState<"graph" | "list">("graph");
  const q = useQuery(registryLineageOptions({ path: { id: root }, query: { direction, depth } }));
  const layout = useMemo(() => (q.data ? layoutLineage(q.data) : undefined), [q.data]);
  return (
    <div className="flex h-full min-h-0 flex-col" data-slot="lineage" data-root={root}>
      <PanelToolbar>
        <NativeSelect aria-label="Direction" className="h-6 w-auto text-xs" value={direction} onChange={(e) => setDirection(e.target.value as typeof direction)}>
          <option value="both">Both ways</option>
          <option value="upstream">Built from</option>
          <option value="downstream">Used by</option>
        </NativeSelect>
        <NativeSelect aria-label="Depth" className="h-6 w-auto text-xs" value={String(depth)} onChange={(e) => setDepth(Number(e.target.value))}>
          {[1, 2, 3, 4, 5].map((d) => (
            <option key={d} value={d}>
              {d} hop{d > 1 ? "s" : ""}
            </option>
          ))}
        </NativeSelect>
        <div role="radiogroup" aria-label="View" className="ml-auto inline-flex rounded-md border bg-background p-0.5 text-xs">
          {(["graph", "list"] as const).map((v) => (
            <button key={v} type="button" role="radio" aria-checked={view === v} onClick={() => setView(v)} className={cn("h-6 rounded-[4px] px-2 capitalize", view === v ? "bg-selected font-medium" : "text-muted-foreground")}>
              {v}
            </button>
          ))}
        </div>
      </PanelToolbar>
      <div className="min-h-0 flex-1 overflow-auto">
        {q.isLoading ? <p className="p-3 text-xs text-muted-foreground">Loading…</p> : null}
        {q.error ? <p className="p-3 text-xs text-destructive">{errorMessage(q.error)}</p> : null}
        {q.data && layout ? (
          <>
            {q.data.truncated || q.data.hidden ? (
              <p className="px-3 pt-2 text-xs text-muted-foreground">
                {q.data.truncated ? "The graph is cut at the node limit. " : ""}
                {q.data.hidden ? `${q.data.hidden} node${q.data.hidden > 1 ? "s" : ""} in projects you cannot see are left out.` : ""}
              </p>
            ) : null}
            {view === "graph" ? <GraphView layout={layout} /> : <ListView nodes={layout.nodes} />}
          </>
        ) : null}
      </div>
    </div>
  );
}

function GraphView({ layout }: { layout: ReturnType<typeof layoutLineage> }) {
  const o = DEFAULT_LAYOUT;
  return (
    <div className="relative m-2" style={{ width: layout.width, height: layout.height }}>
      <svg aria-hidden className="pointer-events-none absolute inset-0 text-border" width={layout.width} height={layout.height}>
        {layout.edges.map((e) => (
          <path key={`${e.from}>${e.to}:${e.relation}`} d={e.path} fill="none" stroke="currentColor" strokeWidth={1.5}>
            <title>{e.relation}</title>
          </path>
        ))}
      </svg>
      <ul aria-label="Lineage graph, built from (left) to used by (right)">
        {layout.nodes.map((n) => (
          <li key={n.id} className="absolute" style={{ left: n.x, top: n.y, width: o.nodeWidth, height: o.nodeHeight }}>
            <button
              type="button"
              onClick={() => openNode(n)}
              title={`${n.id}${n.state ? ` · ${n.state}` : ""}`}
              data-node={n.id}
              data-kind={n.kind}
              aria-current={n.direction === "root" ? "true" : undefined}
              aria-label={`${kindNoun(n.kind)} ${n.label}${n.direction === "root" ? " (this)" : n.direction === "upstream" ? `, built from, ${n.distance} hop${n.distance > 1 ? "s" : ""}` : `, uses it, ${n.distance} hop${n.distance > 1 ? "s" : ""}`}`}
              className={cn(
                "flex h-full w-full flex-col justify-center rounded-md border bg-background px-2 text-left text-xs hover:bg-hover",
                n.direction === "root" && "border-2 border-accent-line bg-accent-soft",
              )}
            >
              <span className="truncate text-[10px] tracking-wide text-muted-foreground uppercase">{kindNoun(n.kind)}</span>
              <span className="truncate font-medium">{n.label}</span>
            </button>
          </li>
        ))}
      </ul>
    </div>
  );
}

function ListView({ nodes }: { nodes: LineageNode[] }) {
  const [copied, setCopied] = useState<string | null>(null);
  const copy = async (el: HTMLElement, id: string) => {
    try {
      await (el.ownerDocument.defaultView ?? window).navigator.clipboard.writeText(id);
      setCopied(id);
    } catch {
      setCopied(null);
    }
  };
  return (
    <table className="w-full text-xs" aria-label="Lineage nodes">
      <thead className="text-left text-muted-foreground">
        <tr>
          <th className="px-2 font-normal">Where</th>
          <th className="font-normal">Kind</th>
          <th className="font-normal">Entity</th>
          <th className="font-normal">State</th>
          <th className="w-20 font-normal">
            <span className="sr-only">Copy id</span>
          </th>
        </tr>
      </thead>
      <tbody>
        {nodes.map((n) => (
          <tr key={n.id} className="h-7 border-t">
            <td className="px-2 text-muted-foreground">{n.direction === "root" ? "this" : `${n.direction === "upstream" ? "built from" : "used by"} · ${n.distance}`}</td>
            <td>{kindNoun(n.kind)}</td>
            <td className="max-w-60 truncate">
              <button type="button" className="underline-offset-2 hover:underline" onClick={() => openNode(n)}>
                {n.label}
              </button>
            </td>
            <td className="text-muted-foreground">{n.state ?? ""}</td>
            <td>
              <Button size="xs" variant="ghost" onClick={(e) => void copy(e.currentTarget, n.id)} aria-label={`Copy ${n.id}`}>
                <Copy aria-hidden />
                {copied === n.id ? "Copied" : "Id"}
              </Button>
            </td>
          </tr>
        ))}
      </tbody>
    </table>
  );
}
