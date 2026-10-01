import type { APIRequestContext, Page, PlaywrightWorkerArgs } from "@playwright/test";
import { expect, newProject, openWorkspace, runCommand, test } from "./fixtures";

// Phase-1 panels against the real control plane: Approvals (a gated request decided from its card), Settings
// (API keys created and revoked; a compute edit that meets a 412), the status-bar badge and notification history.

let n = 0;
const key = (what: string) => `e2e-${what}-${Date.now().toString(36)}-${n++}`;

/** A project-scoped API key (an automation actor): what a script or CI would use. */
async function apiKey(request: APIRequestContext, project: string): Promise<{ id: string; token: string }> {
  const res = await request.post("/api/credentials", { data: { name: key("ci"), scope: { project } }, headers: { "Idempotency-Key": key("crd") } });
  expect(res.status(), await res.text()).toBe(201);
  const body = await res.json();
  return { id: body.credential.id, token: body.token };
}

/** A request context that is only the bearer of the token (no admin cookie). */
async function asToken(playwright: PlaywrightWorkerArgs["playwright"], baseURL: string | undefined, token: string): Promise<APIRequestContext> {
  return playwright.request.newContext({ baseURL, storageState: { cookies: [], origins: [] }, extraHTTPHeaders: { Authorization: `Bearer ${token}` } });
}

async function projectRev(request: APIRequestContext, slug: string): Promise<number> {
  const res = await request.get(`/api/projects/${slug}`);
  return (await res.json()).rev;
}

function card(page: Page, id: string) {
  return page.locator(`[data-approval-card="${id}"]`);
}

test.describe("approvals", () => {
  test("an automation's gated request appears live, is approved with Enter and runs", async ({ page, request, playwright, baseURL }) => {
    const slug = await newProject(request, "Approvals");
    await openWorkspace(page, slug, "Ops");
    // Chat leads Ops' right column (11 "Default workspaces"); Approvals is the next tab.
    await page.locator('[data-tab="approvals"]').click();
    const panel = page.locator('[data-testid="approvals-pending-count"]');
    await expect(panel).toBeVisible();

    // A project-scoped key is an automation actor: archiving is gated for it (preset rule archive-project).
    const { token } = await apiKey(request, slug);
    const ci = await asToken(playwright, baseURL, token);
    const res = await ci.post(`/api/projects/${slug}:archive`, { headers: { "Idempotency-Key": key("archive"), "If-Match": `"${await projectRev(request, slug)}"` } });
    expect(res.status(), await res.text()).toBe(202);
    const { approvalId } = await res.json();
    await ci.dispose();

    // Live: the card, the badge and the polite announcement arrive from the approvals topic.
    const c = card(page, approvalId);
    await expect(c).toBeVisible();
    await expect(c).toContainText("projects.archive");
    await expect(c).toContainText("Automation ·");
    await expect(c).toContainText("archive-project");
    await expect(c).toContainText(/Expires in 2[34] h/);
    await expect(page.getByTestId("live-region")).toContainText("Approval requested: projects.archive");
    await expect(page.getByTestId("approvals-badge")).toContainText(/Approvals\s*\d+/);

    await c.focus();
    await page.keyboard.press("Enter");
    await expect(c).toHaveAttribute("data-state", "approved");
    await expect(c).toContainText("Approved once by admin");
    await expect(c).toContainText("Replay answered 200");
    const project = await (await request.get(`/api/projects/${slug}`)).json();
    expect(project.archivedAt).toBeTruthy();

    // Notification history keeps both; its link opens Approvals.
    await page.getByRole("button", { name: /^Notifications/ }).click();
    const history = page.getByRole("dialog");
    await expect(history).toContainText("Approval requested: projects.archive");
    await expect(history).toContainText("Approved: projects.archive");
    await page.keyboard.press("Escape");
  });

  test("moving @baseline waits for a person; Backspace denies it with a note", async ({ page, request }) => {
    const slug = await newProject(request, "Baseline");
    // The wizard adopted the project's base model at creation; @baseline may point at it.
    const version = (await (await request.get(`/api/projects/${slug}`)).json()).baseModel?.versionId as string;
    expect(version).toBeTruthy();
    const adoptions = await (await request.get(`/api/projects/${slug}/adoptions`)).json();
    expect(adoptions.items.map((a: { version: { id: string } }) => a.version.id)).toContain(version);
    const gated = await request.put(`/api/projects/${slug}/aliases/baseline`, { data: { version }, headers: { "Idempotency-Key": key("baseline") } });
    expect(gated.status(), await gated.text()).toBe(202);
    const { approvalId } = await gated.json();

    await openWorkspace(page, slug);
    // The status-bar badge opens a small popup like Notifications; its expand button opens Approvals floating.
    await page.getByTestId("approvals-badge").click();
    const popup = page.locator('[data-slot="status-popover"]');
    await expect(popup.locator(`[data-approval="${approvalId}"]`)).toContainText("aliases.set");
    await popup.getByRole("button", { name: "Open Approvals as a window" }).click();
    await expect(popup).toHaveCount(0);
    await expect(page.locator(".dv-resize-container [data-approval-card]").first()).toBeVisible();
    const c = card(page, approvalId);
    await expect(c).toContainText("aliases.set");
    await expect(c).toContainText("baseline-alias");
    await expect(c.getByRole("button", { name: "Approve for this session" })).toBeDisabled();
    await c.getByRole("button", { name: "Add note" }).click();
    await c.getByRole("textbox").fill("keep the current baseline until the phone golden set is frozen");
    await c.focus();
    await page.keyboard.press("Backspace");
    await expect(c).toHaveAttribute("data-state", "denied");
    await expect(c).toContainText("Denied by admin");
    await expect(c).toContainText("keep the current baseline");
    expect((await request.get(`/api/projects/${slug}/aliases/baseline`)).status()).toBe(404);
  });
});

async function openSettings(page: Page, section: string) {
  await page.getByTestId("user-menu").click();
  await page.getByRole("menuitem", { name: "Settings" }).click();
  await page.getByRole("tab", { name: section }).click();
}

test.describe("settings", () => {
  test("create an API key (token shown once), use it, revoke it inline", async ({ page, request, playwright, baseURL }) => {
    const slug = await newProject(request, "Keys");
    await openWorkspace(page, slug);
    await openSettings(page, "Credentials");
    const name = key("key");
    const form = page.getByRole("form", { name: "Create API key" });
    await form.getByLabel("Name").fill(name);
    const sessions = form.getByLabel("May run agent sessions in the project");
    await expect(sessions).toBeDisabled(); // agent sessions run in a project
    await form.getByLabel("Project", { exact: true }).selectOption(slug);
    await sessions.check();
    await form.getByRole("button", { name: "Create API key" }).click();
    const once = page.getByTestId("token-once");
    const token = await once.getByLabel("API key token").inputValue();
    expect(token).toMatch(/^cdk_/);

    const ci = await asToken(playwright, baseURL, token);
    expect((await ci.get(`/api/projects/${slug}`)).status()).toBe(200);
    await once.getByRole("button", { name: "I have stored it" }).click();
    await expect(once).toHaveCount(0);

    const row = page.getByTestId(`credential-${name}`);
    await expect(row).toContainText("agent sessions");
    await expect(row).toContainText("API key");
    await row.getByRole("button", { name: "Revoke" }).click();
    await row.getByRole("button", { name: `Revoke ${name}` }).click();
    await expect(row).toHaveCount(0);
    expect((await ci.get(`/api/projects/${slug}`)).status()).toBe(401);
    await ci.dispose();
    await expect(page.getByTestId("live-region")).toContainText(`Credential revoked: ${name}`);
  });

  test("Why this default? explains a policy; a popover from the floating Settings does not fade it", async ({ page, request }) => {
    const slug = await newProject(request, "Why");
    await openWorkspace(page, slug);
    await openSettings(page, "Policies");
    await expect(page.locator(".dv-resize-container")).toHaveCount(1);
    await page.getByRole("button", { name: "Why this default? (GPU-hours per project per day)" }).click();
    const pop = page.getByRole("dialog").filter({ hasText: "Why this default?" });
    await expect(pop).toContainText("Cadence recommendation");
    await expect(pop).toContainText("0–192 GPU-h");
    await expect(page.locator(".cadence-float-yield")).toHaveCount(0);
    await page.keyboard.press("Escape");
  });

  test("a compute cap edited meanwhile answers 412; the form offers both ways out", async ({ page, request }) => {
    const slug = await newProject(request, "Compute");
    await openWorkspace(page, slug);
    await openSettings(page, "Compute");
    const host = page.getByTestId("compute-host-staging");
    await host.getByRole("button", { name: "Edit card 0" }).click();
    const dialog = page.getByTestId("compute-card-dialog-0");
    await dialog.getByLabel("Memory cap of card 0 (GB)").fill("30");

    // Someone else (another tab, the API) changes the host meanwhile.
    const current = await (await request.get("/api/compute/staging")).json();
    const other = await request.patch("/api/compute/staging", {
      data: { cards: [{ index: 0, memoryCapGb: 28 }] },
      headers: { "Idempotency-Key": key("compute"), "If-Match": `"${current.rev}"` },
    });
    expect(other.status(), await other.text()).toBe(200);

    await dialog.getByRole("button", { name: "Save" }).click();
    const conflict = page.getByTestId("compute-conflict");
    await expect(conflict).toContainText(`it is now rev ${current.rev + 1}`);
    await conflict.getByRole("button", { name: `Apply mine on rev ${current.rev + 1}` }).click();
    await expect(conflict).toHaveCount(0);
    await expect(dialog).toHaveCount(0);
    await expect(host.getByRole("button", { name: "Edit card 0" })).toBeFocused();
    await expect(host).toContainText("30 GB");
    const after = await (await request.get("/api/compute/staging")).json();
    expect(after.cards[0].memoryCapGb).toBe(30);

    // Put the seeded cap back for the rest of the run.
    await request.patch("/api/compute/staging", { data: { cards: [{ index: 0, memoryCapGb: 24 }] }, headers: { "Idempotency-Key": key("compute"), "If-Match": `"${after.rev}"` } });
  });

  test("Getting started ticks from data and can be dismissed", async ({ page, request }) => {
    const slug = await newProject(request, "Start");
    await openWorkspace(page, slug);
    await page.locator('[data-tab="getting-started"]').click();
    const steps = page.getByRole("list", { name: "Setup steps" });
    await expect(steps.locator('[data-step="admin"]')).toHaveAttribute("data-state", "done");
    await expect(steps.locator('[data-step="project"]')).toHaveAttribute("data-state", "done");
    await expect(steps.locator('[data-step="run"]')).toContainText("phase 2");
    await page.getByRole("button", { name: "Dismiss" }).click();
    await expect(page.locator('[data-tab="getting-started"]')).toHaveCount(0);
    await runCommand(page, "Reset workspace to default");
    await expect(page.locator('[data-tab="inspector"]')).toBeVisible();
    await expect(page.locator('[data-tab="getting-started"]')).toHaveCount(0);
  });
});
