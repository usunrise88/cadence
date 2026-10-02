import type { Lineage, LineageNode } from "@/api/gen/types.gen";

// A layered layout of a registry.lineage graph (no graph library: a few dozen nodes in columns). Columns are the
// signed distance from the root — upstream (what it was built from) to the left, downstream (what uses it) to the
// right — so edges, which point from what was used to what used it, run left to right. Within a column nodes are
// ordered by the mean row of their neighbours in the column nearer the root (one barycentre sweep outwards), ties by
// kind then label, and short columns are centred.

export type LaidNode = LineageNode & { col: number; row: number; x: number; y: number };
export type LaidEdge = { from: string; to: string; relation: string; path: string };
export type LineageLayout = { nodes: LaidNode[]; edges: LaidEdge[]; width: number; height: number; columns: number };

export type LayoutOptions = { colWidth: number; rowHeight: number; nodeWidth: number; nodeHeight: number; pad: number };
export const DEFAULT_LAYOUT: LayoutOptions = { colWidth: 210, rowHeight: 52, nodeWidth: 176, nodeHeight: 40, pad: 8 };

/** The column of a node relative to the root: negative upstream, positive downstream. */
export function signedDistance(n: Pick<LineageNode, "direction" | "distance">): number {
  return n.direction === "upstream" ? -n.distance : n.direction === "downstream" ? n.distance : 0;
}

const byKindLabel = (a: LineageNode, b: LineageNode) => a.kind.localeCompare(b.kind) || a.label.localeCompare(b.label) || a.id.localeCompare(b.id);

export function layoutLineage(l: Pick<Lineage, "nodes" | "edges">, o: LayoutOptions = DEFAULT_LAYOUT): LineageLayout {
  const cols = new Map<number, LineageNode[]>();
  for (const n of l.nodes) {
    const c = signedDistance(n);
    const list = cols.get(c) ?? [];
    list.push(n);
    cols.set(c, list);
  }
  if (cols.size === 0) return { nodes: [], edges: [], width: 0, height: 0, columns: 0 };
  const neighbours = new Map<string, string[]>();
  for (const e of l.edges) {
    neighbours.set(e.from, [...(neighbours.get(e.from) ?? []), e.to]);
    neighbours.set(e.to, [...(neighbours.get(e.to) ?? []), e.from]);
  }
  const rowOf = new Map<string, number>();
  const keys = [...cols.keys()];
  const minCol = Math.min(...keys);
  const maxCol = Math.max(...keys);
  // Sweep outwards from the root's column: 0, then 1, −1, 2, −2, …
  const order = [...keys].sort((a, b) => Math.abs(a) - Math.abs(b) || b - a);
  for (const c of order) {
    const list = cols.get(c)!;
    const inner = c === 0 ? undefined : c > 0 ? c - 1 : c + 1;
    const bary = (n: LineageNode) => {
      if (inner === undefined) return Infinity;
      const rows = (neighbours.get(n.id) ?? []).filter((id) => cols.get(inner)?.some((m) => m.id === id)).map((id) => rowOf.get(id) ?? 0);
      return rows.length ? rows.reduce((s, r) => s + r, 0) / rows.length : Infinity;
    };
    const sorted = [...list].sort((a, b) => bary(a) - bary(b) || byKindLabel(a, b));
    sorted.forEach((n, i) => rowOf.set(n.id, i));
    cols.set(c, sorted);
  }
  const tallest = Math.max(...[...cols.values()].map((v) => v.length));
  const nodes: LaidNode[] = [];
  const at = new Map<string, LaidNode>();
  for (const [c, list] of cols) {
    const offset = (tallest - list.length) / 2;
    list.forEach((n, i) => {
      const col = c - minCol;
      const laid: LaidNode = { ...n, col, row: i, x: o.pad + col * o.colWidth, y: o.pad + (i + offset) * o.rowHeight };
      nodes.push(laid);
      at.set(n.id, laid);
    });
  }
  nodes.sort((a, b) => a.col - b.col || a.row - b.row);
  const edges: LaidEdge[] = [];
  for (const e of l.edges) {
    const a = at.get(e.from);
    const b = at.get(e.to);
    if (!a || !b) continue;
    const ay = a.y + o.nodeHeight / 2;
    const by = b.y + o.nodeHeight / 2;
    let path: string;
    if (a.col === b.col) {
      // Same column: a loop out of the right side.
      const x = a.x + o.nodeWidth;
      path = `M ${x} ${ay} C ${x + 24} ${ay}, ${x + 24} ${by}, ${x} ${by}`;
    } else {
      const [l2r, r2l] = a.col < b.col ? [a, b] : [b, a];
      const x1 = l2r.x + o.nodeWidth;
      const x2 = r2l.x;
      const y1 = l2r === a ? ay : by;
      const y2 = l2r === a ? by : ay;
      const mid = (x1 + x2) / 2;
      path = `M ${x1} ${y1} C ${mid} ${y1}, ${mid} ${y2}, ${x2} ${y2}`;
    }
    edges.push({ from: e.from, to: e.to, relation: e.relation, path });
  }
  return {
    nodes,
    edges,
    columns: maxCol - minCol + 1,
    width: o.pad * 2 + (maxCol - minCol) * o.colWidth + o.nodeWidth,
    height: o.pad * 2 + (tallest - 1) * o.rowHeight + o.nodeHeight,
  };
}

/** Id prefixes registry.lineage accepts as a root (its path parameter). */
const ROOT_PREFIXES = new Set(["ver", "src", "plr", "run", "ckp", "mix"]);

/** The lineage root a document reference names, if lineage knows its kind (`golden_set:ver_…` → `ver_…`). */
export function lineageRootOf(doc: string | null | undefined): string | undefined {
  if (!doc) return undefined;
  const i = doc.indexOf(":");
  if (i <= 0) return undefined;
  let id = doc.slice(i + 1);
  // Library previews of registry rows read registry:<kind>:<id>.
  if (doc.startsWith("registry:")) id = id.slice(id.indexOf(":") + 1);
  return ROOT_PREFIXES.has(id.slice(0, 3)) && id[3] === "_" ? id : undefined;
}
