import { create } from "zustand";

// Modal flows opened by commands (the wizard, confirmations). One at a time.
export type DialogRequest =
  | { kind: "newProject" }
  | { kind: "editProject"; slug: string }
  | { kind: "confirm"; title: string; detail?: string; confirmLabel: string; onConfirm: () => Promise<unknown> | void }
  | { kind: "shortcuts" }
  | { kind: "twoFactor" }
  | { kind: "palette"; prefix: string };

type DialogState = {
  open: DialogRequest | null;
  show(d: DialogRequest): void;
  close(): void;
};

export const useDialogs = create<DialogState>((set) => ({
  open: null,
  show: (open) => set({ open }),
  close: () => set({ open: null }),
}));

/** The document that last had focus (the main window or a popout): dialogs and the palette open there. */
type FocusState = { doc: Document | null; setDoc(d: Document): void };
export const useFocusedDocument = create<FocusState>((set) => ({
  doc: null,
  setDoc: (doc) => set((s) => (s.doc === doc ? s : { doc })),
}));

export function trackFocus(doc: Document): () => void {
  const on = () => useFocusedDocument.getState().setDoc(doc);
  doc.addEventListener("focusin", on, true);
  doc.addEventListener("pointerdown", on, true);
  return () => {
    doc.removeEventListener("focusin", on, true);
    doc.removeEventListener("pointerdown", on, true);
    if (useFocusedDocument.getState().doc === doc) useFocusedDocument.getState().setDoc(document);
  };
}
