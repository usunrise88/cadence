import { execFileSync } from "node:child_process";
import path from "node:path";
import type { APIRequestContext, Locator, Page } from "@playwright/test";
import { expect, newProject, openWorkspace, test } from "./fixtures";

// The annotation workflow against the real control plane (phase 4 · stream A; docs/spec/04-blocks.md "Annotation
// workflow"). The stack seeds a stereo μ-law call on a local mount and its segments artifact (e2e/stack.sh
// seed-annotation); batches.new samples a golden-set batch from it through the API. The admin annotates two items in
// the Triage panel's Annotate mode; a reviewer invited to the batch opens the link in a browser of their own, sees
// that batch and nothing else, and gives the double items their second, blind transcript (one differs by a word); the
// Annotation batch document shows the freeze blockers, adjudicates the disputed item and asks for the freeze approval.

const SEED = path.resolve(import.meta.dirname, "../.e2e/seed-annotation");

type Item = { id: string; position: number; double: boolean; state: string; prefill: { text: string }; annotations: { annotator: { id: string } }[] };
type Batch = { id: string; rev: number; state: string; progress: Record<string, number>; canFreeze: { ok: boolean; reasons: string[] } };

/** The text the seeded pseudo-label should have said: its prefill without the wrong last word. */
const truth = (it: Item) => it.prefill.text.replace(/ možda$/, "");

async function seededBatch(request: APIRequestContext, slug: string, name: string, size: number): Promise<Batch> {
  const { segments } = JSON.parse(execFileSync(SEED, ["--project", slug], { encoding: "utf8" })) as { segments: string };
  const res = await request.post(`/api/projects/${slug}/batches`, {
    data: { name, segments, size, doubleShare: 0.5, seed: 3 },
    headers: { "Idempotency-Key": `batch-${slug}-${name}` },
  });
  expect(res.status(), await res.text()).toBe(201);
  return (await res.json()) as Batch;
}

async function items(request: APIRequestContext, batch: string): Promise<Item[]> {
  const res = await request.get(`/api/batches/${batch}/batch-items`, { params: { queue: "all" } });
  expect(res.status(), await res.text()).toBe(200);
  return ((await res.json()) as { items: Item[] }).items;
}

async function batchOf(request: APIRequestContext, id: string): Promise<Batch> {
  const res = await request.get(`/api/batches/${id}`);
  expect(res.status(), await res.text()).toBe(200);
  return (await res.json()) as Batch;
}

/**
 * Annotates the item the Annotate view shows with text (Done) and waits until the queue counts it ("<open> to annotate ·
 * <done> done by you" afterwards). Returns the item's id.
 */
async function annotateShown(scope: Locator, text: (id: string) => string, after: { open: number; done: number }): Promise<string> {
  const form = scope.locator('[data-slot="annotate-item"]');
  await expect(form).toBeVisible();
  const id = (await form.getAttribute("data-item"))!;
  const box = form.locator('[data-slot="transcript"]');
  await expect(box).toHaveValue(/ možda$/); // the machine's guess, its wrong last word included
  await box.fill(text(id));
  await form.getByRole("button", { name: "Done", exact: true }).click();
  await expect(scope.getByText(`${after.open} to annotate · ${after.done} done by you`)).toBeVisible();
  await expect(scope.locator(`[data-slot="annotate-item"][data-item="${id}"]`)).toHaveCount(0);
  // An empty queue says so instead of showing an item already done.
  if (after.open === 0) await expect(scope.getByText("Nothing left to annotate in this batch.")).toBeVisible();
  return id;
}

test("Annotation: Triage annotates, a reviewer sees only its batch, a disputed item is adjudicated, freeze is asked", async ({ page, request, browser }) => {
  test.setTimeout(120_000);
  const slug = await newProject(request, "Annotation");
  // Another open batch in the same project: the reviewer must not see it.
  const other = await seededBatch(request, slug, "calls-other", 2);
  const batch = await seededBatch(request, slug, "calls-golden", 4);
  expect(batch.state).toBe("open");
  expect(batch.canFreeze.ok).toBe(false);
  const all = await items(request, batch.id);
  expect(all).toHaveLength(4);
  expect(all.filter((i) => i.double)).toHaveLength(2);
  const byId = new Map(all.map((i) => [i.id, i]));

  // 1. The admin annotates two items in Triage → Annotate, starting from the prefilled machine guess.
  await openWorkspace(page, slug, "Triage");
  await page.locator('[data-tab="triage"]').click();
  const triage = page.locator('[data-panel="triage"]');
  await triage.getByRole("tab", { name: "Annotate" }).click();
  await triage.getByLabel("Batch").selectOption(batch.id);
  const view = triage.locator(`[data-slot="annotate-view"][data-batch="${batch.id}"]`);
  await expect(view.getByText("4 to annotate · 0 done by you")).toBeVisible();
  const first = await annotateShown(view, (id) => truth(byId.get(id)!), { open: 3, done: 1 });
  const second = await annotateShown(view, (id) => truth(byId.get(id)!), { open: 2, done: 2 });
  expect(second).not.toBe(first);

  // The admin's other two items go in through the API (annotations.new, a person's own command).
  for (const it of all.filter((i) => i.id !== first && i.id !== second)) {
    const res = await request.post(`/api/batches/${batch.id}/batch-items/${it.id}/annotations`, {
      data: { status: "done", text: truth(it) },
      headers: { "Idempotency-Key": `ann-${it.id}` },
    });
    expect(res.status(), await res.text()).toBe(201);
  }
  let b = await batchOf(request, batch.id);
  expect(b.progress.agreed).toBe(2); // the single items
  expect(b.progress.pending).toBe(2); // the double items wait for a second person

  // 2. A reviewer is invited (admin); the link opens this batch only, in a browser without the admin's session.
  const inv = await request.post(`/api/batches/${batch.id}/invitations`, { data: { name: "ana" }, headers: { "Idempotency-Key": `inv-${batch.id}` } });
  expect(inv.status(), await inv.text()).toBe(201);
  const { url } = (await inv.json()) as { url: string };
  expect(url).toMatch(/^\/#invitation=cri_/);
  const reviewer = await browser.newContext({ storageState: { cookies: [], origins: [] }, baseURL: test.info().project.use.baseURL, extraHTTPHeaders: { "Cadence-Client": "web" } });
  try {
    const rp: Page = await reviewer.newPage();
    await rp.goto(url);
    const app = rp.locator('[data-slot="reviewer-app"]');
    await expect(app).toBeVisible({ timeout: 30_000 });
    await expect(app.getByText("Annotation batch calls-golden")).toBeVisible();
    await expect(app.getByText("ana · annotator")).toBeVisible();
    // No workspace, no other panel, and the token left the address bar.
    await expect(rp.locator("[data-tab]")).toHaveCount(0);
    expect(new URL(rp.url()).hash).not.toContain("invitation");
    // The session reaches this batch and nothing else.
    for (const p of ["/api/projects", `/api/projects/${slug}/batches`, `/api/batches/${other.id}`, `/api/batches/${batch.id}/batch-items?queue=all`]) {
      const res = await rp.request.get(p);
      expect(res.status(), `${p}: ${await res.text()}`).toBe(403);
    }
    expect((await rp.request.get(`/api/batches/${batch.id}`)).status()).toBe(200);

    // The reviewer's queue: the two double items, blind — the form starts from the machine's guess, never from the
    // admin's transcript. One agrees with the admin; the other differs by a word.
    const rv = app.locator(`[data-slot="annotate-view"][data-batch="${batch.id}"]`);
    await expect(rv.getByText("2 to annotate · 0 done by you")).toBeVisible();
    const agreed = await annotateShown(rv, (id) => truth(byId.get(id)!), { open: 1, done: 1 });
    const disputed = await annotateShown(
      rv,
      (id) => {
        const words = truth(byId.get(id)!).split(" ");
        words[1] = "nešto";
        return words.join(" ");
      },
      { open: 0, done: 2 },
    );
    expect(byId.get(agreed)!.double && byId.get(disputed)!.double).toBe(true);
  } finally {
    await reviewer.close();
  }

  // 3. The Annotation batch document: the freeze waits for adjudication; the admin takes one transcript.
  b = await batchOf(request, batch.id);
  expect(b.progress.disputed).toBe(1);
  expect(b.canFreeze.reasons).toContain("1 item(s) wait for adjudication");
  await triage.getByRole("button", { name: "Open the batch" }).click();
  const doc = page.locator(`[data-batch="${batch.id}"]`).filter({ has: page.locator('[data-slot="progress"]') });
  await expect(doc).toBeVisible();
  // The workspace builds no floating Audio (schema 3): nothing covers the document's lower sections.
  await expect(page.getByRole("button", { name: "Close Audio" })).toHaveCount(0);
  await expect(doc.locator('[data-slot="progress"]')).toContainText("3 agreed");
  await expect(doc.locator('[data-slot="progress"]')).toContainText("1 disputed");
  await expect(doc.locator('[data-slot="agreement"]')).toContainText("over 2 double item(s)");
  const freeze = doc.getByRole("button", { name: "Freeze (approval)" });
  await expect(freeze).toBeDisabled();
  await expect(doc.getByText("1 item(s) wait for adjudication")).toBeVisible();
  const adjudication = doc.getByRole("region", { name: "Adjudication (1)" });
  await expect(adjudication.getByRole("button", { name: "Take this transcript" })).toHaveCount(2);
  await adjudication.getByRole("button", { name: "Take this transcript" }).first().click();

  // Every item resolved and the agreement met: the freeze is an approval the admin decides.
  await expect(doc.getByText("Ready to freeze.")).toBeVisible();
  await expect(doc.locator('[data-slot="progress"]')).toContainText("1 adjudicated");
  await expect(freeze).toBeEnabled();
  await freeze.click();
  await expect(doc.getByText(/Waiting for the admin's approval \(apr_/)).toBeVisible();
  b = await batchOf(request, batch.id);
  expect(b.state).toBe("open");
  expect(b.canFreeze.ok).toBe(true);
});
