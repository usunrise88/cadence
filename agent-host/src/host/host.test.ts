import assert from "node:assert/strict";
import { describe, test } from "node:test";
import type { ToolCallSnapshot } from "../drivers/types.ts";
import { FakeClock } from "./clock.ts";
import { UidPool } from "./isolation.ts";
import { RunawayDetector } from "./runaway.ts";
import { scanDiff } from "./scan.ts";
import { toolCall, Transcript } from "./transcript.ts";

describe("credential scan", () => {
  const diff = (line: string): string => ["+++ b/a.txt", "@@ -0,0 +3,1 @@", `+${line}`].join("\n");
  const cases: Array<[string, string | undefined]> = [
    [`x cst_${"b".repeat(52)}`, "cst_"],
    [`CADENCE_TOKEN=cdk_${"2".repeat(52)}`, "cdk_"],
    [`cah_${"q".repeat(52)}`, "cah_"],
    [`key: sk-ant-oat01-${"A".repeat(40)}`, "anthropic"],
    [`HF_TOKEN=hf_${"a".repeat(34)}`, "huggingface"],
    [`nvapi-${"x".repeat(40)}`, "ngc"],
    [`ghp_${"a".repeat(36)}`, "github"],
    ["AKIAABCDEFGHIJKLMNOP", "aws"],
    ["-----BEGIN OPENSSH PRIVATE KEY-----", "private-key"],
    ["the cst_ prefix names session tokens", undefined],
    ["sk-ant is Anthropic's prefix", undefined],
  ];
  for (const [line, kind] of cases) {
    test(`${kind ?? "nothing"} in ${line.slice(0, 24)}`, () => {
      const got = scanDiff(diff(line));
      assert.deepEqual(got, kind ? [{ path: "a.txt", kind, line: 3 }] : []);
    });
  }
  test("removed lines are not findings", () => {
    assert.deepEqual(scanDiff(["+++ b/a.txt", "@@ -1,1 +0,0 @@", `-cst_${"b".repeat(52)}`].join("\n")), []);
  });
});

describe("runaway detector", () => {
  test("three identical calls in a row, each call counted once", () => {
    const r = new RunawayDetector(3);
    assert.equal(r.observe("a", "mixes.get", { id: 1, x: [1] }), undefined);
    assert.equal(r.observe("a", "mixes.get", { id: 1, x: [1] }), undefined, "the same call id again is an update");
    assert.equal(r.observe("b", "mixes.get", { x: [1], id: 1 }), undefined, "key order does not matter");
    assert.equal(r.observe("c", "mixes.get", { id: 1, x: [1] }), 3);
  });
  test("a different call or a person's message starts over", () => {
    const r = new RunawayDetector(3);
    r.observe("a", "t", { q: 1 });
    r.observe("b", "t", { q: 1 });
    r.observe("c", "t", { q: 2 });
    assert.equal(r.observe("d", "t", { q: 2 }), undefined);
    r.reset();
    r.observe("e", "t", { q: 2 });
    assert.equal(r.observe("f", "t", { q: 2 }), undefined);
    assert.equal(r.observe("g", "t", undefined), undefined, "a call without arguments yet is not counted");
  });
});

describe("transcript", () => {
  const call = (over: Partial<ToolCallSnapshot>): ToolCallSnapshot => ({
    id: "t1", title: "cadence.mixes.edit", acpKind: "other", class: "mcp", status: "completed", diffs: [], locations: [], text: "",
    mcp: { server: "cadence", tool: "mixes.edit" }, ...over,
  });

  test("text blocks coalesce; a tool call closes them; the latest state per key is sent", () => {
    const t = new Transcript("/w");
    t.startTurn(1);
    for (const c of ["He", "llo"]) t.apply({ kind: "message", sessionId: "s", text: c, content: { type: "text", text: c } });
    t.apply({ kind: "thought", sessionId: "s", text: "hmm" });
    t.apply({ kind: "tool_call", sessionId: "s", call: call({ status: "in_progress" }) });
    t.apply({ kind: "tool_call", sessionId: "s", call: call({ status: "in_progress" }) });
    t.apply({ kind: "message", sessionId: "s", text: "Done", content: { type: "text", text: "Done" } });
    t.close();
    const got = t.take().map((e) => [e.key, e.kind, e.text ?? e.toolCall?.status, e.final]);
    assert.deepEqual(got, [
      ["t1:m1", "agent_message", "Hello", true],
      ["t1:th2", "thought", "hmm", true],
      ["tool:t1", "tool_call", "in_progress", undefined],
      ["t1:m3", "agent_message", "Done", true],
    ]);
    t.apply({ kind: "tool_call", sessionId: "s", call: call({ status: "in_progress" }) });
    assert.equal(t.pendingCount, 0, "an unchanged tool call is not sent again");
  });

  test("a failed report goes back in front", () => {
    const t = new Transcript();
    t.put({ key: "a", kind: "notice", text: "1" });
    const sent = t.take();
    t.put({ key: "b", kind: "notice", text: "2" });
    t.restore(sent);
    assert.deepEqual(t.take().map((e) => e.key), ["a", "b"]);
  });

  test("tool calls: relative paths, approval and job ids from the result, capped payloads", () => {
    const tc = toolCall(
      call({
        diffs: [{ path: "/w/pipelines/a.yaml", oldText: null, newText: "x" }],
        locations: ["/w/pipelines/a.yaml"],
        rawOutput: [{ type: "text", text: '{"status":202,"data":{"approvalId":"apr_01","jobId":"job_02"}}' }],
        rawInput: { body: "y".repeat(30_000) },
      }),
      "/w",
    );
    assert.equal(tc.operation, "mixes.edit");
    assert.deepEqual(tc.diffs, [{ path: "pipelines/a.yaml", newText: "x" }]);
    assert.deepEqual(tc.locations, ["pipelines/a.yaml"]);
    assert.equal(tc.approvalId, "apr_01");
    assert.equal(tc.jobId, "job_02");
    assert.deepEqual(Object.keys(tc.input as object), ["truncated", "preview"]);
  });
});

describe("clocks and users", () => {
  test("the fake clock fires due timers in order", () => {
    const c = new FakeClock();
    const fired: string[] = [];
    c.after(20, () => fired.push("b"));
    c.after(10, () => fired.push("a"));
    const t = c.after(15, () => fired.push("x"));
    t.cancel();
    c.advance(19);
    assert.deepEqual(fired, ["a"]);
    c.advance(1);
    assert.deepEqual(fired, ["a", "b"]);
  });

  test("the uid pool gives each live session its own user", () => {
    const p = new UidPool(20000, 2);
    assert.equal(p.acquire("a").uid, 20000);
    assert.equal(p.acquire("b").uid, 20001);
    assert.equal(p.acquire("a").uid, 20000);
    assert.throws(() => p.acquire("c"), /no free session user/);
    p.release("a");
    assert.equal(p.acquire("c").uid, 20000);
  });
});
