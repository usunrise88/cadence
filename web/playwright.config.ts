import { defineConfig, devices } from "@playwright/test";
import { STORAGE_STATE } from "./e2e/auth";

// Shell behaviour against the real control plane (Postgres in Docker) and the Vite dev server.
const API_PORT = Number(process.env.E2E_API_PORT ?? 18081);
const WEB_PORT = Number(process.env.E2E_WEB_PORT ?? 5174);

export default defineConfig({
  testDir: "e2e",
  timeout: 60_000,
  fullyParallel: false,
  workers: 1,
  reporter: process.env.CI ? [["github"], ["html", { open: "never" }]] : "list",
  // First start and sign-in happen once (e2e/global-setup.ts); every page and API request then carries the admin's
  // session cookie, and API requests the CSRF header the SPA sends (Cadence-Client: web).
  globalSetup: "./e2e/global-setup.ts",
  use: {
    baseURL: `http://127.0.0.1:${WEB_PORT}`,
    viewport: { width: 1400, height: 900 },
    trace: "retain-on-failure",
    storageState: STORAGE_STATE,
    extraHTTPHeaders: { "Cadence-Client": "web" },
  },
  projects: [{ name: "chromium", use: { ...devices["Desktop Chrome"], viewport: { width: 1400, height: 900 } } }],
  webServer: [
    { command: "./e2e/stack.sh", url: `http://127.0.0.1:${API_PORT}/healthz`, timeout: 180_000, reuseExistingServer: false, env: { E2E_API_PORT: String(API_PORT) } },
    {
      command: `npx vite --host 127.0.0.1 --port ${WEB_PORT} --strictPort`,
      url: `http://127.0.0.1:${WEB_PORT}`,
      timeout: 120_000,
      reuseExistingServer: false,
      env: { CADENCE_API: `http://127.0.0.1:${API_PORT}` },
    },
  ],
});
