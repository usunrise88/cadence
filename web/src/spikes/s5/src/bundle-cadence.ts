// Bundle-size probe (s5.spec.ts): the Cadence view code (axis, ruler, canvas waveform, WebGL2 renderer, word tracks,
// MSE playback, tile sources; the STFT worker and its WASM are a separate lazy chunk).
export { AudioView } from "./view";
export { mseAudio } from "./mse";
export { PyramidSource, StftSource, Peaks, Features } from "./sources";
