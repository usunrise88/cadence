import { create } from "zustand";

// Modal flows opened by commands (the wizard, confirmations). One at a time.
export type DialogRequest =
  | { kind: "newProject" }
  | { kind: "editProject"; slug: string }
  | { kind: "confirm"; title: string; detail?: string; confirmLabel: string; onConfirm: () => Promise<unknown> | void }
  | { kind: "shortcuts" }
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
