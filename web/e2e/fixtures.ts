import { test as base, expect, type APIRequestContext, type Page } from "@playwright/test";

// Helpers shared by the shell specs: projects are created through the API (one command, as an agent would).

let counter = 0;

/** Creates a project through projects.new (202 + bootstrap job) and waits until the bootstrap job ends. */
export async function newProject(request: APIRequestContext, name = "E2E"): Promise<string> {
  const slug = `e2e-${Date.now().toString(36)}-${counter++}`;
  const res = await request.post("/api/projects", {
    data: { slug, name: `${name} ${slug}` },
    headers: { "Idempotency-Key": `key-${slug}` },
  });
  expect(res.status(), await res.text()).toBe(202);
  const { jobId } = (await res.json()) as { jobId: string };
  await expect
    .poll(
      async () => {
        const job = await request.get(`/api/jobs/${jobId}:wait`, { params: { timeout: 10 } });
        return ((await job.json()) as { state: string }).state;
      },
      { timeout: 60_000, intervals: [100] },
    )
    .toBe("done");
  return slug;
}

export async function openWorkspace(page: Page, slug: string, workspace = "Training", query = ""): Promise<void> {
  await page.goto(`/p/${slug}/w/${workspace}${query}`);
  await expect(page.locator('[data-tab="library"]')).toBeVisible();
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
