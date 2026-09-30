import { describe, expect, it } from "vitest";
import { QueryClient } from "@tanstack/react-query";
import { draftsListQueryKey, mixesGetQueryKey } from "@/api/gen/@tanstack/react-query.gen";
import type { CadenceEvent, Draft, DraftList, Mix } from "@/api/gen/types.gen";
import { patchMix } from "@/entities/mix";
import { activePresence, changed, patchDrafts } from "./drafts";

const agent = { kind: "agent" as const, id: "crd_1", sessionId: "ses_9" };

function draft(over: Partial<Draft>): Draft {
  return {
    id: "drf_1", projectId: "prj_1", entityKind: "mix", entityId: "mix_1", baseRev: 1, currentRev: 1, rev: 1, state: "open",
    content: {}, changes: [], author: agent, stale: false, createdAt: "", updatedAt: "", ...over,
  };
}

function ev(type: string, payload: Record<string, unknown>, rev = 1, seq = 1): CadenceEvent {
  return { seq, topic: "entity.mix.mix_1", type, actor: agent, at: "", entity: { kind: "mix", id: "mix_1", rev }, payload };
}

describe("draft cache patching", () => {
  const key = draftsListQueryKey({ query: { entityKind: "mix", entityId: "mix_1" } });

  it("upserts open drafts, drops decided ones and marks stale drafts", () => {
    const qc = new QueryClient();
    qc.setQueryData<DraftList>(key, { items: [] });
    patchDrafts(qc, [ev("draft.created", { draft: draft({}) })]);
    expect(qc.getQueryData<DraftList>(key)?.items.map((d) => d.rev)).toEqual([1]);
    patchDrafts(qc, [ev("draft.updated", { draft: draft({ rev: 2 }) })]);
    expect(qc.getQueryData<DraftList>(key)?.items.map((d) => d.rev)).toEqual([2]);
    patchDrafts(qc, [ev("mix.revised", { mix: {} }, 2)]);
    expect(qc.getQueryData<DraftList>(key)?.items[0]).toMatchObject({ stale: true, currentRev: 2 });
    patchDrafts(qc, [ev("draft.reverted", { draft: draft({ rev: 3, state: "reverted" }) }, 2)]);
    expect(qc.getQueryData<DraftList>(key)?.items).toEqual([]);
  });

  it("refetches a list it has not loaded instead of inventing one", () => {
    const qc = new QueryClient();
    patchDrafts(qc, [ev("draft.created", { draft: draft({}) })]);
    expect(qc.getQueryData(key)).toBeUndefined();
  });

  it("patches the mix from its revisions and presence", () => {
    const qc = new QueryClient();
    const mkey = mixesGetQueryKey({ path: { id: "mix_1" } });
    qc.setQueryData(mkey, { id: "mix_1", rev: 1, presence: [] } as unknown as Mix);
    patchMix(qc, [ev("presence.changed", { presence: [{ actor: agent, since: "", draftId: "drf_1" }] })], "mix_1");
    expect(qc.getQueryData<Mix>(mkey)?.presence).toHaveLength(1);
    patchMix(qc, [ev("mix.revised", { mix: { id: "mix_1", rev: 2, presence: [] } }, 2)], "mix_1");
    expect(qc.getQueryData<Mix>(mkey)).toMatchObject({ rev: 2, presence: [] });
  });
});

describe("presence and changes", () => {
  it("drops direct-edit presence once its window passed", () => {
    const now = Date.parse("2026-09-30T00:00:10Z");
    const ps = [
      { actor: agent, since: "2026-09-30T00:00:00Z", draftId: "drf_1" },
      { actor: agent, since: "2026-09-30T00:00:00Z", until: "2026-09-30T00:00:05Z" },
      { actor: agent, since: "2026-09-30T00:00:00Z", until: "2026-09-30T00:00:30Z" },
    ];
    expect(activePresence(ps, now)).toHaveLength(2);
  });

  it("matches a change at, above or below a path", () => {
    const changes = [{ path: "/groups/1" }, { path: "/temperature" }];
    expect(changed(changes, "/groups/1/weight")).toBe(true);
    expect(changed(changes, "/groups")).toBe(true);
    expect(changed(changes, "/groups/0/weight")).toBe(false);
    expect(changed(changes, "/temperature")).toBe(true);
  });
});
