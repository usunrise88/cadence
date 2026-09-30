import { beforeEach, describe, expect, it } from "vitest";
import type { SearchHit } from "@/api/gen/types.gen";
import { chipLabel, flattenGroups, hitToEntity, kindLabel, kindNoun, scopeOf, withScope } from "./hits";
import { useSearch } from "./store";

const hit = (over: Partial<SearchHit> = {}): SearchHit => ({
  kind: "dataset_version",
  id: "ver_1",
  title: "dataset/fleurs-he-smoke",
  ref: "dataset_version:ver_1",
  scope: "registry",
  tags: ["locale:he-IL", "domain:read"],
  updatedAt: "2026-09-30T10:00:00Z",
  ...over,
});

describe("search hits", () => {
  it.each([
    ["project", "Projects"],
    ["dataset_version", "Dataset versions"],
    ["help_article", "Help"],
    ["job", "Jobs"],
    ["golden_set", "Golden sets"],
    ["status", "Status"],
  ])("kindLabel(%s) = %s", (kind, label) => {
    expect(kindLabel(kind)).toBe(label);
  });

  it("names one row's kind", () => {
    expect(kindNoun("registry_collection")).toBe("registry collection");
  });

  it("turns a hit into what the Inspector shows", () => {
    const e = hitToEntity(
      hit({
        status: "frozen",
        project: "hebrew",
        numbers: { wer: 9.5 },
        actor: { kind: "agent", id: "ses_1" },
      }),
    );
    expect(e).toMatchObject({
      id: "ver_1",
      name: "dataset/fleurs-he-smoke",
      kind: "dataset_version",
      state: "frozen",
      project: "hebrew",
      tags: "locale:he-IL, domain:read",
      wer: 9.5,
    });
    expect(e.actor).toEqual({ kind: "agent", id: "ses_1" });
    expect(hitToEntity(hit({ tags: [] })).state).toBe("—");
    expect(hitToEntity(hit({ tags: [] }))).not.toHaveProperty("tags");
  });

  it("flattens groups in server order", () => {
    const a = hit({ ref: "project:a", kind: "project" });
    const b = hit();
    expect(
      flattenGroups([
        { kind: "project", items: [a] },
        { kind: "dataset_version", items: [b] },
      ]),
    ).toEqual([a, b]);
  });

  it.each([
    [{ field: "kind", op: ":", value: "job", raw: "kind:job" }, "kind: job"],
    [{ field: "wer", op: "<", value: "10", raw: "wer<10" }, "wer < 10"],
    [{ field: "dur", op: "..", value: "2..8", raw: "dur:2..8" }, "dur .. 2..8"],
  ] as const)("chipLabel %#", (q, want) => {
    expect(chipLabel({ ...q })).toBe(want);
  });

  it("reads and rewrites the scope qualifier", () => {
    expect(scopeOf("fleurs scope:all tag:x")).toBe("all");
    expect(scopeOf("fleurs")).toBeUndefined();
    expect(withScope("fleurs tag:x", "all")).toBe("fleurs tag:x scope:all");
    expect(withScope("fleurs scope:all tag:x", undefined)).toBe("fleurs tag:x");
    expect(withScope("scope:registry", "all")).toBe("scope:all");
    expect(withScope("", "all")).toBe("scope:all");
  });
});

describe("search store", () => {
  beforeEach(() => useSearch.setState({ libraryQuery: "", activeView: null, previews: {} }));

  it("carries the Library's query and saved view", () => {
    useSearch.getState().setLibraryQuery("kind:job", "Failing jobs");
    expect(useSearch.getState()).toMatchObject({
      libraryQuery: "kind:job",
      activeView: "Failing jobs",
    });
    useSearch.getState().setLibraryQuery("kind:job status:failed");
    expect(useSearch.getState().activeView).toBeNull();
  });

  it("remembers previews by document reference and keeps the newest", () => {
    const { remember } = useSearch.getState();
    remember([hit()]);
    expect(useSearch.getState().previews["dataset_version:ver_1"]?.name).toBe("dataset/fleurs-he-smoke");
    remember(Array.from({ length: 205 }, (_, i) => hit({ id: `ver_x${i}`, ref: `dataset_version:ver_x${i}` })));
    const keys = Object.keys(useSearch.getState().previews);
    expect(keys).toHaveLength(200);
    expect(keys).not.toContain("dataset_version:ver_1");
    expect(keys.at(-1)).toBe("dataset_version:ver_x204");
  });

  it("does not change state for an empty result", () => {
    const before = useSearch.getState();
    before.remember([]);
    expect(useSearch.getState()).toBe(before);
  });
});
