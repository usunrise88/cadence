import { expect, floatBounds, newProject, openWorkspace, runCommand, test } from "./fixtures";

test.describe("shell", () => {
  test("palette: commands, help and projects", async ({ page, request }) => {
    const slug = await newProject(request);
    await openWorkspace(page, slug);
    await page.keyboard.press("ControlOrMeta+k");
    const box = page.getByRole("dialog").getByRole("combobox");
    await box.fill(">snap");
    await expect(page.getByRole("option", { name: /Toggle snapping/ })).toBeVisible();
    await box.fill("?keyboard");
    await expect(page.getByRole("option", { name: /Keyboard/ }).first()).toBeVisible();
    await box.fill("@");
    await expect(page.getByRole("option", { name: new RegExp(slug) })).toBeVisible();
    await page.keyboard.press("Escape");
    await expect(page.getByRole("dialog")).toHaveCount(0);
  });

  test("float from the tab menu, dock back, maximize is refused on floats", async ({ page, request }) => {
    const slug = await newProject(request);
    await openWorkspace(page, slug);
    await page.locator('[data-tab="inspector"]').click({ button: "right" });
    await page.getByRole("menuitem", { name: "Float" }).click();
    await expect(page.locator(".dv-resize-container")).toHaveCount(1);
    const float = page.locator(".dv-resize-container");
    await expect(float.getByRole("button", { name: /cannot be maximized/ })).toBeDisabled();
    await float.locator('[data-tab="inspector"]').click({ button: "right" });
    await page.getByRole("menuitem", { name: "Dock left" }).click();
    await expect(page.locator(".dv-resize-container")).toHaveCount(0);
    await expect(page.locator('[data-tab="inspector"]')).toBeVisible();
  });

  test("snapping: a drag near the edge lands on it; Ctrl/Cmd places freely", async ({ page, request }) => {
    const slug = await newProject(request);
    await openWorkspace(page, slug);
    await page.locator('[data-tab="help"]').click();
    await runCommand(page, "Float panel");
    const handle = page.locator(".dv-resize-container .dv-void-container").first();
    const drag = async (toLeft: number, modifier?: "Control") => {
      const h = (await handle.boundingBox())!;
      let x = h.x + h.width / 2;
      const y = h.y + h.height / 2;
      if (modifier) await page.keyboard.down(modifier);
      await page.mouse.move(x, y);
      await page.mouse.down();
      // The first moves arm Dockview's drag (threshold); measure from where the window is once it follows.
      x += 6;
      await page.mouse.move(x, y + 10);
      await page.mouse.move(x, y + 20);
      const dx = toLeft - (await floatBounds(page))!.left;
      for (let i = 1; i <= 10; i++) await page.mouse.move(x + (dx * i) / 10, y + 20);
      await page.mouse.up();
      if (modifier) await page.keyboard.up(modifier);
    };
    await drag(3);
    expect((await floatBounds(page))!.left).toBe(0);
    await drag(203);
    await drag(3, "Control");
    expect((await floatBounds(page))!.left).toBe(3);
  });

  test("keyboard: arrows move a focused float, F6 moves between groups, Alt+W closes", async ({ page, request }) => {
    const slug = await newProject(request);
    await openWorkspace(page, slug);
    await page.locator('[data-tab="help"]').click();
    await runCommand(page, "Float panel");
    const before = (await floatBounds(page))!;
    await page.locator(".dv-resize-container .dv-tab").first().focus();
    await page.keyboard.press("ArrowRight");
    await page.keyboard.press("Shift+ArrowDown");
    const after = (await floatBounds(page))!;
    expect(after.left - before.left).toBe(1);
    expect(after.top - before.top).toBe(10);
    await page.keyboard.press("F6");
    await page.keyboard.press("Alt+w");
    await expect(page.locator("[data-tab]")).toHaveCount(5); // Training: project, library, chat, inspector, help, getting started; one closed
  });

  test("popout: panel, theme, menus and return to grid", async ({ page, request }) => {
    const slug = await newProject(request);
    await openWorkspace(page, slug);
    await page.locator('[data-tab="help"]').click();
    const [popup] = await Promise.all([page.waitForEvent("popup"), runCommand(page, "Pop out group")]);
    await popup.waitForLoadState();
    await expect(popup.locator('[data-panel="help"]')).toBeVisible();
    // The theme class and the stylesheet reached the popout document.
    const themed = await popup.evaluate(() => ({
      cls: document.documentElement.className,
      guide: getComputedStyle(document.documentElement).getPropertyValue("--cadence-guide").trim(),
    }));
    expect(themed.cls).toMatch(/light|dark/);
    expect(themed.guide).not.toBe("");
    // Tooltips and the palette open in the popout's own document; shortcuts work there too.
    await popup.getByRole("button", { name: "Return to grid" }).hover();
    await expect(popup.locator('[data-slot="tooltip-content"]')).toContainText("Return to grid");
    await popup.locator('[data-panel="help"]').click();
    await popup.keyboard.press("ControlOrMeta+k");
    await expect(popup.getByRole("dialog").getByRole("combobox")).toBeVisible();
    await expect(page.getByRole("dialog")).toHaveCount(0);
    await popup.keyboard.press("Escape");
    // Base UI menus portal into the popout's own document.
    await popup.getByRole("button", { name: "Window actions" }).click();
    await expect(popup.getByRole("menuitem", { name: "Return to grid" })).toBeVisible();
    await popup.getByRole("menuitem", { name: "Return to grid" }).click();
    await expect(page.locator('[data-panel="help"]')).toBeVisible();
  });

  test("workspace: saved, restored after reload, deep link opens the document", async ({ page, request }) => {
    const slug = await newProject(request);
    await openWorkspace(page, slug);
    await page.locator('[data-tab="inspector"]').click({ button: "right" });
    await page.getByRole("menuitem", { name: "Close" }).click();
    await expect(page.getByTestId("workspace-sync")).toContainText("Saved", { timeout: 5000 });
    await page.reload();
    await openWorkspace(page, slug);
    await expect(page.locator('[data-tab="inspector"]')).toHaveCount(0);
    await page.goto(`/p/${slug}/w/Eval?doc=project:${slug}`);
    await expect(page.locator('[data-panel="project"]')).toBeVisible();
    await expect(page.getByRole("heading", { name: new RegExp(slug) })).toBeVisible();
  });

  test("two tabs on one workspace: the losing tab gets a notice", async ({ browser, request }) => {
    const slug = await newProject(request);
    const ctx = await browser.newContext();
    const a = await ctx.newPage();
    const b = await ctx.newPage();
    await openWorkspace(a, slug);
    await a.locator('[data-tab="help"]').click({ button: "right" });
    await a.getByRole("menuitem", { name: "Close" }).click();
    await expect(a.getByTestId("workspace-sync")).toContainText("Saved", { timeout: 5000 });
    await openWorkspace(b, slug);
    await b.locator('[data-tab="inspector"]').click({ button: "right" });
    await b.getByRole("menuitem", { name: "Close" }).click();
    await expect(b.getByTestId("workspace-sync")).toContainText("Saved", { timeout: 5000 });
    await a.locator('[data-tab="library"]').click({ button: "right" });
    await a.getByRole("menuitem", { name: "Close" }).click();
    await expect(a.getByTestId("workspace-conflict")).toBeVisible({ timeout: 5000 });
    await ctx.close();
  });

  test("errors: a duplicate slug shows the problem inline", async ({ page, request }) => {
    const slug = await newProject(request);
    await openWorkspace(page, slug);
    await runCommand(page, "New project");
    const dialog = page.getByRole("dialog");
    await dialog.getByRole("textbox", { name: "Name", exact: true }).fill("Duplicate");
    await dialog.getByRole("button", { name: "Customise" }).click();
    await dialog.getByRole("textbox", { name: /Slug/ }).fill(slug);
    await page.getByRole("button", { name: "Create project" }).click();
    await expect(dialog.getByRole("alert").first()).toBeVisible();
    await expect(dialog.getByRole("heading", { name: "New project" })).toBeVisible(); // nothing was created; the form stays open
  });

  test("wizard: three fields create a project, bootstrap runs and the Project document opens", async ({ page, request }) => {
    const first = await newProject(request);
    await openWorkspace(page, first);
    await runCommand(page, "New project");
    const dialog = page.getByRole("dialog");
    const name = `Wizard ${Date.now().toString(36)}`;
    await dialog.getByRole("textbox", { name: "Name", exact: true }).fill(name);
    await dialog.getByRole("textbox", { name: "Language" }).fill("he-IL");
    await expect(dialog.getByRole("textbox", { name: /recordings/ })).toBeDisabled();
    await expect(dialog.getByRole("heading", { name: "Recommended defaults" })).toBeVisible();
    await dialog.getByRole("button", { name: "Create project" }).click();
    // Bootstrap done: the wizard closes and the new project's workspace opens with its Project document.
    await expect(page).toHaveURL(new RegExp(`/p/${name.toLowerCase().replace(/ /g, "-")}/w/`), { timeout: 60_000 });
    await expect(page.getByTestId("project-wizard")).toHaveCount(0);
    const doc = page.locator('[data-panel="project"]');
    await expect(doc.getByRole("heading", { name, exact: true })).toBeVisible();
    await expect(doc.locator('[data-slot="entity-header"] [data-slot="status-chip"]')).toHaveText(/active/i);
    await expect(doc.getByText("he-IL").first()).toBeVisible();
    await expect(doc.getByText("guardrails-default")).toBeVisible();
    // The agent's actions sit behind the AI icon in the header's corner, apart from the project's own verbs.
    await expect(doc.locator('[data-slot="entity-header"]').getByRole("button", { name: "Explain this" })).toHaveCount(0);
    await doc.locator('[data-slot="entity-header"] [data-slot="agent-menu"]').click();
    await expect(page.getByRole("menuitem", { name: "Ask agent about this project" })).toBeVisible();
    await expect(page.getByRole("menuitem", { name: "Explain this" })).toBeVisible();
    await page.keyboard.press("Escape");
  });

  test("project repository: note, agent settings and the recipe document", async ({ page, request }) => {
    const slug = await newProject(request);
    await openWorkspace(page, slug);
    const doc = page.locator('[data-panel="project"]');
    await doc.getByRole("tab", { name: "Notes" }).click();
    await doc.getByRole("textbox").fill("FLEURS he alone overfits after 2k steps; mix in replay from the start.");
    await doc.getByRole("button", { name: "Add note" }).click();
    await expect(doc.getByTestId("project-notes")).toContainText("FLEURS he alone overfits", { timeout: 10_000 });

    await doc.getByRole("tab", { name: "Overview" }).click();
    await doc.getByRole("button", { name: "Open Agent settings" }).click();
    const settings = page.locator('[data-panel="agent-settings"]');
    await expect(settings.getByTestId("rendered-file")).toContainText('"permissions"');
    await settings.getByRole("tab", { name: "opencode.json" }).click();
    await expect(settings.getByTestId("rendered-file")).toContainText('"permission"');

    await doc.getByRole("button", { name: "Browse files" }).click();
    const recipe = page.locator('[data-panel="recipe"]:visible');
    await expect(recipe.getByTestId("recipe-content")).toContainText(slug);
    await recipe.getByRole("button", { name: /^AGENTS\.md/ }).click();
    await expect(page.locator('[data-panel="recipe"]:visible').getByRole("heading", { name: "AGENTS.md" })).toBeVisible();
  });
});
