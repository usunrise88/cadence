import { describe, expect, it } from "vitest";
import type { AgentSession, Branch } from "@/api/gen/types.gen";
import { waitingBranches, waitingReason } from "./branches";

const b = (over: Partial<Branch>): Branch => ({ name: "x", kind: "other", head: "h", updatedAt: "2026-10-02T00:00:00Z", ahead: 1, behind: 0, ...over });
const s = (id: string, state: AgentSession["state"]) => ({ id, state }) as AgentSession;

describe("waiting branches", () => {
  it("lists syncs, other branches and ended sessions' branches ahead of main", () => {
    const list = [
      b({ name: "sync/2026-10-01", kind: "sync" }),
      b({ name: "session/ses_live", kind: "session", sessionId: "ses_live" }),
      b({ name: "session/ses_done", kind: "session", sessionId: "ses_done" }),
      b({ name: "session/ses_gone", kind: "session", sessionId: "ses_gone" }),
      b({ name: "merged", ahead: 0 }),
      b({ name: "experiment" }),
    ];
    const got = waitingBranches(list, [s("ses_live", "running"), s("ses_done", "done")]).map((x) => x.name);
    expect(got).toEqual(["sync/2026-10-01", "session/ses_done", "session/ses_gone", "experiment"]);
  });

  it("says why", () => {
    expect(waitingReason(b({ kind: "sync" }))).toBe("template sync");
    expect(waitingReason(b({ kind: "session" }))).toContain("session");
  });
});
