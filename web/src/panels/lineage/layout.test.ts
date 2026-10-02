import { describe, expect, it } from "vitest";
import type { LineageNode } from "@/api/gen/types.gen";
import { DEFAULT_LAYOUT, layoutLineage, lineageRootOf, signedDistance } from "./layout";

const node = (id: string, kind: string, direction: LineageNode["direction"], distance: number, label = id): LineageNode => ({ id, kind, label, direction, distance });

// A model's lineage: dataset → mix → run → checkpoint → model (root) → project.
const graph = {
  nodes: [
    node("ver_model", "model", "root", 0),
    node("ckp_1", "checkpoint", "upstream", 1),
    node("ver_base", "base_model", "upstream", 1),
    node("run_1", "run", "upstream", 2),
    node("mix_1", "mix", "upstream", 3),
    node("ver_ds_b", "dataset_version", "upstream", 4, "b"),
    node("ver_ds_a", "dataset_version", "upstream", 4, "a"),
    node("prj_1", "project", "downstream", 1, "demo"),
  ],
  edges: [
    { from: "ckp_1", to: "ver_model", relation: "checkpointId" },
    { from: "ver_base", to: "ver_model", relation: "baseModelVersionId" },
    { from: "run_1", to: "ckp_1", relation: "run" },
    { from: "ver_base", to: "run_1", relation: "base" },
    { from: "mix_1", to: "run_1", relation: "mix" },
    { from: "ver_ds_a", to: "mix_1", relation: "mix" },
    { from: "ver_ds_b", to: "mix_1", relation: "mix" },
    { from: "ver_model", to: "prj_1", relation: "adopted" },
  ],
};

describe("lineage layout", () => {
  it("puts upstream left of the root and downstream right, one column per hop", () => {
    expect(signedDistance({ direction: "upstream", distance: 2 })).toBe(-2);
    expect(signedDistance({ direction: "downstream", distance: 1 })).toBe(1);
    const l = layoutLineage(graph);
    const col = (id: string) => l.nodes.find((n) => n.id === id)!.col;
    expect(l.columns).toBe(6);
    expect(col("ver_ds_a")).toBe(0);
    expect(col("mix_1")).toBe(1);
    expect(col("run_1")).toBe(2);
    expect(col("ckp_1")).toBe(3);
    expect(col("ver_model")).toBe(4);
    expect(col("prj_1")).toBe(5);
    expect(l.width).toBe(DEFAULT_LAYOUT.pad * 2 + 5 * DEFAULT_LAYOUT.colWidth + DEFAULT_LAYOUT.nodeWidth);
    // Two rows at most (checkpoint and base model), so the height fits two.
    expect(l.height).toBe(DEFAULT_LAYOUT.pad * 2 + DEFAULT_LAYOUT.rowHeight + DEFAULT_LAYOUT.nodeHeight);
  });

  it("orders a column by its neighbours nearer the root, then by kind and label, and centres short columns", () => {
    const l = layoutLineage(graph);
    const at = (id: string) => l.nodes.find((n) => n.id === id)!;
    // Same column, no ordering by neighbours: base_model sorts before checkpoint by kind.
    expect(at("ver_base").row).toBe(0);
    expect(at("ckp_1").row).toBe(1);
    expect(at("ver_ds_a").row).toBe(0);
    expect(at("ver_ds_b").row).toBe(1);
    // A single node in a two-row layout sits halfway.
    expect(at("ver_model").y).toBe(DEFAULT_LAYOUT.pad + 0.5 * DEFAULT_LAYOUT.rowHeight);
  });

  it("draws every edge between laid nodes left to right", () => {
    const l = layoutLineage(graph);
    expect(l.edges).toHaveLength(8);
    for (const e of l.edges) {
      const m = /^M (\S+) \S+ C .* (\S+) \S+$/.exec(e.path)!;
      expect(Number(m[1])).toBeLessThan(Number(m[2]));
    }
    expect(layoutLineage({ nodes: [], edges: [] })).toEqual({ nodes: [], edges: [], width: 0, height: 0, columns: 0 });
  });

  it("drops edges to nodes outside the walk and finds roots in documents", () => {
    expect(layoutLineage({ nodes: [node("ver_x", "model", "root", 0)], edges: [{ from: "ver_gone", to: "ver_x", relation: "x" }] }).edges).toHaveLength(0);
    expect(lineageRootOf("golden_set:ver_1")).toBe("ver_1");
    expect(lineageRootOf("run:run_1")).toBe("run_1");
    expect(lineageRootOf("registry:dataset_version:ver_2")).toBe("ver_2");
    expect(lineageRootOf("project:demo")).toBeUndefined();
    expect(lineageRootOf("language_pack:he-IL")).toBeUndefined();
    expect(lineageRootOf(null)).toBeUndefined();
  });
});
