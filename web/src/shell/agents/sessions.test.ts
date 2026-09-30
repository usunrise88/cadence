import { describe, expect, it, vi } from "vitest";
import { QueryClient } from "@tanstack/react-query";
import { agentSessionsGetQueryKey, agentSessionsListQueryKey } from "@/api/gen/@tanstack/react-query.gen";
import type { AgentSession, AgentSessionList, CadenceEvent } from "@/api/gen/types.gen";
import { knownSession } from "./labels";
import { mergeMessages, mergeSessions, patchAgentBatch, transcriptKey, type Transcript } from "./sessions";
import { message, session } from "./testdata";

let evSeq = 1000;
const ev = (type: string, payload: object, topic = "agent.session.ses_1"): CadenceEvent => ({ seq: evSeq++, topic, type, actor: { kind: "agent", id: "crd_1" }, payload }) as CadenceEvent;

describe("transcript merge", () => {
  it("inserts new entries in seq order and replaces older revisions in place", () => {
    const a = message({ seq: 1, id: "a" });
    const b = message({ seq: 2, id: "b", text: "Hel", final: false });
    const list = [a, b];
    const out = mergeMessages(list, [{ ...b, rev: 2, text: "Hello" }, message({ seq: 4, id: "d" }), message({ seq: 3, id: "c" })]);
    expect(out.map((m) => m.id)).toEqual(["a", "b", "c", "d"]);
    expect(out[1]!.text).toBe("Hello");
    expect(out[0]).toBe(a); // untouched entries keep their identity (memoised rows do not re-render)
  });

  it("ignores stale revisions and returns the same array when nothing changed", () => {
    const b = message({ id: "b", rev: 3, text: "new" });
    const list = [b];
    expect(mergeMessages(list, [{ ...b, rev: 2, text: "old" }])).toBe(list);
    expect(mergeMessages(list, [])).toBe(list);
  });

  it("coalesces a burst of streamed updates of one entry to the newest", () => {
    const base = message({ id: "s", seq: 7, text: "", final: false });
    const burst = Array.from({ length: 50 }, (_, i) => ({ ...base, rev: i + 1, text: "x".repeat(i + 1), final: i === 49 }));
    const shuffled = [...burst.slice(25), ...burst.slice(0, 25)];
    const out = mergeMessages([], shuffled);
    expect(out).toHaveLength(1);
    expect(out[0]!.rev).toBe(50);
    expect(out[0]!.final).toBe(true);
  });

  it("patches the cache once per transcript per batch (one frame, one render)", () => {
    const qc = new QueryClient();
    const key = transcriptKey("ses_1");
    qc.setQueryData<Transcript>(key, { items: [message({ id: "m1", seq: 1 })] });
    const set = vi.spyOn(qc, "setQueryData");
    const streamed = message({ id: "m2", seq: 2, text: "", final: false });
    const batch = Array.from({ length: 30 }, (_, i) => ev("agent_message.updated", { message: { ...streamed, rev: i + 1, text: `tok${i}` } }));
    patchAgentBatch(qc, batch);
    expect(set.mock.calls.filter((c) => JSON.stringify(c[0]) === JSON.stringify(key))).toHaveLength(1);
    const t = qc.getQueryData<Transcript>(key)!;
    expect(t.items.map((m) => m.id)).toEqual(["m1", "m2"]);
    expect(t.items[1]!.text).toBe("tok29");
  });

  it("does not create a transcript nobody has loaded", () => {
    const qc = new QueryClient();
    patchAgentBatch(qc, [ev("agent_message.created", { message: message({ sessionId: "ses_9" }) })]);
    expect(qc.getQueryData(transcriptKey("ses_9"))).toBeUndefined();
  });
});

describe("session caches", () => {
  it("merges changed sessions into lists (new first) and the get cache, and names them for badges", () => {
    const qc = new QueryClient();
    const listKey = agentSessionsListQueryKey({ path: { p: "demo" }, query: { limit: 200 } });
    qc.setQueryData<AgentSessionList>(listKey, { items: [session({ id: "ses_1", rev: 1 })] });
    qc.setQueryData<AgentSession>(agentSessionsGetQueryKey({ path: { id: "ses_1" } }), session({ rev: 1 }));
    patchAgentBatch(qc, [
      ev("agent_session.changed", { session: session({ id: "ses_1", rev: 2, state: "paused" }) }, "agent.sessions"),
      ev("agent_session.created", { session: session({ id: "ses_2", number: 4, rev: 1 }) }, "agent.sessions"),
      ev("agent_session.created", { session: session({ id: "ses_x", project: "other", rev: 1 }) }, "agent.sessions"),
    ]);
    const list = qc.getQueryData<AgentSessionList>(listKey)!;
    expect(list.items.map((s) => [s.id, s.state])).toEqual([
      ["ses_2", "running"],
      ["ses_1", "paused"],
    ]);
    expect(qc.getQueryData<AgentSession>(agentSessionsGetQueryKey({ path: { id: "ses_1" } }))!.state).toBe("paused");
    expect(knownSession("ses_2")?.number).toBe(4);
  });

  it("keeps a list unchanged for stale revisions", () => {
    const list = { items: [session({ rev: 5 })] };
    expect(mergeSessions(list, [session({ rev: 4, state: "done" })])).toBe(list);
  });
});
