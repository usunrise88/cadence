import { describe, expect, it } from "vitest";
import type { DatasetPayload } from "@/api/gen/types.gen";
import { datasetCharts, durationChart, groupChart, histogramBins } from "./charts";
import { searchQuery } from "./UtteranceSearch";

const payload = (extra: Partial<DatasetPayload> = {}): DatasetPayload => ({
  source: "mount://corpora/x",
  locales: ["sr-RS"],
  splits: [
    { name: "train", utterances: 90, hours: 0.9 },
    { name: "validation", utterances: 10, hours: 0.1 },
  ],
  hours: 1,
  utterances: 100,
  bytes: 0,
  fixture: false,
  ...extra,
});

describe("histogramBins", () => {
  it("closes the open last bucket as wide as the one before it", () => {
    expect(histogramBins({ edges: [0, 2, 4], counts: [1, 5, 2] })).toEqual([
      { start: 0, end: 2, count: 1 },
      { start: 2, end: 4, count: 5 },
      { start: 4, end: 6, count: 2 },
    ]);
  });
  it("draws a single bucket one unit wide and nothing without data", () => {
    expect(histogramBins({ edges: [3], counts: [7] })).toEqual([{ start: 3, end: 4, count: 7 }]);
    expect(histogramBins(undefined)).toEqual([]);
  });
});

describe("dataset charts (R53)", () => {
  it("marks the percentiles and the preview's filter bounds on the duration histogram", () => {
    const d = payload({ stats: { durationHistogram: { edges: [0, 5, 10], counts: [10, 80, 10] }, durationPercentiles: { p5: 1.2, p50: 6, p95: 11 } } });
    const spec = durationChart(d, { minDuration: 0.5, maxDuration: 20 });
    expect(spec?.kind).toBe("histogram");
    expect(spec?.marks?.map((m) => m.label)).toEqual(["p5", "p50", "p95", "min filter", "max filter"]);
  });
  it("lists only the charts the version has data for", () => {
    expect(datasetCharts(payload()).map((c) => c.title)).toEqual(["Hours by split"]);
    const full = payload({
      languages: [{ language: "sr-RS", utterances: 100, hours: 1 }],
      stats: {
        durationHistogram: { edges: [0], counts: [1] },
        charsPerSecondHistogram: { edges: [0], counts: [1] },
        levelHistogram: { edges: [-40], counts: [1] },
        sourceRates: [{ rate: 8000, utterances: 100 }],
        origins: [{ origin: "human", utterances: 100, hours: 1 }],
        roles: [{ role: "caller", utterances: 100, hours: 1 }],
      },
    });
    expect(datasetCharts(full)).toHaveLength(8);
  });
  it("leaves groups without the key out", () => {
    expect(groupChart("x", [{ utterances: 1, hours: 1 }], "role")).toBeUndefined();
  });
});

describe("searchQuery", () => {
  const empty = { q: "", language: "", speaker: "", origin: "", split: "" as const, minDuration: "", maxDuration: "" };
  it("sends only the filters that are set", () => {
    expect(searchQuery(empty, { source: "src_1" }, 25)).toEqual({ limit: 25, source: "src_1" });
    expect(searchQuery({ ...empty, q: " dobar ", minDuration: "1.5", maxDuration: "x", split: "train" }, { dataset: "ver_1" }, 10, "cur")).toEqual({
      limit: 10,
      dataset: "ver_1",
      q: "dobar",
      minDuration: 1.5,
      split: "train",
      after: "cur",
    });
  });
  it("ignores the split outside a dataset version", () => {
    expect(searchQuery({ ...empty, split: "test" }, { source: "src_1" }, 5).split).toBeUndefined();
  });
});
