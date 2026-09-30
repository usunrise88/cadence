import { afterEach, describe, expect, it } from "vitest";
import { isNews, noteNews, useUnread, viewSession } from "./unread";
import { session } from "./testdata";

afterEach(() => useUnread.setState({ unread: {} }));

describe("unread Chats", () => {
  it("news: a finished turn or a session that wants attention; not token counts or older revisions", () => {
    const busy = session({ rev: 2, busy: true });
    expect(isNews(busy, session({ rev: 3, busy: false }))).toBe(true);
    expect(isNews(session({ rev: 2 }), session({ rev: 3, state: "waiting_approval" }))).toBe(true);
    expect(isNews(session({ rev: 2 }), session({ rev: 3, state: "failed" }))).toBe(true);
    expect(isNews(session({ rev: 2 }), session({ rev: 3, state: "cancelled" }))).toBe(false);
    expect(isNews(busy, session({ rev: 3, busy: true }))).toBe(false);
    expect(isNews(busy, session({ rev: 2, busy: false }))).toBe(false);
    expect(isNews(undefined, session())).toBe(false);
    expect(isNews(session({ rev: 2 }), session({ rev: 3, state: "paused", pauseReason: { code: "idle", message: "no message for 30 min" } }))).toBe(false); // asleep
    expect(isNews(session({ rev: 2 }), session({ rev: 3, state: "paused", pauseReason: { code: "runaway", message: "same call 3 times" } }))).toBe(true);
  });

  it("marks a session unread only while no visible Chat shows it; showing it reads it", () => {
    noteNews("ses_1");
    expect(useUnread.getState().unread.ses_1).toBe(true);
    const release = viewSession("ses_1");
    expect(useUnread.getState().unread.ses_1).toBeUndefined();
    noteNews("ses_1");
    expect(useUnread.getState().unread.ses_1).toBeUndefined();
    release();
    noteNews("ses_1");
    expect(useUnread.getState().unread.ses_1).toBe(true);
  });
});
