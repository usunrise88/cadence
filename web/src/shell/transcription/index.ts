// Manual transcription tests (R47–R50): the live channel's client, the lanes' state, microphone capture and the
// page-side reference check. Panels reach it through the panel SDK (@/shell/panel).
export { alignmentWords, alignWords, compareText } from "./align";
export { audioInputs, captureConstraints, captureUnavailable, startCapture, type Capture, type CaptureOptions } from "./capture";
export { CLOSE_MEANING, closeMeaning, LiveClient, parseServerMessage, socketUrl, type LiveClientOptions, type LiveHandlers, type SocketLike } from "./client";
export {
  finalText,
  initialLive,
  laneText,
  laneWords,
  liveReducer,
  percentile,
  sentAt,
  type FinalSegment,
  type Lane,
  type LaneTarget,
  type LiveAction,
  type LivePhase,
  type LiveState,
} from "./lanes";
export { Framer, frameSamples, pcm16ToFloat, toInt16, type PcmFrame } from "./pcm";
