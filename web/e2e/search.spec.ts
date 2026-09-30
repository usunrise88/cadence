import { expect, newProject, openWorkspace, test } from "./fixtures";

// Palette entity search (docs/spec/11-ui-panels.md "Search"): projects.search over the current project and the
// registry, grouped by kind; Enter opens, Space previews in the Inspector, "Open as list" and saved views.

test.describe("search", () => {
  test("palette entity search: grouped hits, typos, qualifiers, Enter and Space", async ({ page, request }) => {
    const slug = await newProject(request, "Quokka");
    await openWorkspace(page, slug);
    const dialog = page.getByRole("dialog");
    const box = dialog.getByRole("combobox");

    // A typo still finds the registry's fixture dataset, under its kind's heading.
    await page.keyboard.press("ControlOrMeta+k");
    await box.fill("fleurz");
    const versions = dialog.getByRole("group", { name: "Dataset versions" });
    await expect(versions.getByRole("option", { name: /dataset\/fleurs-he-smoke/ })).toBeVisible();
    await expect(dialog.getByRole("group", { name: "Registry collections" }).getByRole("option", { name: /dataset\/fleurs-he-smoke/ })).toBeVisible();

    // Qualifiers render as chips; an unknown one explains itself.
    await box.fill("fleurs kind:dataset_version");
    await expect(dialog.locator('[data-slot="palette-qualifiers"]')).toContainText("kind: dataset_version");
    await expect(dialog.getByText("Projects", { exact: true })).toHaveCount(0);
    await box.fill("fleurs colour:red");
    await expect(dialog.getByRole("alert")).toContainText("kind");

    // Enter opens the best hit: a registry version has no document yet, so the Inspector shows it.
    await box.fill("fleurs-he kind:dataset_version");
    const best = versions.getByRole("option", {
      name: /dataset\/fleurs-he-smoke/,
    });
    await expect(best).toBeVisible();
    await expect(dialog.getByRole("option").first()).toHaveAttribute("aria-selected", "true");
    await expect(best).toHaveAttribute("aria-selected", "true");
    await page.keyboard.press("Enter");
    await expect(dialog).toHaveCount(0);
    await page.locator('[data-tab="inspector"]').click();
    const inspector = page.locator('[data-panel="inspector"]');
    await expect(inspector).toContainText("dataset/fleurs-he-smoke");
    await expect(inspector).toContainText("dataset_version");

    // Space after moving with the arrows previews the project in the Inspector and keeps the query as typed.
    await page.keyboard.press("ControlOrMeta+k");
    await box.fill("Quoka kind:project");
    const hit = dialog.getByRole("option", { name: new RegExp(slug) }).first();
    await expect(hit).toBeVisible();
    await page.keyboard.press("ArrowDown");
    await page.keyboard.press("ArrowUp");
    await expect(hit).toHaveAttribute("aria-selected", "true");
    await page.keyboard.press(" ");
    await expect(box).toHaveValue("Quoka kind:project");
    await page.keyboard.press("Escape");
    await page.locator('[data-tab="inspector"]').click();
    await expect(inspector).toContainText(`Quokka ${slug}`);
  });

  test("open as list in the Library, save a view, reopen it from the palette", async ({ page, request }) => {
    const slug = await newProject(request, "Library search");
    await openWorkspace(page, slug);
    const dialog = page.getByRole("dialog");
    const box = dialog.getByRole("combobox");

    await page.keyboard.press("ControlOrMeta+k");
    await box.fill("fleurs kind:dataset_version");
    await dialog.getByRole("option", { name: /Open as list/ }).click();
    await expect(dialog).toHaveCount(0);
    const filter = page.getByRole("textbox", { name: "Filter the library" });
    await expect(filter).toHaveValue("fleurs kind:dataset_version");
    const results = page.getByRole("grid", { name: "Search results" });
    await expect(results.getByRole("row", { name: /fleurs-he-smoke/ })).toBeVisible();
    await expect(results.getByRole("row", { name: /fleurs-ru-smoke/ })).toBeVisible();

    const views = page.getByRole("group", { name: "Saved searches" });
    await views.getByRole("button", { name: "Save view" }).click();
    await views.getByRole("textbox", { name: "View name" }).fill("Fleurs data");
    await views.getByRole("button", { name: "Save", exact: true }).click();
    await expect(views.getByRole("button", { name: "Fleurs data" })).toHaveAttribute("aria-pressed", "true");

    // Clear the filter, then open the saved view from the palette.
    await filter.fill("");
    await expect(page.getByRole("grid", { name: "Registry versions" })).toBeVisible();
    await page.keyboard.press("ControlOrMeta+k");
    await expect(dialog.getByText("Saved searches", { exact: true })).toBeVisible();
    await dialog.getByRole("option", { name: /Fleurs data/ }).click();
    await expect(filter).toHaveValue("fleurs kind:dataset_version");
    await expect(views.getByRole("button", { name: "Fleurs data" })).toHaveAttribute("aria-pressed", "true");
    await expect(results.getByRole("row", { name: /fleurs-he-smoke/ })).toBeVisible();
  });
});
