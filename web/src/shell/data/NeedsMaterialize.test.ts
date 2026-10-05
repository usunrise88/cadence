import { describe, expect, it } from "vitest";
import type { PipelineWarning } from "@/api/gen/types.gen";
import { formatBytes, materializeOf } from "./NeedsMaterialize";

const m = (versionId: string) => ({ versionId, artifact: "b3:x", bytes: 1, copyBytes: 1, shards: 1, copyShards: 1, from: [], missing: 0 });

describe("needs materialize", () => {
  it("keeps the needs-materialize warnings, once per dataset version", () => {
    const warnings: PipelineWarning[] = [
      { code: "step-kind-deprecated", step: "train", message: "deprecated" },
      { code: "needs-materialize", step: "train", message: "a", materialize: m("ver_a") },
      { code: "needs-materialize", step: "other", message: "a again", materialize: m("ver_a") },
      { code: "needs-materialize", step: "train", message: "b", materialize: m("ver_b") },
    ];
    expect(materializeOf(warnings).map((x) => x.versionId)).toEqual(["ver_a", "ver_b"]);
    expect(materializeOf(undefined)).toEqual([]);
  });

  it("formats bytes", () => {
    expect(formatBytes(2.4e9)).toBe("2.40 GB");
    expect(formatBytes(3.5e6)).toBe("3.5 MB");
    expect(formatBytes(2048)).toBe("2 kB");
  });
});
