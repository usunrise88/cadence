import { describe, expect, it } from "vitest";
import type { SpectrogramManifest } from "@/api/gen/types.gen";
import { isPending } from "./data";
import { detailWindow, LongPeaks, overviewHopMs, PeakPyramid } from "./peaks";
import { audioItem, parseAudioItem } from "./selection";
import { ServerSource } from "./tiles";
import { referenceTrack, WordTrack } from "./words";

// Long audio and the reference track (phase 4 tail; R51, R52): the overview hop and detail windows of long audio, the
// two-level peaks, the spectrogram build's 202, narrowband tiles, the reference word track and its selection item.

const H = "b3:" + "a".repeat(64);
const G = "ver_0192abcd-ef01";

describe("long audio peaks", () => {
  it("picks the finest stored level that keeps the overview small", () => {
    expect(overviewHopMs(30)).toBe(10);
    expect(overviewHopMs(327)).toBe(10); // 32 700 pairs
    expect(overviewHopMs(600)).toBe(160);
    expect(overviewHopMs(3600)).toBe(160); // an hour: 22 500 pairs
    expect(overviewHopMs(4 * 3600)).toBe(2560);
  });

  it("loads detail only when the overview has less than a pair per pixel, in whole blocks", () => {
    expect(detailWindow(0, 3600, 1200, 0.16, 3600)).toBeNull(); // the whole hour: 3 s per pixel
    expect(detailWindow(100, 120, 1200, 0.16, 3600)).toEqual([0, 360]);
    expect(detailWindow(1000, 30, 1200, 0.16, 3600)).toEqual([960, 1080]);
    expect(detailWindow(3590, 10, 1200, 0.16, 3600)).toEqual([3540, 3600]);
    expect(detailWindow(5, 10, 1200, 0.01, 60)).toBeNull(); // short audio has all its 10 ms peaks already
  });

  it("reads detail inside its window and the overview elsewhere", () => {
    const overview = new PeakPyramid(1, Int8Array.from([-10, 10, -20, 20, -30, 30, -40, 40]), 0.16); // 0–0.64 s
    const detail = new PeakPyramid(1, Int8Array.from(Array.from({ length: 32 }, (_, i) => (i % 2 ? 100 : -100))), 0.01, 0.16); // 0.16–0.32 s
    const p = new LongPeaks(overview, detail);
    expect(p.channels).toBe(1);
    expect(p.hopS).toBe(0.16);
    const fine = p.levelFor(0.16, 160); // 1 ms per pixel: the detail's base level
    expect(fine).toBe(0);
    expect(p.range(0, 0.2, 0.21, fine)).toEqual([-100, 100]);
    expect(p.range(0, 0.5, 0.51, fine)).toEqual([-40, 40]); // outside the window: the overview
    const coarse = p.levelFor(0.64, 4); // 0.16 s per pixel: the overview
    expect(coarse).toBeGreaterThanOrEqual(1000);
    expect(p.range(0, 0.2, 0.21, coarse)).toEqual([-20, 20]);
    expect(new LongPeaks(overview, null).range(0, 0.2, 0.21, 0)).toEqual([-20, 20]);
  });
});

describe("spectrogram build", () => {
  const manifest: SpectrogramManifest = {
    schema: "cadence.spectrogram-tiles/1", sampleRate: 16000, originSampleRate: 16000, channels: 1, bins: 129, binHz: 31.25,
    tileFrames: 512, encoding: { floorDb: -120, stepDb: 0.5 }, levels: [{ level: 0, hopS: 0.01, frames: 10, tiles: 1 }], peakDb: [-6],
    durationS: 0.1,
  };
  it("tells a 202 from a manifest", () => {
    expect(isPending({ jobId: "job_1" })).toBe(true);
    expect(isPending(manifest)).toBe(false);
  });
  it("stops narrowband audio at 4 kHz", () => {
    const fetcher = () => Promise.resolve(new Uint8Array(0));
    expect(new ServerSource(manifest, fetcher, 1 << 20).fmaxHz).toBe(8000);
    expect(new ServerSource({ ...manifest, narrowband: true, bandwidthHz: 3400 }, fetcher, 1 << 20).fmaxHz).toBe(4000);
    expect(new ServerSource({ ...manifest, originSampleRate: 8000 }, fetcher, 1 << 20).fmaxHz).toBe(4000);
  });
});

describe("reference track", () => {
  const base = { utteranceId: "utt_1", audio: H, text: "", words: [], deletions: [], aligned: false };
  it("draws aligned reference words and shows an unaligned reference as text", () => {
    expect(referenceTrack(base)).toBeUndefined();
    const t = referenceTrack({ ...base, reference: { artifact: H, aligned: true, text: "Shalom olam", language: "he-IL",
      words: [{ index: 0, word: "Shalom", start: 0.1, end: 0.4 }, { index: 1, word: "olam", start: 0.5, end: 0.9, score: 0.8 }] } })!;
    expect(t.id).toBe("ref");
    expect(t.label).toBe("Reference");
    expect(t.lang).toBe("he-IL");
    expect(t.words).toEqual([{ word: "Shalom", start: 0.1, end: 0.4 }, { word: "olam", start: 0.5, end: 0.9 }]);
    expect(t.note).toBeUndefined();
    const u = referenceTrack({ ...base, reference: { artifact: H, aligned: false, text: "Shalom olam", words: [], reason: "no aligner for sr" } }, "sr")!;
    expect(u.words).toEqual([]);
    expect(u.note).toBe("Shalom olam (unaligned: no aligner for sr)");
    expect(u.lang).toBe("sr");
    const track = new WordTrack(document, u, 300, () => {});
    expect(track.el.querySelector(".cadence-audio-track-note")?.textContent).toBe("Shalom olam (unaligned: no aligner for sr)");
  });

  it("carries the golden set in the audio selection item", () => {
    const item = audioItem({ utterance: H, start: 1, end: 2, cell: "evc_1", goldenSet: G });
    expect(item).toBe(`utt:${H}#t=1.00,2.00&cell=evc_1&gs=${G}`);
    expect(parseAudioItem(item)).toEqual({ utterance: H, start: 1, end: 2, cell: "evc_1", goldenSet: G });
    expect(parseAudioItem(`utt:${H}#gs=dataset/x`)).toEqual({ utterance: H });
  });
});
