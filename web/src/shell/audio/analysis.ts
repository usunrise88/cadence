// The audio view's analysis tracks (R51 "Energy, VAD"; phase 4): the level in dBFS, speech regions and estimated
// bandwidth of each channel, computed by the server from the audio (tracks.get, tag media), and the end-of-utterance
// gap of an annotation item's window. Audio of 8 kHz origin (a telephone call) is narrowband: the spectrogram stops
// at 4 kHz (R52).
import { useQuery } from "@tanstack/react-query";
import { tracksGetOptions } from "@/api/gen/@tanstack/react-query.gen";
import type { AudioTracks } from "@/api/gen/types.gen";

export type AnalysisChannel = {
  channel: number;
  role?: string;
  levelDb: number[];
  speech: [number, number][];
  bandwidthHz: number;
  narrowband: boolean;
};

export type AnalysisData = {
  /** Seconds per level value. */
  hopS: number;
  /** Where the level track starts on the view's axis (0: the audio's start). */
  offset: number;
  channels: AnalysisChannel[];
  narrowband: boolean;
  bandwidthHz: number;
  eou?: { speechEnd: number; nextSpeech?: number; gapS?: number };
};

/** The tracks of a tracks.get answer, on the axis of the audio they were computed from. */
export function analysisFromApi(t: AudioTracks): AnalysisData {
  return {
    hopS: t.hopS,
    offset: 0,
    narrowband: t.narrowband,
    bandwidthHz: t.bandwidthHz,
    channels: t.channels.map((c) => ({
      channel: c.channel,
      role: c.role,
      levelDb: c.levelDb,
      speech: c.speech.map((s) => [s[0] ?? 0, s[1] ?? 0] as [number, number]),
      bandwidthHz: c.bandwidthHz,
      narrowband: c.narrowband,
    })),
    eou: t.eou ? { speechEnd: t.eou.speechEnd ?? 0, nextSpeech: t.eou.nextSpeech, gapS: t.eou.gapS } : undefined,
  };
}

/** The narrowband cap of the spectrogram (Hz) when the audio is of 8 kHz origin, else undefined. */
export function narrowbandCap(a: AnalysisData | undefined): number | undefined {
  return a?.narrowband ? 4000 : undefined;
}

/** tracks.get of an utterance, an annotation item's window (bit_…) or a triage item's (tri_…). */
export function useTracks(utterance: string | undefined, enabled = true) {
  return useQuery({
    ...tracksGetOptions({ path: { id: utterance ?? "" } }),
    enabled: enabled && !!utterance,
    staleTime: Infinity,
    retry: false,
    select: analysisFromApi,
  });
}
