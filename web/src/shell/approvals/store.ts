import { create } from "zustand";
import type { Approval } from "@/api/gen/types.gen";

// The approval card that last had focus: the palette's "Approve request" / "Deny request" act on it.
type FocusedApproval = { approval: Approval | null; set(a: Approval | null): void };

export const useFocusedApproval = create<FocusedApproval>((set) => ({
  approval: null,
  set: (approval) => set({ approval }),
}));
