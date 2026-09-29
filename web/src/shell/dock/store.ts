import type { DockviewApi } from "dockview-react";
import { create } from "zustand";

// Holds the one Dockview API instance. Only shell/dock and the adapter use it; panels and commands go through
// the functions in ./layout.ts.

type DockState = {
  api: DockviewApi | null;
  /** Tool panels hidden by "Hide or show all tool panels"; restored on the next toggle. */
  hiddenTools: string[] | null;
  setApi(api: DockviewApi | null): void;
  setHiddenTools(ids: string[] | null): void;
};

export const useDock = create<DockState>((set) => ({
  api: null,
  hiddenTools: null,
  setApi: (api) => set({ api }),
  setHiddenTools: (hiddenTools) => set({ hiddenTools }),
}));

export function dockApi(): DockviewApi | null {
  return useDock.getState().api;
}
