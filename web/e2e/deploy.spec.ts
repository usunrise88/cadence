import type { Page } from "@playwright/test";
import { expect, newProject, openWorkspace, test } from "./fixtures";

// Phase 5 · stream D4 panels against the real control plane: Settings → Deployment targets lists the staging target
// seeded at first start (defaults.yaml serving.staging_target) and the instance signing key section; the Ops
// workspace opens with the Shadow panel, which is empty in a project without shadow deployments.

async function openSettings(page: Page, section: string) {
  await page.getByTestId("user-menu").click();
  await page.getByRole("menuitem", { name: "Settings" }).click();
  await page.getByRole("tab", { name: section }).click();
}

test.describe("deployment", () => {
  test("Settings → Deployment targets shows the seeded staging target and its health", async ({ page, request }) => {
    const res = await request.get("/api/deployment-targets");
    expect(res.status(), await res.text()).toBe(200);
    const { items } = (await res.json()) as { items: { name: string; kind: string }[] };
    const staging = items.find((t) => t.kind === "staging");
    expect(staging, "a staging target is seeded at first start").toBeTruthy();

    const slug = await newProject(request, "Targets");
    await openWorkspace(page, slug, "Ops");
    await openSettings(page, "Deployment targets");
    const row = page.getByTestId(`target-${staging!.name}`);
    await expect(row).toBeVisible();
    await expect(row.getByTestId("target-health")).toContainText(/up|down|unknown/);
    await expect(page.getByRole("heading", { name: "Instance signing key" })).toBeVisible();
  });

  test("the Ops workspace opens the Shadow panel, empty without shadow deployments", async ({ page, request }) => {
    const slug = await newProject(request, "Shadow");
    await openWorkspace(page, slug, "Ops");
    await page.locator('[data-tab="shadow"]').click();
    const shadow = page.locator('[data-slot="shadow"]');
    await expect(shadow).toBeVisible();
    await expect(shadow.getByText("No shadow deployment", { exact: true })).toBeVisible();
    await expect(shadow.getByRole("combobox", { name: "Deployment" })).toBeDisabled();
  });
});
