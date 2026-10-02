import { defineConfig, devices } from "@playwright/test";

// Spike S5 measurements: headless Chromium against the spike's own Vite server (no control plane needed).
// Run through docs/spikes/s5/run.sh (the pinned Playwright image). Tracing off: it drops frames (S1).
const PORT = Number(process.env.S5_PORT ?? 5175);

export default defineConfig({
  testDir: ".",
  testMatch: "s5.spec.ts",
  timeout: 600_000,
  workers: 1,
  reporter: "list",
  outputDir: "../../../test-results/s5/playwright",
  use: {
    baseURL: `http://127.0.0.1:${PORT}`,
    viewport: { width: 1400, height: 900 },
    trace: "off",
    launchOptions: {
      // No GPU in the container: WebGL2 runs on SwiftShader (software), a pessimistic bound for real GPUs.
      args: ["--enable-unsafe-swiftshader", "--ignore-gpu-blocklist", "--autoplay-policy=no-user-gesture-required", "--enable-precise-memory-info"],
    },
  },
  projects: [{ name: "chromium", use: { ...devices["Desktop Chrome"], viewport: { width: 1400, height: 900 } } }],
  webServer: {
    command: `../../../node_modules/.bin/vite --config vite.config.ts --port ${PORT}`,
    url: `http://127.0.0.1:${PORT}`,
    timeout: 120_000,
    reuseExistingServer: false,
  },
});
