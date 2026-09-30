import { mkdirSync, writeFileSync } from "node:fs";
import path from "node:path";
import type { Page, Request } from "@playwright/test";
import { McpAgent, mintAgentToken, openMix, projectWithMix } from "./agent";
import { expect, openWorkspace, test } from "./fixtures";

// Spike A4 (docs/spikes/A4-live-events.md): an agent edits a mix through MCP with a real agent token while the Mix
// panel is open. Measured: commit → visible draft (p50/p95 over ≥ 20 edits), resume of the event stream after the
// connection drops (no gap, no duplicate), the 412 path of a concurrent UI edit, and a burst of edits the panel
// absorbs. Results land in test-results/spikes/A4.json and in the spike's Result section.

test.use({ trace: "off" });
test.setTimeout(240_000);

const OUT = path.resolve(import.meta.dirname, "../test-results/spikes");

type Win = {
  __seen: Record<string, number>;
  __seqs: number[];
  __batches: number;
  __cadence: { events: { connectionState: string; subscribe(p: string[], h: (b: { seq: number }[]) => void, owner: string): () => void } };
};

function pct(xs: number[], p: number): number {
  const s = [...xs].sort((a, b) => a - b);
  return Math.round(s[Math.min(s.length - 1, Math.floor((p / 100) * s.length))]! * 10) / 10;
}

const stats = (xs: number[]) => ({ n: xs.length, p50: pct(xs, 50), p95: pct(xs, 95), max: Math.round(Math.max(...xs) * 10) / 10 });

/** Records, in the page, when each draft revision first shows (epoch ms) and every event seq of the mix's topic. */
async function installRecorder(page: Page, topic: string): Promise<void> {
  await page.evaluate((topic) => {
    const w = window as unknown as Win;
    w.__seen = {};
    w.__seqs = [];
    w.__batches = 0;
    const record = () => {
      for (const el of document.querySelectorAll('[data-panel="mix"] [data-slot="draft"]')) {
        const rev = el.getAttribute("data-draft-rev");
        if (rev && !(rev in w.__seen)) w.__seen[rev] = performance.timeOrigin + performance.now();
      }
    };
    new MutationObserver(record).observe(document.body, { subtree: true, childList: true, attributes: true, attributeFilter: ["data-draft-rev"] });
    w.__cadence.events.subscribe(
      [topic],
      (b) => {
        w.__batches++;
        for (const e of b) w.__seqs.push(e.seq);
      },
      "spike:a4",
    );
  }, topic);
}

async function seenAt(page: Page, rev: number): Promise<number> {
  await page.waitForFunction((rev) => rev in (window as unknown as Win).__seen, String(rev), { timeout: 20_000 });
  return page.evaluate((rev) => (window as unknown as Win).__seen[rev]!, String(rev));
}

type EditData = { draft: { id: string; rev: number; updatedAt: string } };

test("A4: agent edits through MCP reach the open Mix panel live, resume without loss, conflict on 412", async ({ page, request }) => {
  const { slug, mix } = await projectWithMix(request, "a4-mix");
  await openWorkspace(page, slug);
  await openMix(page, mix.name);
  const topic = `entity.mix.${mix.id}`;
  await installRecorder(page, topic);
  const agent = await McpAgent.connect(mintAgentToken(slug, "ses_4"), slug);
  const edit = async (i: number) => {
    const t0 = Date.now();
    const r = await agent.call("mixes.edit", { id: mix.id, ifMatch: '"1"', body: { temperature: 1 + i / 100 } }, `toolu_a4_${i}`);
    const t1 = Date.now();
    expect(r.isError, JSON.stringify(r.result)).toBe(false);
    return { t0, t1, draft: (r.result.data as EditData).draft };
  };

  // 1. Latency: commit → visible draft. The draft's updatedAt is the commit transaction's timestamp (Postgres now(),
  //    same host clock as the browser); send → visible and response → visible bound it from the agent's side.
  const WARM = 3;
  const EDITS = 30;
  const commit: number[] = [];
  const send: number[] = [];
  const response: number[] = [];
  for (let i = 1; i <= WARM + EDITS; i++) {
    const { t0, t1, draft } = await edit(i);
    const visible = await seenAt(page, draft.rev);
    if (i <= WARM) continue;
    commit.push(visible - Date.parse(draft.updatedAt));
    send.push(visible - t0);
    response.push(visible - t1);
  }

  // 2. Resume: the connection drops, the agent keeps editing, the client reconnects from the last seq it saw.
  //    Chromium's offline emulation (context.setOffline) leaves an open event stream alone, so the drop is made in
  //    the page: the shell's EventSource is closed and fails the way a dead connection does (readyState CLOSED),
  //    and the shell's own reconnect runs. The server's Last-Event-ID path (EventSource's native retry) is covered
  //    by TestEventStreamFilterAndResume in the control plane.
  const eventRequests: { url: string; lastEventId?: string; at: number }[] = [];
  const onRequest = (r: Request) => {
    if (r.url().includes("/api/events")) eventRequests.push({ url: r.url(), lastEventId: r.headers()["last-event-id"], at: Date.now() });
  };
  page.on("request", onRequest);
  const firstSeq = await page.evaluate(() => Math.min(...(window as unknown as Win).__seqs));
  const dropped = await page.evaluate(() => {
    const stream = (window as unknown as { __cadence: { events: { source: EventSource | null } } }).__cadence.events;
    const src = stream.source;
    if (!src) return false;
    src.close();
    src.onerror?.call(src, new Event("error"));
    return true;
  });
  let last = 0;
  const MISSED = 5;
  for (let i = 1; i <= MISSED; i++) last = (await edit(100 + i)).draft.rev;
  const lastCommit = Date.now();
  const visibleLast = await seenAt(page, last);
  const reconnectAt = eventRequests.at(-1)?.at ?? lastCommit;
  const recovered = visibleLast - reconnectAt;
  page.off("request", onRequest);
  const server = await request.get(`/api/events?topics=${topic}&after=${firstSeq - 1}&limit=1000`);
  const serverSeqs = ((await server.json()) as { items: { seq: number }[] }).items.map((e) => e.seq);
  const clientSeqs = await page.evaluate(() => (window as unknown as Win).__seqs);
  const missing = serverSeqs.filter((s) => !clientSeqs.includes(s));
  const duplicates = clientSeqs.length - new Set(clientSeqs).size;
  expect(missing, "events lost across the reconnect").toEqual([]);
  expect(duplicates).toBe(0);

  // 3. Burst: edits back to back; how fast they commit and how far the panel lags behind the last one.
  const BURST = 60;
  const batchesBefore = await page.evaluate(() => (window as unknown as Win).__batches);
  const burstStart = Date.now();
  let burstLast = 0;
  for (let i = 1; i <= BURST; i++) burstLast = (await edit(200 + i)).draft.rev;
  const burstEnd = Date.now();
  const burstLag = (await seenAt(page, burstLast)) - burstEnd;
  const batches = (await page.evaluate(() => (window as unknown as Win).__batches)) - batchesBefore;
  const burstSeconds = (burstEnd - burstStart) / 1000;

  // 4. Conflict: the person edits rev 1 while the agent's draft is accepted elsewhere; the save answers 412.
  const panel = page.locator('[data-panel="mix"]');
  const draftId = await panel.locator('[data-slot="draft"]').getAttribute("data-draft-id");
  const draftRev = await panel.locator('[data-slot="draft"]').getAttribute("data-draft-rev");
  const acc = await request.post(`/api/drafts/${draftId}:accept`, { headers: { "Idempotency-Key": `a4-accept-${slug}`, "If-Match": `"${draftRev}"` } });
  expect(acc.status()).toBe(200);
  await expect(panel.locator('[data-slot="draft"]')).toHaveCount(0);
  // The person edits rev 2; meanwhile someone else saves rev 3 (another tab); the person's save must not overwrite.
  await panel.getByLabel("Weight of target").fill("4");
  const other = await request.patch(`/api/mixes/${mix.id}`, { data: { replayShare: 0 }, headers: { "Idempotency-Key": `a4-other-${slug}`, "If-Match": '"2"' } });
  expect(other.status()).toBe(200);
  const saved = page.waitForResponse((r) => r.url().includes(`/api/mixes/${mix.id}`) && r.request().method() === "PATCH");
  await panel.getByRole("button", { name: "Save mix" }).click();
  const conflictStatus = (await saved).status();
  await expect(panel.locator('[data-slot="conflict"]')).toBeVisible();
  const after = (await (await request.get(`/api/mixes/${mix.id}`)).json()) as { rev: number; groups: { weight: number }[] };
  expect(conflictStatus).toBe(412);
  expect(after.groups[0]!.weight).toBe(1); // not overwritten

  const result = {
    at: new Date().toISOString(),
    browser: "chromium (Playwright, headless), Vite dev server, control plane and Postgres 17 on one host",
    latencyMs: { commitToVisible: stats(commit), sendToVisible: stats(send), responseToVisible: stats(response) },
    resume: {
      dropped,
      missedEdits: MISSED,
      reconnectToVisibleMs: recovered,
      dropToVisibleMs: visibleLast - lastCommit,
      eventsChecked: serverSeqs.length,
      missing: missing.length,
      duplicates,
      reconnects: eventRequests.map((r) => ({ after: new URL(r.url).searchParams.get("after"), lastEventId: r.lastEventId ?? null })),
    },
    burst: {
      edits: BURST,
      seconds: burstSeconds,
      editsPerSecond: Math.round((BURST / burstSeconds) * 10) / 10,
      eventsPerSecond: Math.round(((2 * BURST) / burstSeconds) * 10) / 10,
      panelBatches: batches,
      lastVisibleLagMs: burstLag,
    },
    conflict: { status: conflictStatus, overwritten: after.groups[0]!.weight !== 1, revAfter: after.rev },
  };
  mkdirSync(OUT, { recursive: true });
  writeFileSync(path.join(OUT, "A4.json"), JSON.stringify(result, null, 2));
  console.log("A4", JSON.stringify(result));
  expect(pct(commit, 50)).toBeLessThan(300);
  await agent.close();
});
