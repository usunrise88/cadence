import { describe, expect, it } from "vitest";
import { sessionTransition } from "./live";
import { session } from "./testdata";

describe("what agent sessions tell the live region (WCAG 4.1.3)", () => {
  it("announces the finished turn with the reply, not the streaming one", () => {
    const busy = session({ busy: true, rev: 4 });
    expect(sessionTransition(session({ busy: false, rev: 3 }), busy)).toEqual({}); // turn started: nothing
    expect(sessionTransition(busy, session({ busy: true, rev: 5 }))).toEqual({}); // mid-turn update: nothing
    expect(sessionTransition(busy, session({ busy: false, rev: 6 }), "Done: the mix has two groups.")).toEqual({
      announce: "claude-code · session 3 finished its turn: Done: the mix has two groups.",
    });
  });

  it("names pauses with their reason and failures, and notes them in the history", () => {
    const t = sessionTransition(session({ rev: 1 }), session({ rev: 2, state: "paused", pauseReason: { code: "budget_turns", message: "40 turns used" } }));
    expect(t.announce).toBe("claude-code · session 3 paused: 40 turns used");
    expect(t.notice).toMatchObject({ level: "warning", title: "claude-code · session 3 paused", detail: "40 turns used" });
    expect(sessionTransition(session({ rev: 1 }), session({ rev: 2, state: "failed", error: "driver crashed" })).notice?.level).toBe("error");
  });

  it("says the agent host is restarting instead of a finished turn", () => {
    const busy = session({ busy: true, rev: 4, hostState: "connected" });
    expect(sessionTransition(busy, session({ busy: false, rev: 5, hostState: "released" }))).toEqual({
      announce: "claude-code · session 3: the agent host is restarting — reconnecting",
    });
    expect(sessionTransition(session({ rev: 5, hostState: "connected" }), session({ rev: 6, hostState: "lost" })).announce).toBe(
      "claude-code · session 3: the agent host stopped answering",
    );
    expect(sessionTransition(session({ rev: 6, hostState: "released" }), session({ rev: 7, hostState: "connected" }))).toEqual({});
  });

  it("says when an ended session leaves changes to accept, and ignores stale revisions", () => {
    const ended = session({ rev: 9, state: "done", merge: { state: "pending", head: "abc" } });
    const t = sessionTransition(session({ rev: 8 }), ended);
    expect(t.announce).toContain("its changes wait for you");
    expect(t.notice?.title).toBe("claude-code · session 3 ended with session changes");
    expect(sessionTransition(ended, session({ rev: 8 }))).toEqual({});
  });
});
