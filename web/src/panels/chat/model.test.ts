import { describe, expect, it } from "vitest";
import type { AgentToolCall } from "@/api/gen/types.gen";
import { message, session } from "@/shell/agents/testdata";
import { budgetUse, compact, diffStat, dryRunEstimate, hostAway, lineDiff, rowOffsets, sessionStatus, toolDraft, toolEntityRef, toolOperation, visibleEntries, visibleRange } from "./model";

const tool = (over: Partial<AgentToolCall>): AgentToolCall => ({ id: "toolu_1", title: "mixes.edit", class: "mcp", status: "completed", ...over });

describe("header", () => {
  it("reads the state chip: running, waiting approval, paused with the reason", () => {
    expect(sessionStatus(session({ busy: true, turn: 2 }))).toMatchObject({ label: "running", tone: "running", detail: "Turn 2 in progress" });
    expect(sessionStatus(session({ state: "waiting_approval" })).label).toBe("waiting approval");
    expect(sessionStatus(session({ state: "paused", pauseReason: { code: "idle", message: "No message for 30 min" } }))).toMatchObject({ label: "paused", detail: "No message for 30 min" });
    expect(sessionStatus(session({ busy: true, pendingControl: "cancel" })).label).toBe("running · stopping…");
  });

  it("reads reconnecting while no agent host runs a live session", () => {
    expect(sessionStatus(session({ hostState: "released" }))).toEqual({ label: "reconnecting", tone: "warning", detail: "The agent host is restarting — reconnecting…" });
    expect(sessionStatus(session({ state: "waiting_approval", hostState: "lost", pendingControl: "pause" })).label).toBe("reconnecting · pausing…");
    expect(hostAway(session({ hostState: "lost" }))).toMatch(/stopped answering/);
    expect(hostAway(session({ hostState: "connected" }))).toBeUndefined();
    expect(hostAway(session({ state: "paused", hostState: "released" }))).toBeUndefined();
    expect(sessionStatus(session({ hostState: "connected", busy: true })).label).toBe("running");
  });

  it("meters turns and tokens against the budget", () => {
    const b = budgetUse(session({ use: { turns: 10, inputTokens: 300_000, outputTokens: 100_000 } }));
    expect(b.turns).toEqual({ used: 10, limit: 40, ratio: 0.25 });
    expect(b.tokens.ratio).toBe(1);
    expect(compact(1234)).toBe("1.2k");
    expect(compact(45_000)).toBe("45k");
  });

  it("hides turn starts; turn ends show usage", () => {
    const items = [message({ kind: "turn", turnInfo: { state: "started" } }), message({ kind: "agent_message" }), message({ kind: "turn", turnInfo: { state: "ended" } })];
    expect(visibleEntries(items).map((m) => m.kind)).toEqual(["agent_message", "turn"]);
  });
});

describe("tool-call cards", () => {
  it("names the Cadence operation and the entity it acted on", () => {
    const tc = tool({ input: { id: "mix_1", body: { temperature: 1 } }, output: { operation: "mixes.edit", status: 200, data: { draft: { id: "drf_1", rev: 2, entityId: "mix_1" } } } });
    expect(toolOperation(tc)).toBe("mixes.edit");
    expect(toolOperation(tool({ title: "mcp__cadence__runs.new" }))).toBe("runs.new");
    expect(toolEntityRef(tc)).toBe("@mix:mix_1");
    expect(toolDraft(tc)).toEqual({ id: "drf_1", rev: 2 });
    expect(toolEntityRef(tool({ title: "Read file", class: "read" }))).toBeUndefined();
  });

  it("shows a dry run's estimate inline", () => {
    const tc = tool({
      operation: "runs.new",
      input: { dryRun: true, body: {} },
      output: { status: 200, data: { basis: "table", plusMinus: 0.5, gpuHours: { value: 3, low: 1.5, high: 4.5 }, durationSeconds: { value: 7200, low: 1, high: 2 }, card: { host: "staging", index: 0 }, data: { hours: 12.5 } } },
    });
    expect(dryRunEstimate(tc)).toBe("Dry run: 3.0 GPU-h ±50% · ~2.0 h · staging card 0 · 12.5 h of audio");
    expect(dryRunEstimate(tool({ input: { id: "x" }, output: { status: 200, data: {} } }))).toBeUndefined();
  });

  it("diffs a file edit line by line and folds unchanged runs", () => {
    const old = ["a", "b", "c", "d", "e", "f", "g", "h", "i", "j"].join("\n");
    const neu = ["a", "b", "c", "d", "E", "f", "g", "h", "i", "j", "k"].join("\n");
    const d = lineDiff(old, neu, 1);
    expect(d).toEqual([
      { op: "…", count: 3 },
      { op: " ", text: "d" },
      { op: "-", text: "e" },
      { op: "+", text: "E" },
      { op: " ", text: "f" },
      { op: "…", count: 3 },
      { op: " ", text: "j" },
      { op: "+", text: "k" },
    ]);
    expect(diffStat(d)).toEqual({ added: 2, removed: 1 });
    expect(lineDiff(undefined, "x\ny")).toEqual([
      { op: "+", text: "x" },
      { op: "+", text: "y" },
    ]);
  });
});

describe("windowing", () => {
  it("renders only the rows in view (plus overscan) from measured and estimated heights", () => {
    const ids = Array.from({ length: 1000 }, (_, i) => `m${i}`);
    const offsets = rowOffsets(ids, new Map([["m0", 200]]), 50);
    expect(offsets[1]).toBe(200);
    expect(offsets[1000]).toBe(200 + 999 * 50);
    const r = visibleRange(offsets, 200 + 50 * 100, 500, 5);
    expect(r).toEqual({ first: 96, last: 116 });
    expect(visibleRange(offsets, 0, 500, 0)).toEqual({ first: 0, last: 7 });
  });
});
