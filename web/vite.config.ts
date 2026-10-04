import path from "node:path";
import tailwindcss from "@tailwindcss/vite";
import react from "@vitejs/plugin-react";
import { playwright } from "@vitest/browser-playwright";
import { defineConfig } from "vitest/config";

// The control plane serves the built SPA (embedded in the Go binary) and the API under /api.
// In development Vite serves the SPA and proxies the API.
const alias = { "@": path.resolve(import.meta.dirname, "./src") };

export default defineConfig({
  plugins: [react(), tailwindcss()],
  resolve: { alias },
  server: {
    port: 5173,
    proxy: { "/api": { target: process.env.CADENCE_API ?? "http://127.0.0.1:8080", changeOrigin: false } },
  },
  build: { outDir: "dist", sourcemap: true },
  test: {
    restoreMocks: true,
    projects: [
      {
        resolve: { alias },
        test: {
          name: "unit",
          environment: "jsdom",
          setupFiles: ["src/test/setup-jsdom.ts"],
          include: ["src/**/*.test.{ts,tsx}", "eslint-rules/**/*.test.ts", "scripts/**/*.test.ts"],
          exclude: ["src/**/*.browser.test.{ts,tsx}"],
        },
      },
      {
        // Contract tests that need real layout (Dockview geometry): headless Chromium.
        resolve: { alias },
        test: {
          name: "browser",
          include: ["src/**/*.browser.test.{ts,tsx}"],
          browser: { enabled: true, provider: playwright({
              // The capture test: the fake microphone (a tone), the permission granted without a prompt, and an
              // AudioContext that runs without the click the panel's Go live / Check gives it.
              launchOptions: { args: ["--use-fake-device-for-media-stream", "--use-fake-ui-for-media-stream", "--autoplay-policy=no-user-gesture-required"] },
            }), headless: true, instances: [{ browser: "chromium" }] },
        },
      },
    ],
  },
});
