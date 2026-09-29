import { describe, expect, it } from "vitest";
import { verbs } from "@/api/operations.gen";
import { verbIconComponents } from "./icons";

describe("verb icons", () => {
  it("every verb's icon is imported", () => {
    for (const [verb, spec] of Object.entries(verbs)) {
      expect(verbIconComponents[spec.icon], `${verb} → ${spec.icon}`).toBeDefined();
    }
  });
  it("one icon per verb", () => {
    const icons = Object.values(verbs).map((v) => v.icon);
    expect(new Set(icons).size).toBe(icons.length);
  });
});
