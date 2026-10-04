import { describe, expect, it } from "vitest";
import type { BatchItem } from "@/api/gen/types.gen";
import { invitationToken } from "./invitation";
import { channelCycle, channelLabel, formOf, keepSpans, segmentSpan, spanOf, toggleTag } from "./model";

const item = {
  id: "bit_1",
  batchId: "anb_1",
  position: 3,
  state: "pending",
  rev: 1,
  segment: { hash: "b3:x", uri: "mount://corpora/c.wav#t=5,9&ch=0", start: 5, end: 9, duration: 4, channel: 0, role: "caller" },
  window: { start: 3, end: 11, channels: 2, roles: ["caller", "bot"] },
  prefill: { text: "dobar dan", origin: "pseudo-label" },
  context: { turns: [] },
  strata: {},
  double: false,
  required: 1,
  annotations: [],
} as unknown as BatchItem;

describe("annotate model", () => {
  it("starts from the caller's own annotation, else the prefill", () => {
    expect(formOf(item, "usr_a")).toEqual({ text: "dobar dan", tags: [], entities: [] });
    const own = { ...item, annotations: [{ id: "ann_1", itemId: "bit_1", annotator: { kind: "user", id: "usr_a" }, status: "done", text: "Dobar dan!", tags: ["noise"], entities: [], createdAt: "", updatedAt: "" }] } as unknown as BatchItem;
    expect(formOf(own, "usr_a")).toEqual({ text: "Dobar dan!", tags: ["noise"], entities: [] });
    expect(formOf(own, "usr_b").text).toBe("dobar dan");
  });

  it("marks a selection as a span in code points, trimmed", () => {
    const text = "Zovem se  Ana Petrović ";
    expect(spanOf(text, 8, 23, "name")).toEqual({ start: 10, end: 22, class: "name", text: "Ana Petrović" });
    expect(spanOf(text, 3, 3, "name")).toBeUndefined();
    expect(spanOf("שלום דנה", 5, 8, "name")).toEqual({ start: 5, end: 8, class: "name", text: "דנה" });
  });

  it("drops spans an edit moved", () => {
    const spans = [{ start: 9, end: 12, class: "name", text: "Ana" }];
    expect(keepSpans("Zovem se Ana.", spans)).toHaveLength(1);
    expect(keepSpans("Zovem se  Ana.", spans)).toHaveLength(0);
  });

  it("cycles the target channel, the other party, then both", () => {
    expect(channelCycle(item)).toEqual([0, 1, undefined]);
    expect(channelLabel(item, 0)).toBe("Caller (target)");
    expect(channelLabel(item, 1)).toBe("Bot");
    expect(channelLabel(item, undefined)).toBe("Both");
    expect(channelCycle({ ...item, window: { ...item.window, channels: 1 } })).toEqual([undefined]);
  });

  it("places the segment in its window and toggles tags", () => {
    expect(segmentSpan(item)).toEqual({ start: 2, end: 6 });
    expect(toggleTag(["noise"], "noise")).toEqual([]);
    expect(toggleTag([], "crosstalk")).toEqual(["crosstalk"]);
  });

  it("reads an invitation token from the fragment only", () => {
    expect(invitationToken("#invitation=cri_abc123")).toBe("cri_abc123");
    expect(invitationToken("#x=1&invitation=cri_z9")).toBe("cri_z9");
    expect(invitationToken("#invitation=cdk_nope")).toBeUndefined();
    expect(invitationToken("")).toBeUndefined();
  });
});
