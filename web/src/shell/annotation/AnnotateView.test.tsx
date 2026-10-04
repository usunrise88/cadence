import { afterEach, describe, expect, it } from "vitest";
import { cleanup, render, screen } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { authGetQueryKey, batchItemsListQueryKey } from "@/api/gen/@tanstack/react-query.gen";
import type { BatchItem } from "@/api/gen/types.gen";
import { AnnotateView } from "./AnnotateView";
import { shownItem } from "./model";

afterEach(() => cleanup());

const item = (id: string, position: number, annotator?: string) =>
  ({
    id,
    batchId: "anb_1",
    position,
    state: annotator ? "agreed" : "pending",
    rev: 1,
    segment: { hash: "b3:x", uri: "mount://corpora/c.wav#t=5,9&ch=0", start: 5, end: 9, duration: 4, channel: 0, role: "caller" },
    window: { start: 3, end: 11, channels: 2, roles: ["caller", "bot"] },
    prefill: { text: "dobar dan", origin: "pseudo-label" },
    context: { turns: [] },
    strata: {},
    double: false,
    required: 1,
    annotations: annotator
      ? [{ id: `ann_${id}`, itemId: id, annotator: { kind: "user", id: annotator }, status: "done", text: "Dobar dan", tags: [], entities: [], createdAt: "", updatedAt: "" }]
      : [],
  }) as unknown as BatchItem;

describe("the item the Annotate view shows", () => {
  const done = item("bit_1", 1, "usr_a");
  const open = item("bit_2", 2);
  it("is the picked one, else the server's next, else none — never a done item by default", () => {
    expect(shownItem([done, open], "bit_2", undefined)?.id).toBe("bit_2");
    expect(shownItem([done, open], "bit_2", "bit_1")?.id).toBe("bit_1"); // a person reopens their own annotation
    expect(shownItem([done], undefined, undefined)).toBeUndefined();
    expect(shownItem([], undefined, undefined)).toBeUndefined();
  });
});

describe("AnnotateView with an empty queue", () => {
  it("says there is nothing left to annotate instead of showing a done item", () => {
    const qc = new QueryClient({ defaultOptions: { queries: { retry: false, staleTime: Infinity } } });
    qc.setQueryData(authGetQueryKey(), { actor: { kind: "user", id: "usr_a" } } as never);
    qc.setQueryData(batchItemsListQueryKey({ path: { id: "anb_1" }, query: { queue: "mine" } }), { items: [item("bit_1", 1, "usr_a"), item("bit_2", 2, "usr_a")] });
    render(
      <QueryClientProvider client={qc}>
        <AnnotateView batchId="anb_1" />
      </QueryClientProvider>,
    );
    expect(screen.getByText("0 to annotate · 2 done by you")).toBeTruthy();
    expect(screen.getByText(/Nothing left to annotate in this batch\./)).toBeTruthy();
    expect(document.querySelector('[data-slot="annotate-item"]')).toBeNull();
  });
});
