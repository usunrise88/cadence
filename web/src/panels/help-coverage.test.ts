import { existsSync } from "node:fs";
import path from "node:path";
import { describe, expect, it } from "vitest";
import type { PanelManifest } from "@/shell/registry/panels";

// CI fails when a panel lacks its help article (docs/spec/11-ui-panels.md "Authoring and sources of truth").
const manifests = import.meta.glob<{ default: PanelManifest }>("./*/manifest.ts", { eager: true });
const HELP = path.resolve(import.meta.dirname, "../../../docs/help");

describe("panel help", () => {
  const all = Object.values(manifests).map((m) => m.default);
  it("finds the panels", () => {
    expect(all.length).toBeGreaterThanOrEqual(4);
  });
  it.each(all.map((m) => [m.id, m.help]))("%s has docs/help for %s", (_id, help) => {
    const [section, slug] = help.split(".");
    expect(existsSync(path.join(HELP, section!, `${slug}.md`))).toBe(true);
  });
});
