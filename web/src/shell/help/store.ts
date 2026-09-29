import { create } from "zustand";

// The Help panel follows the focused panel unless pinned to an article (docs/spec/11-ui-panels.md "Help").
type HelpState = {
  /** An explicitly opened article (from an error, the palette or a link); null = follow the focused panel. */
  article: string | null;
  pinned: boolean;
  /** Help id of the last focused panel other than Help itself. */
  focusedHelp: string | null;
  show(article: string): void;
  follow(): void;
  setPinned(p: boolean): void;
  setFocusedHelp(id: string | null): void;
};

export const useHelp = create<HelpState>((set) => ({
  article: null,
  pinned: false,
  focusedHelp: null,
  show: (article) => set({ article, pinned: true }),
  follow: () => set({ article: null, pinned: false }),
  setPinned: (pinned) => set({ pinned }),
  setFocusedHelp: (focusedHelp) => set((s) => (s.focusedHelp === focusedHelp ? s : { focusedHelp })),
}));
