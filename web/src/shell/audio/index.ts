// The audio view (R51, R52): the only way panels show or play audio. Panels import from this index.
export { AudioView, hypothesisTrack, useAudioDefaults, useWords, type AudioViewProps } from "./AudioView";
export { analysisFromApi, narrowbandCap, useTracks, type AnalysisChannel, type AnalysisData } from "./analysis";
export { decodeAudioFile, LocalAudioView, type LocalAudioViewProps } from "./LocalAudioView";
export { AudioAxis, formatTime, useAudioAxis, useAxisState, type AxisState } from "./axis";
export { focusedAudio, onFocusedAudio, type AudioController } from "./controller";
export type { AudioEngine, SpecSettings } from "./engine";
export { toCtm, toTextGrid, toWebVtt, type ExportWord } from "./exports";
export { COLORMAPS, type Colormap } from "./renderer";
export { audioItem, parseAudioItem, spanFragment, spanReference, type AudioTarget } from "./selection";
export { AUDIO_PANEL, openAudio, useAudioItem, useAudioTarget } from "./target";
export { referenceTrack, type TrackWord, type WordTrackData } from "./words";
