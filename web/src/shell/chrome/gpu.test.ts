import { describe, expect, it } from "vitest";
import type { ComputeHost } from "@/api/gen/types.gen";
import { applyTelemetry, formatReading, isStale, mergeReadings, seedReadings, sortedReadings } from "./gpu";

const host = (cards: ComputeHost["cards"]): ComputeHost => ({ id: "cmp_1", name: "staging", cards }) as unknown as ComputeHost;

describe("status bar GPU readings", () => {
  const seeded = seedReadings([
    host([
      {
        index: 0,
        name: "RTX PRO 5000",
        cardClass: "blackwell-48gb",
        memoryGb: 48,
        memoryCapGb: 22,
        allowedJobKinds: ["training"],
        telemetry: { index: 0, memoryUsedMb: 24371, memoryTotalMb: 49152, utilization: 12, reportedAt: "2026-09-30T12:00:00Z" },
      },
      { index: 1, name: "idle", cardClass: "x", memoryGb: 24, memoryCapGb: 12, allowedJobKinds: [] },
    ]),
  ]);

  it("seeds from compute.list with the card's cap, skipping cards that never reported", () => {
    expect(Object.keys(seeded)).toEqual(["staging#0"]);
    expect(seeded["staging#0"]).toMatchObject({ name: "RTX PRO 5000", capGb: 22, usedMb: 24371, util: 12 });
    expect(formatReading(seeded["staging#0"]!)).toBe("23.8/48 GB · 12 %");
  });

  it("applies live telemetry, never an older reading over a newer one, and keeps the cap in the merge", () => {
    const t = Date.parse("2026-09-30T12:00:10Z") / 1000;
    const live = applyTelemetry({}, "staging", [{ index: 0, memoryUsedMb: 40000, memoryTotalMb: 49152, utilization: 97 }], t);
    const merged = mergeReadings(seeded, live);
    expect(merged["staging#0"]).toMatchObject({ usedMb: 40000, util: 97, capGb: 22 });
    const older = applyTelemetry(live, "staging", [{ index: 0, memoryUsedMb: 1, utilization: 1 }], t - 5);
    expect(older).toBe(live);
    expect(applyTelemetry(live, "staging", [{ index: 3 }], t + 1)).toBe(live);
  });

  it("orders cards and marks readings stale after two minutes", () => {
    const r = applyTelemetry({}, "b", [{ index: 1, utilization: 5 }, { index: 0, utilization: 6 }], 100);
    const all = sortedReadings({ ...r, ...applyTelemetry({}, "a", [{ index: 0, memoryUsedMb: 512 }], 100) });
    expect(all.map((c) => c.key)).toEqual(["a#0", "b#0", "b#1"]);
    expect(formatReading(all[0]!)).toBe("0.5 GB");
    expect(isStale(all[0]!, 219)).toBe(false);
    expect(isStale(all[0]!, 221)).toBe(true);
  });
});
