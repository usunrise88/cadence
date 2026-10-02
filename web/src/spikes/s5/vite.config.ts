import path from "node:path";
import react from "@vitejs/plugin-react";
import { defineConfig } from "vite";

// Spike S5 harness: its own Vite root, never reachable from the product's index.html (web/vite.config.ts).
// React, Dockview and Vite resolve from web/node_modules; the spike-only packages from ./node_modules.
const here = import.meta.dirname;
export default defineConfig({
  root: here,
  plugins: [react()],
  resolve: {
    alias: {
      // The package's exports map hides the .wasm file; the glue and the binary are imported by path.
      "@pffft": path.resolve(here, "node_modules/@echogarden/pffft-wasm/dist/simd"),
    },
  },
  optimizeDeps: { exclude: ["@echogarden/pffft-wasm"] },
  worker: { format: "es" },
  server: { port: Number(process.env.S5_PORT ?? 5175), strictPort: true, host: "127.0.0.1", fs: { allow: [path.resolve(here, "../../..")] } },
  build: { outDir: path.resolve(here, "../../../test-results/s5/dist"), emptyOutDir: true },
});
