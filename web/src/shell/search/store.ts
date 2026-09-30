import { create } from "zustand";
import type { SearchHit } from "@/api/gen/types.gen";
import type { EntityData } from "@/shell/entity/manifest";
import { hitToEntity } from "./hits";

// Search state shared by the palette and the Library: the query the Library lists ("Open as list", a saved view)
// and a preview cache so the Inspector can show a hit whose kind has no entity manifest yet.

const PREVIEW_LIMIT = 200;

type SearchState = {
  /** The Library's query in the search language; empty = browse the registry. */
  libraryQuery: string;
  /** The saved view the Library shows, if its query is unchanged since it was opened. */
  activeView: string | null;
  /** Document reference → what the Inspector shows for it. Oldest entries are dropped past PREVIEW_LIMIT. */
  previews: Record<string, EntityData>;
  setLibraryQuery(query: string, view?: string | null): void;
  remember(hits: SearchHit[]): void;
};

export const useSearch = create<SearchState>((set) => ({
  libraryQuery: "",
  activeView: null,
  previews: {},
  setLibraryQuery: (query, view = null) => set({ libraryQuery: query, activeView: view }),
  remember: (hits) =>
    set((s) => {
      if (hits.length === 0) return s;
      const previews = { ...s.previews };
      for (const h of hits) {
        delete previews[h.ref]; // re-insert so the newest are last
        previews[h.ref] = hitToEntity(h);
      }
      const keys = Object.keys(previews);
      for (const k of keys.slice(0, Math.max(0, keys.length - PREVIEW_LIMIT))) delete previews[k];
      return { previews };
    }),
}));

/** The cached preview of a document reference, if a search returned it. */
export function usePreview(doc: string | null): EntityData | undefined {
  return useSearch((s) => (doc ? s.previews[doc] : undefined));
}
