// @vitest-environment node
import path from "node:path";
import { ESLint } from "eslint";
import { describe, expect, it } from "vitest";

// The architecture rules must actually fire: each case lints a snippet as if it lived at `file`.
const eslint = new ESLint({ cwd: path.resolve(import.meta.dirname, "..") });

async function messages(file: string, code: string): Promise<string[]> {
  const [res] = await eslint.lintText(code, { filePath: path.resolve(import.meta.dirname, "..", file) });
  return (res?.messages ?? []).map((m) => `${m.ruleId}: ${m.message}`);
}

describe("architecture lint rules", () => {
  it.each([
    ["a panel importing another panel", "src/panels/library/X.tsx", `import { InspectorPanel } from "@/panels/inspector/InspectorPanel";\nexport const x = InspectorPanel;\n`, "panels never import each other"],
    ["a relative cross-panel import", "src/panels/library/X.tsx", `import { HelpPanel } from "../help/HelpPanel";\nexport const x = HelpPanel;\n`, "panels never import each other"],
    ["a panel importing Dockview", "src/panels/library/X.tsx", `import { DockviewReact } from "dockview-react";\nexport const x = DockviewReact;\n`, "never talk to Dockview"],
    ["the shell importing Dockview values outside dock/", "src/shell/chrome/X.tsx", `import { DockviewReact } from "dockview-react";\nexport const x = DockviewReact;\n`, "Only shell/dock"],
    ["asChild", "src/shell/chrome/X.tsx", `export const x = <div asChild />;\n`, "render"],
    ["a hand-drawn header in a panel", "src/panels/library/X.tsx", `export const x = <header>Run 12</header>;\n`, "EntityHeader"],
    ["an h2 in a panel", "src/panels/library/X.tsx", `export const x = <h2>Run 12</h2>;\n`, "EntityHeader"],
    ["fetch in a panel", "src/panels/library/X.tsx", `export const x = () => fetch("/api/projects");\n`, "generated query layer"],
    ["the SDK in a panel", "src/panels/library/X.tsx", `import { projectsList } from "@/api/gen/sdk.gen";\nexport const x = projectsList;\n`, "TanStack Query"],
    ["a colour literal", "src/shell/chrome/X.tsx", `export const c = "#3e63dd";\n`, "No colour literals"],
    ["a panel importing uPlot", "src/panels/library/X.tsx", `import uPlot from "uplot";\nexport const x = uPlot;\n`, "use @/shell/charts"],
    ["a panel importing ECharts", "src/panels/library/X.tsx", `import { init } from "echarts/core";\nexport const x = init;\n`, "use @/shell/charts"],
    ["a panel importing a charts file", "src/panels/library/X.tsx", `import { ema } from "@/shell/charts/math";\nexport const x = ema;\n`, "not its files"],
    ["the shell importing ECharts outside shell/charts", "src/shell/chrome/X.tsx", `import * as echarts from "echarts";\nexport const x = echarts;\n`, "only that module imports"],
    ["a component importing uPlot", "src/components/X.tsx", `import uPlot from "uplot";\nexport const x = uPlot;\n`, "only that module imports"],
    ["a default export outside a manifest", "src/shell/chrome/X.tsx", `const a = 1;\nexport default a;\n`, "No default exports"],
  ])("rejects %s", async (_name, file, code, want) => {
    const msgs = await messages(file, code);
    expect(msgs.join("\n")).toContain(want);
  });

  it("allows @/shell/charts in panels and the chart libraries inside it", async () => {
    expect(await messages("src/panels/library/X.tsx", `import { TimeSeriesChart } from "@/shell/charts";\nexport const x = TimeSeriesChart;\n`)).toEqual([]);
    expect(await messages("src/shell/charts/X.ts", `import uPlot from "uplot";\nimport { init } from "echarts/core";\nexport const x = [uPlot, init];\n`)).toEqual([]);
  });

  it("allows type-only Dockview imports in the shell and panels' own files", async () => {
    expect(await messages("src/shell/chrome/X.ts", `import type { DockviewApi } from "dockview-react";\nexport type A = DockviewApi;\n`)).toEqual([]);
    expect(await messages("src/panels/library/Y.tsx", `import { LibraryPanel } from "./LibraryPanel";\nexport const y = LibraryPanel;\n`)).toEqual([]);
  });
});
