import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { RouterProvider } from "@tanstack/react-router";
import { configureApiClient } from "@/api/client";
import { router } from "@/app/router";
import { registerEntities } from "@/entities";
import { registerPanels } from "@/panels";
import { registerBuiltinCommands } from "@/shell/commands/builtin";
import { registerSpikePanels } from "@/spikes/stubs";
import { dockApi } from "@/shell/dock/store";
import { commands, events, panels } from "@/shell/registries";
import { installTheme } from "@/shell/theme/store";
import { restoreWorkspace, useWorkspaceSync } from "@/shell/workspaces/persistence";
import { openPanel } from "@/shell/dock/layout";
import "@/styles/index.css";

configureApiClient();
registerEntities();
registerPanels();
const spikes = new URLSearchParams(location.search).get("spikes");
if (spikes !== null) registerSpikePanels(spikes === "always" ? "always" : "onlyWhenVisible");
registerBuiltinCommands();
installTheme(document);

const queryClient = new QueryClient({
  defaultOptions: { queries: { staleTime: 30_000, retry: 1, refetchOnWindowFocus: false } },
});

// Test and spike hooks (Playwright, docs/spikes): read-only handles, only in development or with cadence.debug=1.
let debug = import.meta.env.DEV;
try {
  debug ||= localStorage.getItem("cadence.debug") === "1";
} catch {
  /* storage unavailable */
}
if (debug) {
  (window as unknown as { __cadence: unknown }).__cadence = { events, commands, panels, dock: dockApi, sync: useWorkspaceSync, queryClient, openPanel, restoreWorkspace };
}

createRoot(document.getElementById("root")!).render(
  <StrictMode>
    <QueryClientProvider client={queryClient}>
      <RouterProvider router={router} />
    </QueryClientProvider>
  </StrictMode>,
);
