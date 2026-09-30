import { useEffect, useRef } from "react";
import { create } from "zustand";

// A document's editor lives in its panel, its Edit action in the shell's header (and the palette). The command
// asks the open document to take focus here; the panel answers through useEditRequest.

type EditRequests = { seq: Record<string, number>; request(doc: string): void };

export const useEditRequests = create<EditRequests>((set) => ({
  seq: {},
  request: (doc) => set((s) => ({ seq: { ...s.seq, [doc]: (s.seq[doc] ?? 0) + 1 } })),
}));

/** Runs onRequest each time a command asks the document `doc` (kind:id) to start editing. */
export function useEditRequest(doc: string | undefined, onRequest: () => void): void {
  const n = useEditRequests((s) => (doc ? (s.seq[doc] ?? 0) : 0));
  const ref = useRef(onRequest);
  useEffect(() => {
    ref.current = onRequest;
  });
  const seen = useRef(n);
  useEffect(() => {
    if (n === seen.current) return;
    seen.current = n;
    ref.current();
  }, [n]);
}
