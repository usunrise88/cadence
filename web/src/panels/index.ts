import { panels } from "@/shell/registries";
import type { PanelManifest } from "@/shell/registry/panels";

// Every panel directory registers through its manifest's default export; nothing else imports a panel.
const manifests = import.meta.glob<{ default: PanelManifest }>("./*/manifest.ts", { eager: true });

export function registerPanels(): void {
  for (const mod of Object.values(manifests)) panels.register(mod.default);
}
