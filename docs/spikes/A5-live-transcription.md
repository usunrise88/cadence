# A5 — Live transcription from the browser microphone

Status: todo
Box: 2 days

## Goal
Prove the live path of R48 end to end and measure what a person will feel: microphone → WebSocket through Caddy →
control-plane relay → worker `live` job → Nemotron 3.5 cache-aware decoding → partial and final words back.

## Setup
Staging host; NeMo Speech 26.07 runtime with NeMo's streaming pipeline API (`nemo.collections.asr.inference`,
cache-aware RNNT pipeline); nvidia/nemotron-3.5-asr-streaming-0.6b at the pinned revision, profiles `80ms`, `160ms`,
`1120ms`; Chrome, Firefox and Safari on a laptop microphone; the site behind Caddy with basic auth (until phase 1
sign-in); a training job holding the card under its 24 GB cap for the co-run step.

## Steps
1. Capture with an AudioWorklet at the device rate (echo cancellation, noise suppression and auto gain off; channel 0
   only), send 80 ms PCM16 frames; resample in the worker with the training resampler.
2. Decode per stream id at each profile; emit `partial` (replaces the previous one), `final` (words with audio-time
   spans, never revised) and endpoints; `finalize` pads the right context with silence.
3. Measure per profile: model load time; time to first partial after speech onset; time from `finalize` to the last
   final; time from utterance end (Silero VAD on the recording) to its final with automatic endpointing; real-time
   factor; GPU memory of the job.
4. Send a fixture file through the same socket at real-time pace; its words must equal a fast decode of the same file.
5. Repeat step 3 while a training job runs under the cap, then with telephony simulation (8 kHz, G.711) on; open two
   targets on the same audio.
6. Per browser: permission prompts, raw capture settings as reported by `getSettings()`, the WebSocket through Caddy
   with basic auth, single-use ticket and Origin check, reconnect after a dropped socket.

## Acceptance
At `160ms`: p95 from `finalize` to the last final ≤ chunk + 100 ms (the R31 budget, relay included); live, paced
replay and file decode produce identical words; the training job keeps running without OOM while a live session is
open; Chrome, Firefox and Safari all capture and stream.

## Record
Latencies per profile (p50/p95), load time, GPU memory of a live job, real-time factor, relay overhead, the automatic
endpointing delay, browser quirks.

## Result
_(fill in: what worked, numbers, surprises, what the spec should change)_
