import { chromium, expect, type FullConfig } from "@playwright/test";
import { ADMIN_PASSWORD, ADMIN_USER, STORAGE_STATE } from "./auth";

// Every run starts on a fresh database: the first start happens here, through the UI (so the first-start screen is
// exercised), and the signed-in state is saved for the specs (config `use.storageState`). A reused stack that is
// already set up is signed in through the API instead.
export default async function globalSetup(config: FullConfig): Promise<void> {
  const baseURL = config.projects[0]?.use.baseURL ?? `http://127.0.0.1:${process.env.E2E_WEB_PORT ?? 5174}`;
  const browser = await chromium.launch();
  const context = await browser.newContext({ baseURL });
  const page = await context.newPage();
  const status = await (await context.request.get("/api/auth")).json();
  if (status.setupRequired) {
    await page.goto("/");
    // The first page load of a cold Vite dev server optimises dependencies (and may reload): allow for it.
    await expect(page.getByRole("heading", { name: "Set up the admin account" })).toBeVisible({ timeout: 90_000 });
    await page.getByLabel("Password", { exact: true }).fill(ADMIN_PASSWORD);
    await page.getByLabel("Repeat the password").fill(ADMIN_PASSWORD);
    await page.getByRole("button", { name: "Create the admin account" }).click();
    // Signed in: the setup screen gives way to the app (an error would stay on it, in role=alert).
    await expect(page.getByRole("heading", { name: "Set up the admin account" })).toHaveCount(0, { timeout: 15_000 });
  } else {
    const res = await context.request.post("/api/auth:login", {
      data: { username: ADMIN_USER, password: ADMIN_PASSWORD },
      headers: { "Cadence-Client": "web" },
    });
    expect(res.status(), await res.text()).toBe(200);
  }
  await context.storageState({ path: STORAGE_STATE });
  await browser.close();
}
