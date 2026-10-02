// The audio view's data (tag media, R25): a signed link to play, peaks for the waveform, and the spectrogram — the
// browser STFT (in a Web Worker) for audio up to views.audio.browser_stft_max_s, the server tile pyramid
// (spectrogram_tiles@1 through spectrogram.get) above it. Shell code: it calls the generated client; panels never do.
import { useQuery } from "@tanstack/react-query";
import { useEffect, useState } from "react";
import { CLIENT_HEADER } from "@/api/client";
import { audioSign } from "@/api/gen/sdk.gen";
import { peaksGetOptions, spectrogramGetOptions } from "@/api/gen/@tanstack/react-query.gen";
import type { AudioLink } from "@/api/gen/types.gen";
import { peaksFromApi, type PeakPyramid } from "./peaks";
import { stftShape, type AudioDefaults } from "./settings";
import { stftU8, type StftParams } from "./stft";
import type { StftRequest, StftResponse } from "./stft.worker";
import { MemorySource, ServerSource, type SpecSource } from "./tiles";
import { decodeWav } from "./wav";

const API = "/api";

/** A short-lived signed link to the utterance's audio (audio.sign). Refetch it when the media element fails. */
export function useAudioLink(utterance: string | undefined) {
  return useQuery<AudioLink>({
    queryKey: ["media", "audio.sign", utterance],
    enabled: !!utterance,
    // The link lives media.signed_link_ttl_s; a new one is minted only when the element fails or the view reopens.
    staleTime: Infinity,
    gcTime: 0,
    retry: false,
    queryFn: async () => (await audioSign({ path: { id: utterance! }, body: {}, throwOnError: true })).data,
  });
}

export function usePeaks(utterance: string | undefined) {
  return useQuery({
    ...peaksGetOptions({ path: { id: utterance ?? "" }, query: { hopMs: 10 } }),
    enabled: !!utterance,
    staleTime: Infinity,
    retry: false,
    select: (p): PeakPyramid => peaksFromApi(p),
  });
}

let worker: Worker | null | undefined;
let nextId = 1;
const pending = new Map<number, (r: StftResponse) => void>();

/** The STFT of one channel, in the shared Web Worker when there is one (jsdom has none: the main thread then). */
export function computeStft(pcm: Float32Array, params: StftParams): Promise<{ frames: number; u8: Uint8Array }> {
  if (worker === undefined) {
    try {
      worker = typeof Worker === "undefined" ? null : new Worker(new URL("./stft.worker.ts", import.meta.url), { type: "module" });
      if (worker) {
        worker.onmessage = (e: MessageEvent<StftResponse>) => {
          pending.get(e.data.id)?.(e.data);
          pending.delete(e.data.id);
        };
      }
    } catch {
      worker = null;
    }
  }
  if (!worker) return Promise.resolve(stftU8(pcm, params));
  const w = worker;
  const id = nextId++;
  return new Promise((resolve) => {
    pending.set(id, resolve);
    const req: StftRequest = { id, pcm, params };
    w.postMessage(req, [pcm.buffer]);
  });
}

/** Resamples linearly to 16 kHz (audio.get serves 16 kHz already; this covers a foreign rate). */
function to16k(pcm: Float32Array, rate: number): Float32Array {
  if (rate === 16000) return pcm;
  const n = Math.floor((pcm.length * 16000) / rate);
  const out = new Float32Array(n);
  for (let i = 0; i < n; i++) {
    const t = (i * rate) / 16000;
    const a = Math.floor(t);
    const f = t - a;
    out[i] = (pcm[a] ?? 0) * (1 - f) + (pcm[a + 1] ?? pcm[a] ?? 0) * f;
  }
  return out;
}

export type SpecState = { source: SpecSource | null; note: string; loading: boolean };

/**
 * The spectrogram of the utterance: computed in the browser from the served PCM when the audio is short enough,
 * otherwise the server pyramid; a note says why there is none.
 */
export function useSpectrogram(utterance: string | undefined, link: AudioLink | undefined, d: AudioDefaults | undefined, enabled: boolean): SpecState {
  const long = !!link && link.durationS > (d?.browserStftMaxS ?? Infinity);
  const manifest = useQuery({
    ...spectrogramGetOptions({ path: { id: utterance ?? "" } }),
    enabled: enabled && !!utterance && long,
    staleTime: Infinity,
    retry: false,
  });
  const [browser, setBrowser] = useState<SpecState>({ source: null, note: "", loading: false });
  const url = link?.url;
  const origin = link?.originSampleRate ?? 16000;
  useEffect(() => {
    if (!enabled || !url || !d || long) return;
    let cancelled = false;
    setBrowser({ source: null, note: "", loading: true });
    (async () => {
      const res = await fetch(url, { credentials: "same-origin", headers: { [CLIENT_HEADER]: "web" } });
      if (!res.ok) throw new Error(`audio ${res.status}`);
      const audio = decodeWav(await res.arrayBuffer());
      const shape = stftShape(d, origin);
      const params = { win: shape.win, hop: shape.hop, nFft: shape.nFft, bins: shape.bins };
      const channels = await Promise.all(audio.channels.map((c) => computeStft(to16k(c, audio.sampleRate), params)));
      if (cancelled) return;
      const src = new MemorySource(
        `stft:${utterance}`,
        channels.map((c) => ({ frameMajor: c.u8, frames: c.frames })),
        shape.bins,
        shape.binHz,
        shape.hop / 16000,
        shape.fmaxHz,
      );
      setBrowser({ source: src, note: "", loading: false });
    })().catch((e: unknown) => {
      if (!cancelled) setBrowser({ source: null, note: `The spectrogram could not be computed: ${e instanceof Error ? e.message : String(e)}`, loading: false });
    });
    return () => {
      cancelled = true;
    };
  }, [enabled, url, d, long, origin, utterance]);

  const [server, setServer] = useState<SpecSource | null>(null);
  const m = manifest.data;
  useEffect(() => {
    if (!m || !d || !utterance) return;
    const fetcher = async (path: string) => {
      const res = await fetch(`${API}/registry/utterances/${encodeURIComponent(utterance)}/spectrogram?tile=${encodeURIComponent(path)}`, {
        credentials: "same-origin",
        headers: { [CLIENT_HEADER]: "web" },
      });
      if (!res.ok) throw new Error(`tile ${res.status}`);
      return new Uint8Array(await res.arrayBuffer());
    };
    const src = new ServerSource(m, fetcher, d.tileCacheMb * 1024 * 1024);
    let cancelled = false;
    void src.warm().then(() => !cancelled && setServer(src));
    return () => {
      cancelled = true;
    };
  }, [m, d, utterance]);

  if (!enabled) return { source: null, note: "", loading: false };
  if (long) {
    if (manifest.isError) return { source: null, note: "No spectrogram for audio this long yet: its tile pyramid (spectrogram_tiles) was not computed.", loading: false };
    return { source: server, note: "", loading: !server };
  }
  return browser;
}
