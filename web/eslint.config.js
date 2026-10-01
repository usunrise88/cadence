import js from "@eslint/js";
import globals from "globals";
import reactHooks from "eslint-plugin-react-hooks";
import tseslint from "typescript-eslint";
import { defineConfig, globalIgnores } from "eslint/config";
import cadence from "./eslint-rules/cadence.js";

const DOCKVIEW = ["dockview", "dockview/*", "dockview-react", "dockview-react/*", "dockview-core", "dockview-core/*"];
// Chart libraries: only @/shell/charts imports them (R53); panels and the rest of the app use the shell module.
const CHART_LIBS = ["uplot", "uplot/*", "echarts", "echarts/*", "zrender", "zrender/*"];
const chartsOnly = (message) => ({ group: CHART_LIBS, message });

export default defineConfig([
  globalIgnores(["dist", "src/api/gen", "src/api/operations.gen.ts", "playwright-report", "test-results"]),
  {
    files: ["**/*.{ts,tsx}"],
    extends: [js.configs.recommended, tseslint.configs.recommended, reactHooks.configs.flat.recommended],
    languageOptions: { globals: globals.browser },
    plugins: { cadence },
    rules: {
      "@typescript-eslint/no-explicit-any": "error",
      "@typescript-eslint/no-unused-vars": ["error", { argsIgnorePattern: "^_", varsIgnorePattern: "^_" }],
      "react-hooks/set-state-in-effect": "off",
      "no-restricted-syntax": [
        "error",
        { selector: "JSXAttribute[name.name='asChild']", message: "Base UI uses the `render` prop, not Radix's `asChild`." },
        { selector: "ExportDefaultDeclaration", message: "No default exports (only panel manifests)." },
        { selector: "Literal[value=/#[0-9a-fA-F]{3}([0-9a-fA-F]{3})?([0-9a-fA-F]{2})?\\b/]", message: "No colour literals; use theme tokens (src/styles/theme.css)." },
      ],
    },
  },
  // Charts: uPlot and ECharts only inside src/shell/charts (the blocks below repeat the group because a later block
  // replaces this rule's options).
  {
    files: ["src/**/*.{ts,tsx}"],
    rules: {
      "@typescript-eslint/no-restricted-imports": ["error", { patterns: [chartsOnly("Charts go through @/shell/charts; only that module imports uPlot or ECharts.")] }],
    },
  },
  // Dockview: only the dock host (public API) and the adapter (internals) may import it; elsewhere in the shell,
  // types only.
  {
    files: ["src/shell/**/*.{ts,tsx}"],
    ignores: ["src/shell/dock/**", "src/shell/floating-snap/dockview-adapter.ts", "src/shell/**/*.test.{ts,tsx}", "src/shell/**/testkit.ts"],
    rules: {
      "@typescript-eslint/no-restricted-imports": [
        "error",
        {
          patterns: [
            { group: DOCKVIEW, allowTypeImports: true, message: "Only shell/dock and floating-snap/dockview-adapter.ts touch Dockview." },
            chartsOnly("Charts go through @/shell/charts; only that module imports uPlot or ECharts."),
          ],
        },
      ],
    },
  },
  {
    files: ["src/shell/charts/**/*.{ts,tsx}"],
    rules: {
      "@typescript-eslint/no-restricted-imports": [
        "error",
        { patterns: [{ group: DOCKVIEW, allowTypeImports: true, message: "Only shell/dock and floating-snap/dockview-adapter.ts touch Dockview." }] },
      ],
    },
  },
  // Panels: plug-ins that know only the shell.
  {
    files: ["src/panels/**/*.{ts,tsx}"],
    rules: {
      "cadence/no-cross-panel-imports": "error",
      "@typescript-eslint/no-restricted-imports": [
        "error",
        {
          patterns: [
            { group: DOCKVIEW, message: "Panels never talk to Dockview; use the shell (src/shell/panel)." },
            { group: ["@/api/gen/sdk.gen", "@/api/gen/client*", "@/api/client"], message: "Panels fetch only through the generated TanStack Query options." },
            { group: ["@/shell/dock/*", "@/shell/floating-snap/*"], message: "Panels use the panel SDK (@/shell/panel), not shell internals." },
            // Allowed shell primitives: @/shell/panel, the entity primitives, @/shell/charts (and @/shell/audio, R51).
            { group: ["@/shell/charts/*"], message: "Panels import charts from @/shell/charts (its index), not its files." },
            chartsOnly("Panels never import uPlot or ECharts; use @/shell/charts (TimeSeriesChart, AnalyticsChart)."),
          ],
        },
      ],
      "no-restricted-globals": [
        "error",
        { name: "fetch", message: "Panels fetch only through the generated query layer." },
        { name: "EventSource", message: "Panels subscribe with useTopic(); the shell owns the one stream." },
      ],
      "no-restricted-syntax": [
        "error",
        { selector: "JSXAttribute[name.name='asChild']", message: "Base UI uses the `render` prop, not Radix's `asChild`." },
        { selector: "JSXOpeningElement[name.name=/^(header|h1|h2)$/]", message: "Document headers are rendered by the shell from the entity manifest (EntityHeader); use h3+ inside a panel." },
        { selector: "JSXAttribute[name.name='role'][value.value='banner']", message: "Document headers are rendered by the shell from the entity manifest." },
        { selector: "Literal[value=/#[0-9a-fA-F]{3}([0-9a-fA-F]{3})?([0-9a-fA-F]{2})?\\b/]", message: "No colour literals; use theme tokens." },
      ],
    },
  },
  {
    files: ["src/panels/*/manifest.ts"],
    rules: { "no-restricted-syntax": "off" },
  },
  // shadcn components are vendored as generated; they follow their upstream style.
  {
    files: ["src/components/ui/**"],
    rules: { "react-refresh/only-export-components": "off" },
  },
  {
    files: ["*.config.{ts,js}", "eslint-rules/**", "scripts/**", "e2e/**"],
    languageOptions: { globals: globals.node },
    rules: { "no-restricted-syntax": "off" },
  },
]);
