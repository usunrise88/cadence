import { McpAgent, mintAgentToken, openMix, projectWithMix } from "./agent";
import { expect, openWorkspace, test } from "./fixtures";

// The Mix document with an agent editing through MCP (phase-1 gate): the agent's edit arrives live as a draft with
// the session badge, presence disables the table, Accept applies it attributed to the person; a person's save on a
// revision that moved on gets the 412 conflict notice, never an overwrite.

test("an agent's edit appears live as a draft with the session badge; accept applies it", async ({ page, request }) => {
  const { slug, mix } = await projectWithMix(request);
  await openWorkspace(page, slug);
  await openMix(page, mix.name);
  const panel = page.locator('[data-panel="mix"]');
  await expect(panel.locator('[data-slot="mix-preview"]')).toContainText("he-IL");

  const agent = await McpAgent.connect(mintAgentToken(slug, "ses_9"), slug);
  const read = await agent.call("mixes.get", { id: mix.id });
  const edit = await agent.call("mixes.edit", { id: mix.id, ifMatch: read.result.etag, body: { temperature: 2 } }, "toolu_e2e_1");
  expect(edit.isError, JSON.stringify(edit.result)).toBe(false);

  const draft = panel.locator('[data-slot="draft"]');
  await expect(draft).toBeVisible();
  await expect(draft).toHaveAttribute("data-draft-rev", "1");
  const badge = draft.locator('[data-slot="actor-badge"]');
  await expect(badge).toHaveText("agent · session 9");
  await expect(badge).toHaveAttribute("data-tool-call", "toolu_e2e_1");
  // Presence: "agent editing" in the header, the table waits.
  await expect(panel.locator('[data-slot="entity-header"] [data-slot="presence"]')).toContainText("agent editing");
  await expect(panel.getByLabel("Weight of target")).toBeDisabled();
  // The diff on hover.
  await draft.getByText("1 change").hover();
  await expect(page.locator('[data-slot="draft-diff"]')).toContainText("/temperature");

  // The agent's second edit updates the same draft live.
  await agent.call("mixes.edit", { id: mix.id, ifMatch: read.result.etag, body: { replayShare: 0 } }, "toolu_e2e_2");
  await expect(draft).toHaveAttribute("data-draft-rev", "2");
  await expect(badge).toHaveAttribute("data-tool-call", "toolu_e2e_2");

  // An agent never accepts; the person does.
  const denied = await agent.call("drafts.accept", { id: await draft.getAttribute("data-draft-id"), ifMatch: '"2"' });
  expect(denied.isError && denied.result.status).toBe(403);
  await draft.getByRole("button", { name: "Accept" }).click();
  await expect(draft).toHaveCount(0);
  await expect(panel.getByText("Groups · rev 2")).toBeVisible();
  await expect(panel.getByLabel("Temperature")).toHaveValue("2");
  await expect(panel.getByLabel("Weight of target")).toBeEnabled();
  // The revision is the person's; the header's badge names the agent whose draft it was.
  await expect(panel.locator('[data-slot="entity-header"] [data-slot="actor-badge"]')).toHaveText("agent · session 9");
  await agent.close();
});

test("a person's save on a mix that moved on gets the conflict notice, then reapplies", async ({ page, request }) => {
  const { slug, mix } = await projectWithMix(request, "he-conflict");
  await openWorkspace(page, slug);
  await openMix(page, mix.name);
  const panel = page.locator('[data-panel="mix"]');

  // The person starts editing rev 1.
  await panel.getByLabel("Weight of target").fill("3");
  // Meanwhile the agent drafts a change; the table waits while it edits.
  const agent = await McpAgent.connect(mintAgentToken(slug, "ses_12"), slug);
  const edit = await agent.call("mixes.edit", { id: mix.id, ifMatch: '"1"', body: { temperature: 2 } }, "toolu_conflict");
  const draftId = (edit.result.data as { draft: { id: string } }).draft.id;
  await expect(panel.locator('[data-slot="draft"]')).toBeVisible();
  await expect(panel.getByRole("button", { name: "Save mix" })).toBeDisabled();

  // Someone accepts the draft elsewhere (another tab): the mix moves to rev 2 under the person's edit.
  const acc = await request.post(`/api/drafts/${draftId}:accept`, { headers: { "Idempotency-Key": `acc-${slug}`, "If-Match": '"1"' } });
  expect(acc.status(), await acc.text()).toBe(200);
  await expect(panel.locator('[data-slot="draft"]')).toHaveCount(0);
  await expect(panel.getByLabel("Weight of target")).toHaveValue("3"); // the local edit survives

  const conflict = page.waitForResponse((r) => r.url().includes(`/api/mixes/${mix.id}`) && r.request().method() === "PATCH");
  await panel.getByRole("button", { name: "Save mix" }).click();
  expect((await conflict).status()).toBe(412);
  const notice = panel.locator('[data-slot="conflict"]');
  await expect(notice).toContainText("moved to rev 2");
  await expect(notice.locator('[data-slot="actor-badge"]')).toHaveText("agent · session 12");

  await notice.getByRole("button", { name: "Reapply my changes" }).click();
  await panel.getByRole("button", { name: "Save mix" }).click();
  await expect(panel.getByText("Groups · rev 3")).toBeVisible();
  await expect(panel.getByLabel("Weight of target")).toHaveValue("3");
  await expect(panel.getByLabel("Temperature")).toHaveValue("2"); // the agent's accepted change is kept
  await agent.close();
});
