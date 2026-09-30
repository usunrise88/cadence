import { defineConfig } from "@hey-api/openapi-ts";

// The control plane's types for the host protocol and the transcript: `make gen` (or `npm run gen`). Types only —
// the host's small fetch client is src/host/api.ts. Never edit src/api/gen by hand.
export default defineConfig({
  input: "../api/openapi.yaml",
  output: { path: "src/api/gen" },
  plugins: ["@hey-api/typescript"],
});
