import type { APIRequestContext } from "@playwright/test";
import { expect, newProject, openWorkspace, test } from "./fixtures";
import { ScriptedHost } from "./host";

// Session changes end to end, without a real agent (the spec plays the agent host, e2e/host.ts): while a turn runs
// the host's worktree watcher reports an uncommitted file → the Chat's Session changes and the open Recipe document
// show it live → the turn commits (the report goes clean) → main moves on the same file → at the end the conflict
// opens in the three-way view: stacked at the Chat's width, keyboard-reachable, side by side in the Recipe document.

test.setTimeout(120_000);

async function note(request: APIRequestContext, slug: string, text: string): Promise<void> {
  const p = (await (await request.get(`/api/projects/${slug}`)).json()) as { rev: number };
  const res = await request.post(`/api/projects/${slug}:note`, {
    data: { text },
    headers: { "If-Match": `"${p.rev}"`, "Idempotency-Key": `note-${slug}-${text.length}-${Date.now()}` },
  });
  expect(res.status(), await res.text()).toBe(200);
}

test("Session changes: live working changes, then a conflict in the three-way view", async ({ page, request }) => {
  const slug = await newProject(request, "Changes");
  const profile = await request.get(`/api/projects/${slug}/agent-profile`);
  const edited = await request.patch(`/api/projects/${slug}/agent-profile`, {
    data: { autoMerge: "never" },
    headers: { "If-Match": `"${((await profile.json()) as { rev: number }).rev}"`, "Idempotency-Key": `profile-${slug}` },
  });
  expect(edited.status(), await edited.text()).toBe(200);
  // NOTES.md exists on main before the session branches off it.
  await note(request, slug, "Baseline WER is 14.2 on the phone golden set.");
  const host = await ScriptedHost.connect();

  await openWorkspace(page, slug);
  await page.locator('[data-tab="chat"]').click();
  const chat = page.locator('[data-panel="chat"]').first();
  await chat.getByLabel("Message to the agent").fill("Tidy the notes");
  await page.keyboard.press("Enter");
  const root = chat.locator("[data-chat-session]");
  await expect(root).toHaveAttribute("data-chat-session", /^ses_/);
  const sessionId = (await root.getAttribute("data-chat-session"))!;
  await host.start(sessionId);
  expect(await host.message(sessionId)).toBe("Tidy the notes");
  await host.report(sessionId, { state: { state: "running", busy: true, turn: 1 } });

  // The watcher reports the uncommitted edit: Session changes list it while the turn runs.
  await host.report(sessionId, { working: { turn: 1, truncated: false, files: [{ path: "NOTES.md", status: "modified", bytes: 64, additions: 2, deletions: 3 }] } });
  const merge = chat.locator('[data-slot="merge-area"]');
  await expect(merge.locator('[data-slot="merge-state"]')).toHaveText("editing");
  await expect(merge.locator('[data-slot="working-changes"] [data-path="NOTES.md"]')).toContainText("+2 −3");

  // The Recipe document of that file shows it too, attributed to the session.
  await merge.getByRole("button", { name: "Diff in Recipe" }).click();
  const recipe = page.locator('[data-panel="recipe"]:visible');
  await recipe.getByRole("button", { name: /^NOTES\.md/ }).click();
  const banner = page.locator('[data-panel="recipe"]:visible [data-slot="working-banner"]');
  await expect(banner.locator(`[data-session="${sessionId}"]`)).toContainText("Edited in");
  await expect(banner).toContainText("+2 −3");

  // The turn commits: the report goes clean and both views drop the uncommitted file.
  const sha = host.commit(sessionId, slug, "NOTES.md", "# Notes\n\nRewritten by the agent.\n");
  await host.entries(sessionId, 1, [
    { key: "t1:commit", kind: "commit", commit: { sha, files: ["NOTES.md"] } },
    { key: "t1:end", kind: "turn", turnInfo: { state: "ended", stopReason: "end_turn" } },
  ]);
  await host.report(sessionId, { state: { state: "running", busy: false, turn: 1 }, working: { turn: 1, truncated: false, files: [] } });
  await expect(banner).toHaveCount(0);
  await expect(merge).toHaveCount(0);

  // Main moves on the same file; the session ends with changes that conflict.
  await note(request, slug, "Main moved: the 8 kHz augmentation helped.");
  await page.locator('[data-tab="chat"]').click();
  await chat.getByRole("button", { name: "End…" }).click();
  await chat.getByRole("button", { name: "End the session" }).click();
  await host.control(sessionId, "end");
  await host.report(sessionId, { state: { state: "done" } });
  await expect(merge).toHaveAttribute("data-merge", "conflict");
  await expect(merge.getByRole("button", { name: "Accept into main" })).toBeDisabled();

  // The conflicting file opens in the three-way view; at the Chat's width it is stacked.
  const conflicts = merge.locator('[data-slot="branch-conflicts"]');
  await expect(conflicts).toContainText("NOTES.md");
  await conflicts.getByRole("button", { name: "Three-way" }).click();
  const view = merge.locator('[data-slot="three-way"][data-path="NOTES.md"]');
  await expect(view).toHaveAttribute("data-mode", "split");
  const width = await view.evaluate((el) => el.getBoundingClientRect().width);
  expect(width).toBeLessThan(672);
  const hunk = view.locator('[data-hunk="conflict"]').first();
  await expect(hunk).toBeVisible();
  expect(await hunk.evaluate((el) => getComputedStyle(el).gridTemplateColumns.split(" ").length), "stacked at the Chat's width").toBe(1);
  await expect(hunk.locator('[data-side="base"]')).toContainText("Baseline WER is 14.2");
  await expect(hunk.locator('[data-side="main"]')).toContainText("8 kHz augmentation");
  await expect(hunk.locator('[data-side="branch"]')).toContainText("Rewritten by the agent.");
  await expect(hunk.locator('[data-side="branch"]')).toContainText("session");

  // Keyboard: Next conflict focuses the first conflict; Unified shows it in three labelled parts.
  const next = view.getByRole("button", { name: "Next conflict" });
  await next.focus();
  await page.keyboard.press("Enter");
  await expect(view.locator('[data-conflict="0"]')).toBeFocused();
  await view.getByRole("button", { name: "Unified" }).focus();
  await page.keyboard.press("Enter");
  await expect(view).toHaveAttribute("data-mode", "unified");
  const block = view.locator('[data-hunk="conflict"]').first();
  await expect(block.locator('[data-side="main"]')).toContainText("8 kHz augmentation");
  await expect(block.locator('[data-side="base"]')).toContainText("base (before both)");

  // The Recipe document's branch view opens the same conflict side by side when it has the room.
  await merge.getByRole("button", { name: "Diff in Recipe" }).click();
  const diff = page.locator('[data-panel="recipe"]:visible [data-testid="branch-diff"]');
  await expect(diff).toContainText("conflict with main");
  const wide = diff.locator('[data-slot="three-way"][data-path="NOTES.md"]');
  await expect(wide.locator('[data-hunk="conflict"]').first()).toBeVisible();
  const cols = await wide.locator('[data-hunk="conflict"]').first().evaluate((el) => getComputedStyle(el).gridTemplateColumns.split(" ").length);
  const wideWidth = await wide.evaluate((el) => el.getBoundingClientRect().width);
  expect(cols).toBe(wideWidth >= 672 ? 3 : 1);

  // Dark theme: the same view, drawn from the tokens.
  await page.emulateMedia({ colorScheme: "dark" });
  await expect(wide.locator('[data-hunk="conflict"]').first()).toBeVisible();
  await host.close();
});
