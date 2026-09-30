import { openPanel } from "@/shell/dock/layout";
import { parseDocRef } from "@/shell/entity/manifest";
import { useHelp } from "@/shell/help/store";
import { panels } from "@/shell/registries";
import { useSelection } from "@/shell/selection/store";
import { useSearch } from "./store";

// Where a search hit goes (docs/spec/11-ui-panels.md "Search" · Behaviour): Enter opens the document, Space previews
// it in the Inspector, "Open as list" hands the query to the Library.

/** Previews a document reference in the Inspector without opening it. */
export function previewRef(ref: string): void {
  useSelection.getState().setActiveDoc(ref);
  openPanel("inspector", { inactive: true });
}

/**
 * Opens a hit: help articles in the Help panel, kinds with a document panel as that document, anything else as a
 * preview in the Inspector (their documents arrive with their phases).
 */
export function openRef(ref: string): void {
  const parsed = parseDocRef(ref);
  if (!parsed) return;
  if (parsed.kind === "help_article") {
    useHelp.getState().show(parsed.id);
    openPanel("help");
    return;
  }
  const doc = panels.all().find((p) => p.kind === "document" && p.entity === parsed.kind);
  if (doc) {
    openPanel(doc.id, { doc: ref });
    return;
  }
  useSelection.getState().setActiveDoc(ref);
  openPanel("inspector");
}

/** Shows a query (or a saved view) as a list in the Library. */
export function openInLibrary(query: string, view: string | null = null): void {
  useSearch.getState().setLibraryQuery(query, view);
  openPanel("library");
}
