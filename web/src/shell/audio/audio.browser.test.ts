import "@/styles/theme.css";
import "./audio.css";
import { afterEach, describe, expect, it } from "vitest";
import { AudioAxis } from "./axis";
import { blurAudio, focusedAudio } from "./controller";
import { AudioEngine, type EngineOptions } from "./engine";
import { peaksFromPcm } from "./peaks";
import { rendererOf } from "./renderer";
import { stftU8 } from "./stft";
import { MemorySource } from "./tiles";

// The audio view in headless Chromium: the shared WebGL2 renderer draws the spectrogram into each view's canvas,
// several views in one window share one context, a second document (an iframe, like a popout) gets its own renderer,
// a lost context keeps the picture and redraws on restore, and word tracks are bidi-isolated DOM.

const engines: AudioEngine[] = [];
afterEach(() => {
  for (const e of engines.splice(0)) e.destroy();
  document.body.innerHTML = "";
});

function tone(seconds: number, hz: number): Float32Array {
  return Float32Array.from({ length: seconds * 16000 }, (_, i) => 0.5 * Math.sin((2 * Math.PI * hz * i) / 16000));
}

function source(id: string, pcm: Float32Array): MemorySource {
  const s = stftU8(pcm, { win: 400, hop: 160, nFft: 512, bins: 257 });
  return new MemorySource(id, [{ frameMajor: s.u8, frames: s.frames }], 257, 31.25, 0.01, 8000);
}

function view(doc: Document, axis: AudioAxis, extra: Partial<EngineOptions> = {}): AudioEngine {
  const box = doc.createElement("div");
  box.style.width = "600px";
  doc.body.appendChild(box);
  const e = new AudioEngine(box, {
    axis,
    title: "test audio",
    tileSlots: 16,
    maxVisibleWords: 400,
    settings: { gainDb: 0, rangeDb: 80, colormap: "magma", axis: "hz", fmaxHz: 8000 },
    ...extra,
  });
  engines.push(e);
  return e;
}

async function frames(win: Window, n = 3): Promise<void> {
  for (let i = 0; i < n; i++) await new Promise((r) => win.requestAnimationFrame(() => r(null)));
}

describe("audio view", () => {
  it("draws waveform, spectrogram and words, sharing one renderer per window", async () => {
    const pcm = tone(2, 1000);
    const axis = new AudioAxis(2);
    const a = view(document, axis);
    const b = view(document, axis, { compact: true });
    for (const e of [a, b]) {
      e.setPeaks(peaksFromPcm([pcm], 16000));
      e.setSpec(source("s1", pcm), "");
      e.setWords([{ id: "hyp", label: "Hypothesis", lang: "he", words: [{ word: "שלום", start: 0.2, end: 0.6, confidence: 0.4, op: "S", ref: "שלומות" }, { word: "123", start: 0.8, end: 1.1 }], deletions: [{ at: 1.5, ref: "תודה" }] }]);
    }
    await frames(window, 4);
    expect(a.renderer).toBe(b.renderer);
    expect(rendererOf(window)?.stats.generation).toBe(1);
    expect(a.probe()).toEqual({ canvases: 1, blank: 0 });
    expect(b.probe()).toEqual({ canvases: 1, blank: 0 });
    // A 1 kHz tone: the 1 kHz row is bright, 6 kHz is dark (magma: bright is high red + green).
    const spec = a.root.querySelector<HTMLCanvasElement>(".cadence-audio-spec")!;
    const ctx = spec.getContext("2d")!;
    const rowAt = (hz: number) => Math.round(spec.height * (1 - hz / 8000));
    const lum = (y: number) => {
      const d = ctx.getImageData(Math.floor(spec.width / 2), Math.min(spec.height - 1, y), 1, 1).data;
      return d[0]! + d[1]! + d[2]!;
    };
    expect(lum(rowAt(1000))).toBeGreaterThan(lum(rowAt(6000)) + 200);
    const words = a.root.querySelectorAll<HTMLElement>(".cadence-audio-word");
    expect(words.length).toBe(2);
    expect(words[0]!.tagName).toBe("BDI");
    expect(words[0]!.dir).toBe("auto");
    expect(words[0]!.dataset.op).toBe("S");
    expect(words[0]!.title).toContain("substituted for “שלומות”");
    expect(a.root.querySelector(".cadence-audio-deletion")?.getAttribute("title")).toBe("deleted: “תודה”");
    expect(a.root.style.direction || getComputedStyle(a.root).direction).toBe("ltr");
    expect(a.summaryText()).toContain("Tracks: waveform, spectrogram, Hypothesis (2 words)");
  });

  it("keeps the picture through a context loss and redraws on restore", async () => {
    const pcm = tone(1, 3000);
    const e = view(document, new AudioAxis(1));
    e.setSpec(source("s2", pcm), "");
    await frames(window);
    const before = e.samplePixels();
    const r = e.renderer;
    if (!r.loseContext(50)) return; // no WEBGL_lose_context: nothing to test
    await frames(window, 2);
    expect(r.lost).toBe(true);
    e.invalidate();
    await frames(window, 2);
    expect(e.samplePixels()).toEqual(before); // the 2D copy keeps the last image
    await new Promise((res) => setTimeout(res, 150));
    await frames(window, 3);
    expect(r.lost).toBe(false);
    expect(r.stats.restored).toBe(1);
    expect(e.probe().blank).toBe(0);
  });

  it("renders in a second document with that window's renderer", async () => {
    const frame = document.createElement("iframe");
    frame.style.width = "700px";
    frame.style.height = "400px";
    document.body.appendChild(frame);
    const doc = frame.contentDocument!;
    for (const s of document.querySelectorAll("style")) doc.head.appendChild(s.cloneNode(true));
    const e = view(doc, new AudioAxis(1));
    e.setSpec(source("s3", tone(1, 500)), "");
    await frames(frame.contentWindow!, 4);
    expect(e.win).toBe(frame.contentWindow);
    expect(e.renderer).not.toBe(rendererOf(window));
    expect(e.probe()).toEqual({ canvases: 1, blank: 0 });
  });

  it("takes the keys' commands while focused", async () => {
    const axis = new AudioAxis(10, { start: 0, span: 10 });
    const e = view(document, axis);
    e.root.focus();
    expect(focusedAudio()).toBe(e);
    e.zoomBy(0.5);
    axis.tick(performance.now() + 1000);
    expect(axis.get().span).toBeCloseTo(5, 6);
    e.loopIn();
    e.seek(3);
    e.loopOut();
    expect(axis.get().loop).toEqual([0, 3]);
    e.root.dispatchEvent(new KeyboardEvent("keydown", { key: "+", bubbles: true }));
    axis.tick(performance.now() + 1000);
    expect(axis.get().span).toBeCloseTo(2.5, 6);
    blurAudio(e);
    expect(focusedAudio()).toBeNull();
  });
});
