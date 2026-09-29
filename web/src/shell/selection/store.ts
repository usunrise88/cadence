import { create } from "zustand";

// Selection bus (docs/spec/10-ui-shell.md "Shell concepts"): one store holds the active document and the selection
// inside it; tool panels follow the active document unless pinned to one.

/** A selection inside a document, e.g. `ckpt:4` or `utt:123`. */
export type Selection = { doc: string; item?: string };
/** What "Ask agent" attaches to a prompt. */
export type Reference = { kind: string; id: string; label?: string };

type SelectionState = {
  activeDoc: string | null;
  /** Selection per document, so switching documents restores the previous selection. */
  selections: Record<string, string | undefined>;
  /** Tool panel instance id → pinned document. */
  pins: Record<string, string>;
  setActiveDoc(doc: string | null): void;
  select(doc: string, item: string | undefined): void;
  pin(instanceId: string, doc: string): void;
  unpin(instanceId: string): void;
  forgetDoc(doc: string): void;
};

export const useSelection = create<SelectionState>((set) => ({
  activeDoc: null,
  selections: {},
  pins: {},
  setActiveDoc: (doc) => set((s) => (s.activeDoc === doc ? s : { activeDoc: doc })),
  select: (doc, item) => set((s) => ({ selections: { ...s.selections, [doc]: item } })),
  pin: (instanceId, doc) => set((s) => ({ pins: { ...s.pins, [instanceId]: doc } })),
  unpin: (instanceId) =>
    set((s) => {
      const pins = { ...s.pins };
      delete pins[instanceId];
      return { pins };
    }),
  forgetDoc: (doc) =>
    set((s) => {
      const selections = { ...s.selections };
      delete selections[doc];
      return { selections, activeDoc: s.activeDoc === doc ? null : s.activeDoc };
    }),
}));

/** The document a tool panel shows: its pin, else the active document. */
export function useFollowedDoc(instanceId: string): { doc: string | null; pinned: boolean } {
  const pinned = useSelection((s) => s.pins[instanceId]);
  const active = useSelection((s) => s.activeDoc);
  return { doc: pinned ?? active, pinned: pinned !== undefined };
}

export function useCurrentSelection(instanceId: string): Selection | null {
  const { doc } = useFollowedDoc(instanceId);
  const item = useSelection((s) => (doc ? s.selections[doc] : undefined));
  return doc ? { doc, item } : null;
}
