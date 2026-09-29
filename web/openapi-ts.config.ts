import { defineConfig } from "@hey-api/openapi-ts";

// Generated client: `make gen` (or `npm run gen`). Never edit src/api/gen by hand.
export default defineConfig({
  input: "../api/openapi.yaml",
  output: { path: "src/api/gen" },
  plugins: [
    "@hey-api/client-fetch",
    "@hey-api/typescript",
    { name: "@hey-api/sdk" },
    "@tanstack/react-query",
  ],
});
