import { describe, expect, it } from "vitest";
import { AudioAxis, clampRange, formatTime, MIN_SPAN, pyramidLevel, rulerStep, zoomAround } from "./axis";
import { toCtm, toTextGrid, toWebVtt } from "./exports";
import { PeakPyramid, peaksFromApi, peaksFromPcm } from "./peaks";
import { audioItem, parseAudioItem, parseClock, spanFragment, spanReference } from "./selection";
import { audioDefaults, stftShape } from "./settings";
import { fft, quantize, stftU8 } from "./stft";
import { buildPyramid, levelTable, MemorySource, TILE, TileCache, tileRange, tileSpan } from "./tiles";
import { decodeWav } from "./wav";
import { opLabel, stepWord, wordIndexAt, type TrackWord } from "./words";

const H = "b3:" + "a".repeat(64);

describe("axis", () => {
  it("clamps ranges into the audio", () => {
    expect(clampRange(-5, 2, 10)).toEqual({ start: 0, span: 2 });
    expect(clampRange(9, 2, 10)).toEqual({ start: 8, span: 2 });
    expect(clampRange(0, 0.01, 10).span).toBe(MIN_SPAN);
    expect(clampRange(0, 50, 10)).toEqual({ start: 0, span: 10 });
  });
  it("zooms around a time that keeps its place", () => {
    const r = zoomAround({ start: 0, span: 10, duration: 100 }, 0.5, 5);
    expect(r).toEqual({ start: 2.5, span: 5 });
  });
  it("seeks, follows and loops", () => {
    const a = new AudioAxis(60, { start: 0, span: 10 });
    let n = 0;
    a.subscribe(() => n++);
    a.seek(30, true);
    expect(a.get().playhead).toBe(30);
    expect(a.get().start).toBeCloseTo(27); // 30 % from the left
    a.setLoop(5, 3);
    expect(a.get().loop).toBeNull();
    a.setLoop(5, 8);
    expect(a.get().loop).toEqual([5, 8]);
    expect(n).toBeGreaterThan(2);
    a.setDuration(20);
    expect(a.get().duration).toBe(20);
    expect(a.get().playhead).toBe(20);
  });
  it("animates the zoom to its target", () => {
    const a = new AudioAxis(100, { start: 0, span: 100 });
    a.animateTo(10, 10, 100);
    expect(a.animating).toBe(true);
    a.tick(performance.now() + 1000);
    expect(a.get().start).toBeCloseTo(10, 9);
    expect(a.get().span).toBeCloseTo(10, 9);
    expect(a.animating).toBe(false);
  });
  it("picks pyramid levels, ruler steps and time labels", () => {
    expect(pyramidLevel(10, 1000, 0.01, 8)).toBe(0); // 1 frame per pixel
    expect(pyramidLevel(80, 1000, 0.01, 8)).toBe(3); // 8 frames per pixel
    expect(pyramidLevel(36000, 1000, 0.01, 8)).toBe(7);
    expect(rulerStep(10, 800)).toBe(1);
    expect(rulerStep(600, 800)).toBe(60);
    expect(formatTime(75.25, 3)).toBe("1:15.25");
    expect(formatTime(75.25, 30)).toBe("1:15.3");
    expect(formatTime(3725, 600)).toBe("62:05");
  });
});

describe("stft", () => {
  it("matches a direct DFT", () => {
    const n = 16;
    const re = Float64Array.from({ length: n }, (_, i) => Math.sin(i) + 0.3 * Math.cos(3 * i));
    const im = new Float64Array(n);
    const ref = Array.from({ length: n }, (_, k) => {
      let r = 0;
      let j = 0;
      for (let t = 0; t < n; t++) {
        r += re[t]! * Math.cos((-2 * Math.PI * k * t) / n);
        j += re[t]! * Math.sin((-2 * Math.PI * k * t) / n);
      }
      return [r, j];
    });
    fft(re, im);
    for (let k = 0; k < n; k++) {
      expect(re[k]).toBeCloseTo(ref[k]![0]!, 9);
      expect(im[k]).toBeCloseTo(ref[k]![1]!, 9);
    }
    expect(() => fft(new Float64Array(12), new Float64Array(12))).toThrow();
  });
  it("puts a sine at its bin and level", () => {
    const rate = 16000;
    const pcm = Float32Array.from({ length: rate }, (_, i) => 0.5 * Math.sin((2 * Math.PI * 1000 * i) / rate));
    const { frames, u8 } = stftU8(pcm, { win: 400, hop: 160, nFft: 512, bins: 257 });
    expect(frames).toBe(101);
    const row = u8.subarray(50 * 257, 51 * 257);
    let best = 0;
    for (let k = 0; k < 257; k++) if (row[k]! > row[best]!) best = k;
    expect(best).toBe(32); // 1000 Hz / 31.25 Hz
    expect(Math.abs(row[32]! * 0.5 - 120 - 20 * Math.log10(0.5))).toBeLessThan(0.6);
    expect(quantize(-200)).toBe(0);
    expect(quantize(20)).toBe(255);
  });
});

describe("tiles", () => {
  it("halves levels until one tile holds them", () => {
    expect(levelTable(1201, 0.01).map((l) => [l.frames, l.tiles])).toEqual([
      [1201, 3],
      [601, 2],
      [301, 1],
    ]);
  });
  it("builds bins-major tiles and max-pools levels", () => {
    const bins = 3;
    const frames = 600;
    const fm = new Uint8Array(frames * bins);
    for (let f = 0; f < frames; f++) for (let k = 0; k < bins; k++) fm[f * bins + k] = (f + k) % 200;
    const { levels, tiles } = buildPyramid(fm, frames, bins, 0.01);
    expect(levels.length).toBe(2);
    expect(tiles[0]![0]!.length).toBe(TILE * bins);
    expect(tiles[0]![0]![1 * TILE + 5]).toBe(6); // bin 1, frame 5
    expect(tiles[0]![1]![0 * TILE + 100]).toBe(0); // zero padding past frame 599
    expect(tiles[1]![0]![2 * TILE + 3]).toBe(Math.max(8, 9)); // frames 6, 7 of bin 2
    const src = new MemorySource("m", [{ frameMajor: fm, frames }], bins, 31.25, 0.01, 4000);
    expect(src.tile(0, 1, 0)).toBe(src.peek(0, 1, 0));
    expect(src.bytes()).toBe(3 * TILE * bins);
  });
  it("finds the tiles a range covers", () => {
    const lv = { level: 0, hopS: 0.01, frames: 2000, tiles: 4 };
    expect(tileRange(lv, 0, 1, 0.01)).toEqual([0, 0]);
    expect(tileRange(lv, 5, 1, 0.01)).toEqual([0, 1]);
    const [a, b] = tileRange(lv, 100, 10, 0.01); // past the level: no tile
    expect(a).toBeGreaterThan(b);
    expect(tileRange(lv, 18, 4, 0.01)).toEqual([3, 3]);
    expect(tileSpan(lv, 1, 0.01)).toEqual([5.115, 10.235]);
  });
  it("evicts the least recent unpinned tile", () => {
    const c = new TileCache(10, (k) => k.startsWith("top"));
    c.set("top", new Uint8Array(4));
    c.set("a", new Uint8Array(4));
    c.get("top");
    c.set("b", new Uint8Array(4));
    expect(c.keys().sort()).toEqual(["b", "top"]);
    expect(c.bytes()).toBe(8);
  });
});

describe("peaks", () => {
  it("decodes peaks.get and pools levels", () => {
    const data = new Int8Array(4000 * 2);
    for (let i = 0; i < 4000; i++) {
      data[2 * i] = -(i % 100);
      data[2 * i + 1] = i % 100;
    }
    const b64 = btoa(String.fromCharCode(...new Uint8Array(data.buffer)));
    const p = peaksFromApi({ utteranceId: "utt_1", channels: 1, hopS: 0.01, frames: 4000, start: 0, durationS: 40, encoding: "int8-minmax", data: b64, clipped: [7] });
    expect(p.frames).toBe(4000);
    expect(p.levels.length).toBe(3);
    expect(p.range(0, 0, 1, 0)).toEqual([-99, 99]);
    expect(p.range(0, 0.5, 0.52, 0)).toEqual([-51, 51]);
    expect(p.clipped).toEqual([7]);
    expect(p.levelFor(40, 100)).toBe(2);
  });
  it("computes peaks of PCM and marks clipping", () => {
    const pcm = new Float32Array(1600).fill(0.25);
    pcm[900] = 1;
    const p = peaksFromPcm([pcm], 16000);
    expect(p.frames).toBe(10);
    expect(p.range(0, 0, 0.01, 0)).toEqual([32, 32]);
    expect(p.clipped).toEqual([5]);
    expect(new PeakPyramid(1, new Int8Array(0), 0.01).range(0, 0, 1, 0)).toEqual([0, 0]);
  });
});

describe("wav", () => {
  it("decodes 16-bit stereo PCM", () => {
    const n = 4;
    const buf = new ArrayBuffer(44 + n * 4);
    const v = new DataView(buf);
    const s = (o: number, t: string) => [...t].forEach((c, i) => v.setUint8(o + i, c.charCodeAt(0)));
    s(0, "RIFF");
    v.setUint32(4, 36 + n * 4, true);
    s(8, "WAVEfmt ");
    v.setUint32(16, 16, true);
    v.setUint16(20, 1, true);
    v.setUint16(22, 2, true);
    v.setUint32(24, 8000, true);
    v.setUint32(28, 32000, true);
    v.setUint16(32, 4, true);
    v.setUint16(34, 16, true);
    s(36, "data");
    v.setUint32(40, n * 4, true);
    for (let i = 0; i < n; i++) {
      v.setInt16(44 + i * 4, 16384, true);
      v.setInt16(46 + i * 4, -32768, true);
    }
    const a = decodeWav(buf);
    expect(a.sampleRate).toBe(8000);
    expect(a.channels.length).toBe(2);
    expect(a.channels[0]![3]).toBe(0.5);
    expect(a.channels[1]![0]).toBe(-1);
    expect(() => decodeWav(new ArrayBuffer(8))).toThrow("not a WAV");
  });
});

describe("selection", () => {
  it("writes and reads media fragment items", () => {
    const item = audioItem({ utterance: "utt_1", start: 1.2, end: 2.345, channel: 1, cell: "evc_a" });
    expect(item).toBe("utt:utt_1#t=1.20,2.35&ch=1&cell=evc_a");
    expect(parseAudioItem(item)).toEqual({ utterance: "utt_1", start: 1.2, end: 2.35, channel: 1, cell: "evc_a" });
    expect(parseAudioItem(`${H}#t=00:01:02.5,63&hyp=${H}`)).toEqual({ utterance: H, start: 62.5, end: 63, hypotheses: H });
    expect(parseAudioItem("@utt:utt_9#t=,4&x=y")).toEqual({ utterance: "utt_9", end: 4 });
    expect(parseAudioItem("utt:utt_1#t=5,2")).toEqual({ utterance: "utt_1", start: 5 });
    expect(parseAudioItem("mix:mix_1")).toBeUndefined();
    expect(parseClock("npt:1:02")).toBe(62);
    expect(parseClock("x")).toBeUndefined();
    expect(spanFragment(1)).toBe("t=1.00");
  });
  it("references spans for chat by utterance id only", () => {
    expect(spanReference({ utterance: "utt_1", start: 1.2, end: 2.35, cell: "evc_a" })).toEqual({ ref: "@utt:utt_1#t=1.20,2.35", label: "utt_1 1.20–2.35 s" });
    expect(spanReference({ utterance: "utt_1" })).toEqual({ ref: "@utt:utt_1", label: "utt_1" });
    expect(spanReference({ utterance: H })).toBeUndefined();
  });
});

describe("exports", () => {
  const words = [
    { word: "שלום", start: 0.2, end: 0.6, confidence: 0.9 },
    { word: 'say "hi"', start: 0.5, end: 1.0 },
    { word: "<b>", start: 2, end: 2.4, confidence: 0.5 },
  ];
  it("writes a TextGrid without holes or overlaps", () => {
    const tg = toTextGrid(words, 3);
    expect(tg).toContain('Object class = "TextGrid"');
    expect(tg).toContain("intervals: size = 6");
    expect(tg).toContain('text = "say ""hi"""');
    expect(tg).toMatch(/xmin = 0\.600 \n\s+xmax = 1\.000 /); // the overlap clipped to the previous end
    expect(tg).toMatch(/intervals \[6\]:\n\s+xmin = 2\.400 \n\s+xmax = 3\.000 \n\s+text = "" /);
  });
  it("writes CTM lines", () => {
    expect(toCtm(words, "utt_1").split("\n")).toEqual(["utt_1 1 0.200 0.400 שלום 0.900", 'utt_1 1 0.500 0.500 say_"hi"', "utt_1 1 2.000 0.400 <b> 0.500", ""]);
    expect(toCtm([], "x")).toBe("");
  });
  it("writes WebVTT cues split at pauses, with word timestamps", () => {
    const vtt = toWebVtt(words);
    expect(vtt.startsWith("WEBVTT\n\n00:00:00.200 --> 00:00:01.000\nשלום <00:00:00.500>say \"hi\"\n")).toBe(true);
    expect(vtt).toContain("00:00:02.000 --> 00:00:02.400\n&lt;b&gt;\n");
  });
});

describe("words", () => {
  const ws: TrackWord[] = [
    { word: "a", start: 0, end: 0.5 },
    { word: "b", start: 1, end: 1.5, op: "S", ref: "c" },
    { word: "d", start: 2, end: 2.5 },
  ];
  it("finds and steps words", () => {
    expect(wordIndexAt(ws, 1.2)).toBe(1);
    expect(wordIndexAt(ws, 3)).toBe(3);
    expect(stepWord(ws, 1, 1)).toBe(2);
    expect(stepWord(ws, 1, -1)).toBe(0);
    expect(stepWord([], 0, 1)).toBe(-1);
    expect(opLabel("S", "c")).toEqual({ glyph: "≠", text: "substituted for “c”" });
  });
});

describe("settings", () => {
  const sec = (v: unknown) => ({ value: v });
  const d = {
    views: {
      audio: {
        window_ms: sec(25),
        hop_ms: sec(10),
        n_fft: sec(512),
        fmax_hz: sec(8000),
        axis: sec("mel"),
        range_db: sec(80),
        gain_db: sec(0),
        colormap: sec("magma"),
        tile_frames: sec(512),
        tile_slots: sec(64),
        tile_cache_mb: sec(64),
        model_input_max_span_s: sec(30),
        words_max_visible: sec(400),
        browser_stft_max_s: sec(600),
      },
    },
  };
  it("reads views.audio and shapes the STFT", () => {
    const a = audioDefaults(d as never)!;
    expect(a.colormap).toBe("magma");
    expect(stftShape(a, 8000)).toEqual({ win: 400, hop: 160, nFft: 512, bins: 129, binHz: 31.25, fmaxHz: 4000 });
    expect(stftShape(a, 44100).bins).toBe(257);
    expect(audioDefaults({ views: { audio: { window_ms: sec(25) } } } as never)).toBeUndefined();
    expect(audioDefaults(undefined)).toBeUndefined();
  });
});
