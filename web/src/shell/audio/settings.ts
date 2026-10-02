// The audio view's defaults: defaults.yaml views.audio (R52; spike S5), served by defaults.get. The view waits for
// them rather than repeating any value here (defaults live in defaults.yaml only).
import type { Defaults } from "@/api/gen/types.gen";
import { COLORMAPS, SLOT_BINS, type Colormap } from "./renderer";

export type AudioDefaults = {
  windowMs: number;
  hopMs: number;
  nFft: number;
  fmaxHz: number;
  axis: "mel" | "hz";
  rangeDb: number;
  gainDb: number;
  colormap: Colormap;
  tileFrames: number;
  tileSlots: number;
  tileCacheMb: number;
  modelInputMaxSpanS: number;
  wordsMaxVisible: number;
  browserStftMaxS: number;
};

/** views.audio from a defaults document; undefined while it is not loaded or lacks a key. */
export function audioDefaults(d: Defaults | undefined): AudioDefaults | undefined {
  const section = (d?.views as Record<string, Record<string, { value?: unknown }>> | undefined)?.audio;
  if (!section) return undefined;
  const num = (k: string): number | undefined => {
    const v = section[k]?.value;
    return typeof v === "number" ? v : undefined;
  };
  const str = (k: string): string | undefined => {
    const v = section[k]?.value;
    return typeof v === "string" ? v : undefined;
  };
  const out = {
    windowMs: num("window_ms"),
    hopMs: num("hop_ms"),
    nFft: num("n_fft"),
    fmaxHz: num("fmax_hz"),
    axis: str("axis") === "hz" ? ("hz" as const) : ("mel" as const),
    rangeDb: num("range_db"),
    gainDb: num("gain_db"),
    colormap: (COLORMAPS as string[]).includes(str("colormap") ?? "") ? (str("colormap") as Colormap) : undefined,
    tileFrames: num("tile_frames"),
    tileSlots: num("tile_slots"),
    tileCacheMb: num("tile_cache_mb"),
    modelInputMaxSpanS: num("model_input_max_span_s"),
    wordsMaxVisible: num("words_max_visible"),
    browserStftMaxS: num("browser_stft_max_s"),
  };
  for (const v of Object.values(out)) if (v === undefined) return undefined;
  return out as AudioDefaults;
}

/** STFT shape for audio at an origin rate, served at 16 kHz: window and hop in samples, bins up to the Nyquist. */
export function stftShape(d: AudioDefaults, originRate: number): { win: number; hop: number; nFft: number; bins: number; binHz: number; fmaxHz: number } {
  const rate = 16000;
  const binHz = rate / d.nFft;
  // A texture slot holds 257 bins (FFT 512 at 16 kHz); a longer FFT keeps the lower 257.
  const fmaxHz = Math.min(originRate / 2, rate / 2, (SLOT_BINS - 1) * binHz);
  return {
    win: Math.round((d.windowMs * rate) / 1000),
    hop: Math.round((d.hopMs * rate) / 1000),
    nFft: d.nFft,
    bins: Math.min(d.nFft / 2 + 1, SLOT_BINS, Math.floor(fmaxHz / binHz) + 1),
    binHz,
    fmaxHz,
  };
}
