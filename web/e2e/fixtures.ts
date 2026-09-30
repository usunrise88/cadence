import { test as base, expect, type APIRequestContext, type Page } from "@playwright/test";

// Helpers shared by the shell specs: projects are created through the API (one command, as an agent would).

let counter = 0;

export async function newProject(request: APIRequestContext, name = "E2E"): Promise<string> {
  const slug = `e2e-${Date.now().toString(36)}-${counter++}`;
  const res = await request.post("/api/projects", {
    data: { slug, name: `${name} ${slug}` },
    headers: { "Idempotency-Key": `key-${slug}` },
  });
  expect(res.status()).toBe(201);
  return slug;
}

export async function openWorkspace(page: Page, slug: string, workspace = "Training", query = ""): Promise<void> {
  await page.goto(`/p/${slug}/w/${workspace}${query}`);
  // Every default workspace has tabs (Ops has no Library); the restore flag below says the layout is complete.
  await expect(page.locator("[data-tab]").first()).toBeVisible();
  await page.waitForFunction(() => {
    const c = (window as unknown as { __cadence?: { sync: { getState(): { restoring: boolean; key: string | null } } } }).__cadence;
    return !!c && !c.sync.getState().restoring && !!c.sync.getState().key;
  });
}

/** Runs a command through the palette, the way a person would. */
export async function runCommand(page: Page, title: string): Promise<void> {
  await page.keyboard.press("ControlOrMeta+k");
  await page.getByRole("dialog").getByRole("combobox").fill(`>${title}`);
  await page.getByRole("option", { name: new RegExp(title) }).first().click();
}

export function floatBounds(page: Page) {
  return page.evaluate(() => {
    const el = document.querySelector<HTMLElement>(".dv-resize-container");
    const host = document.querySelector<HTMLElement>(".dv-dockview");
    if (!el || !host) return null;
    const a = el.getBoundingClientRect();
    const b = host.getBoundingClientRect();
    return { left: Math.round(a.left - b.left), top: Math.round(a.top - b.top), width: Math.round(a.width), height: Math.round(a.height) };
  });
}

export const test = base;
export { expect };
