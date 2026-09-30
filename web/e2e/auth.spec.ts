import { createHmac } from "node:crypto";
import { ADMIN_PASSWORD, ADMIN_USER, STORAGE_STATE } from "./auth";
import { expect, newProject, test } from "./fixtures";

// Sign-in, sign-out, a lost session, and the second factor. First start is covered by global-setup.ts, which
// creates the admin through the first-start screen.

const stepNow = () => Math.floor(Date.now() / 30_000);

/** RFC 6238 code (HMAC-SHA1, six digits, 30 s steps) of a base32 secret at time step `step`. */
function totp(secret: string, step: number): string {
  const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZ234567";
  let bits = "";
  for (const ch of secret.replace(/=+$/, "")) bits += alphabet.indexOf(ch).toString(2).padStart(5, "0");
  const key = Buffer.from(bits.match(/.{8}/g)!.map((b) => parseInt(b, 2)));
  const msg = Buffer.alloc(8);
  msg.writeBigUInt64BE(BigInt(step));
  const mac = createHmac("sha1", key).update(msg).digest();
  const off = mac[mac.length - 1]! & 0x0f;
  return String((mac.readUInt32BE(off) & 0x7fffffff) % 1_000_000).padStart(6, "0");
}

test.describe("sign-in", () => {
  // These tests start signed out; the rest of the suite runs with the admin's saved session.
  test.use({ storageState: { cookies: [], origins: [] } });

  test("sign in at a deep link, lose the session, sign out", async ({ page, playwright, baseURL }) => {
    const admin = await playwright.request.newContext({ baseURL, storageState: STORAGE_STATE, extraHTTPHeaders: { "Cadence-Client": "web" } });
    const slug = await newProject(admin, "Auth");
    await admin.dispose();

    await page.goto(`/p/${slug}/w/Training`);
    const heading = page.getByRole("heading", { name: "Sign in" });
    await expect(heading).toBeVisible();
    await page.locator('input[name="username"]').fill(ADMIN_USER);
    await page.locator('input[name="password"]').fill("not the password");
    await page.getByRole("button", { name: "Sign in" }).click();
    await expect(page.getByRole("alert")).toContainText("wrong username, password or code");

    await page.locator('input[name="password"]').fill(ADMIN_PASSWORD);
    await page.getByRole("button", { name: "Sign in" }).click();
    await expect(page.locator('[data-tab="library"]')).toBeVisible();
    await expect(page).toHaveURL(new RegExp(`/p/${slug}/w/Training`));
    await expect(page.getByTestId("user-menu")).toHaveText(/admin/);

    // The session disappears (expired, revoked elsewhere): the next API call answers 401 and sign-in returns.
    await page.context().clearCookies();
    await page.evaluate(() =>
      (window as unknown as { __cadence: { queryClient: { invalidateQueries(): Promise<void> } } }).__cadence.queryClient.invalidateQueries(),
    );
    await expect(heading).toBeVisible();
    await page.locator('input[name="username"]').fill(ADMIN_USER);
    await page.locator('input[name="password"]').fill(ADMIN_PASSWORD);
    await page.getByRole("button", { name: "Sign in" }).click();
    await expect(page.locator('[data-tab="library"]')).toBeVisible();

    await page.getByTestId("user-menu").click();
    await page.getByRole("menuitem", { name: "Sign out" }).click();
    await expect(heading).toBeVisible();
    await page.reload();
    await expect(heading).toBeVisible();
  });

  test("two-factor: turn on, sign in with a code, turn off", async ({ page, playwright, baseURL }) => {
    test.setTimeout(120_000); // may wait for the next 30-second TOTP step
    const admin = await playwright.request.newContext({ baseURL, storageState: STORAGE_STATE, extraHTTPHeaders: { "Cadence-Client": "web" } });
    const slug = await newProject(admin, "TOTP");
    await admin.dispose();
    const signIn = async (code?: string) => {
      await page.locator('input[name="username"]').fill(ADMIN_USER);
      await page.locator('input[name="password"]').fill(ADMIN_PASSWORD);
      await page.getByRole("button", { name: "Sign in" }).click();
      if (code === undefined) return;
      await page.locator('input[name="totpCode"]').fill(code);
      await page.getByRole("button", { name: "Sign in" }).click();
    };

    await page.goto(`/p/${slug}/w/Training`);
    await signIn();
    await page.getByTestId("user-menu").click();
    await page.getByRole("menuitem", { name: /Two-factor authentication/ }).click();
    const dialog = page.getByRole("dialog");
    await dialog.getByRole("button", { name: "Set up" }).click();
    // The key arrives as a QR code to scan from the screen; typing it is the fallback (grouped by four).
    await expect(dialog.getByRole("img", { name: /QR code with the two-factor key/ })).toBeVisible();
    await dialog.getByText("Can’t scan? Type the key instead").click();
    const secret = (await dialog.getByTestId("totp-secret").textContent())!.replace(/\s/g, "");
    const s0 = stepNow();
    await dialog.locator('input[name="code"]').fill(totp(secret, s0));
    await dialog.getByRole("button", { name: "Confirm and turn on" }).click();
    await expect(dialog).toHaveCount(0);

    await page.getByTestId("user-menu").click();
    await page.getByRole("menuitem", { name: "Sign out" }).click();
    // Each code works once: the next step's code (accepted within the one-step drift window) signs in.
    await signIn(totp(secret, s0 + 1));
    await expect(page.locator('[data-tab="library"]')).toBeVisible();

    await page.getByTestId("user-menu").click();
    await page.getByRole("menuitem", { name: /Two-factor authentication/ }).click();
    // Step s0 + 1 is used now; s0 + 2 is accepted once the clock reaches s0 + 1 (drift window).
    while (stepNow() < s0 + 1) await page.waitForTimeout(500);
    await page.getByRole("dialog").locator('input[name="code"]').fill(totp(secret, s0 + 2));
    await page.getByRole("dialog").getByRole("button", { name: "Turn off" }).click();
    await expect(page.getByRole("dialog")).toHaveCount(0);
    await page.getByTestId("user-menu").click();
    await expect(page.getByRole("menuitem", { name: /Two-factor authentication/ })).toContainText("Off");
  });
});
