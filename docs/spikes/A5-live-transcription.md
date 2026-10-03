# A5 — Live transcription from the browser microphone

Status: partial — the live path works and meets the 160 ms budget beside training; live equals file decode only when
evals use the same pipeline decoder; Firefox/Safari and Caddy are left to the owner
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
Run on 2026-10-02 on the stand host, in about 3½ hours of the 2-day box.
- **Card:** one RTX PRO 6000 Blackwell, 96 GB, shared with the owner's services (67.4 GB resident, constant
  throughout).
- **Runtime:** the stand's image `cadence/worker:635702e` (NeMo 3.0.0, torch 2.12, Python 3.13), in our own
  containers.
- **Model:** `nvidia/nemotron-3.5-asr-streaming-0.6b` at `ea30d66d…`, read-only from the stand's HF cache, fp32.
- **Clips:**
  - the NeMo pack's ten FLEURS he_il fixtures (2.9–3.7 s);
  - twelve FLEURS ru_ru test clips (4–15 s), copied read-only out of the stand's content store (`replay-golden-ru-ru`).

Everything is throwaway code under `docs/spikes/a5/`:

| File | What it is |
| --- | --- |
| `live_core.py` | The worker-side decoder on NeMo's pipeline API, the shims, the streaming resampler and the telephony chain |
| `live_worker.py` | The `live` job: it dials the relay and speaks the R48 messages |
| `relay/` | The Go relay, using `coder/websocket` |
| `bench.py` | The paced, endpointing and fast client, with wall-clock measurements |
| `fast_decode.py`, `compare.py`, `report.py` | The three file decodes and the comparisons |
| `train_load.py` | The capped fine-tuning load for the co-run |
| `checks.py` | The gate checks (Origin, ticket) |
| `web/` | The capture page, the AudioWorklet and the Playwright driver |
| `run.sh`, `gpuwatch.sh`, `bench_all.sh`, `fast_all.sh` | Runners |

Data and outputs are in `~/cadence-spikes/a5/`, which is outside the repository.

### Verdict per acceptance line

| Acceptance (at `160ms`) | Result |
| --- | --- |
| p95 from `finalize` to the last final ≤ chunk + 100 ms = 260 ms, relay included | **Pass:** 43 ms at 16 kHz and 42 ms with a 48 kHz microphone. Beside training it is 70 ms, and 92 ms in the endpointing session. Two targets (160 ms and 1120 ms) are finalized together. |
| Live, paced replay and file decode give identical words | **Pass for the pipeline decoder, fail against today's eval decoder.** A paced replay through the relay equals the same code decoding as fast as it can in 22/22 clips (he and ru) at 80, 160 and 1120 ms. It equals NeMo's own `pipeline.run` in 22/22 at 160 ms and 21/22 at 80 and 1120 ms; in both misses `pipeline.run` drops the clip's last word. It agrees with the pack's own cache-aware loop (`nemotron_transcribe@1`, which evals use) in only 10/22 clips at 160 ms and 8/22 at 1120 ms, at a similar WER. That is a different decoder, not a bug: see proposal 2. |
| The training job keeps running without OOM while a live session is open | **Pass at a 14 GB cap.** 900 s of real fine-tuning steps under `gpu.apply_cap(14000)` ran beside three live sessions: 13.8 GB in nvidia-smi, 10 946 steps, no OOM. The owner's services were unchanged. The A3-size cap (22 GB) did not fit next to the residents at that moment and was not run. |
| Chrome, Firefox and Safari all capture and stream | **Chromium only:** headless Chromium 153 through Playwright 1.63 with a fake microphone. Firefox and Safari are left to the owner (checklist below). |

### Numbers per profile
Clips were paced at real time through the relay. "16k" sends the clips' own samples; "48k" upsamples them on the
client, as a 48 kHz microphone would. Latencies are client wall clock: p50 / p95.

| | 80 ms (1 target) | 160 ms (with 1120 ms) | 1120 ms (with 160 ms) |
| --- | --- | --- | --- |
| Model load: restore of the `.nemo` / process ready | 22.2 s / 30.2 s | 22.1 s / 30.3 s, one model shared by both targets | same as 160 ms |
| Time to first partial after speech onset (ru, energy VAD, 240 ms rule) | 683 / 1 052 ms | 586 / 969 ms | 1 206 / 1 598 ms |
| Partial lag (receive time minus the time its audio end was captured), 16k | 13 / 15 ms | 15 / 30 ms | 29 / 32 ms |
| Partial lag, 48k with 80 ms frames | 15 / 93 ms | 16 / 95 ms | 30 / 110 ms |
| Partial lag, 48k with 20 ms frames | 14 / 33 ms | — | — |
| `finalize` → last final, 16k | 22 / 27 ms | 30 / 43 ms (both targets) | same |
| Same, 48k | 22 / 24 ms | 27 / 42 ms | same |
| Same, beside training (16k / 48k f20 / endpointing session) | — | 54 / 70, 51 / 84, 55 / 92 ms | same |
| Automatic endpoint: utterance end → final (`stop_history_eou` 800 ms) | 1 534 / 2 240 ms | 1 494 / 2 228 ms | 2 048 / 2 427 ms |
| Early end-of-utterance mid-utterance | 12 of 12 clips, a word split in 10 of 22 | 0 | 0 |
| GPU step per chunk | 12 / 14 ms | 13.7 / 16.5 ms | 13.7 / 15.7 ms |
| Real-time factor of the job, paced (all targets) | 0.15 | 0.10 for both targets | |
| Real-time factor, fast file mode | 0.12 (live path); `pipeline.run` batch 8: 0.022–0.05 | 0.07 for both; `pipeline.run` 0.013–0.04 | `pipeline.run` 0.004–0.03 |
| WER on 12 ru clips (light normalisation) | 15.2 | 16.7 | 16.7 |
| WER on 10 he clips (base model; 2–5 clips decode empty) | 82.6 | 80.2 | 67.4 |

Speech onset comes from an energy VAD. Silero VAD is not in the image, and its licence check (R26) is stream R's. Two
ru clips open with a click that a 4-of-5-frame rule takes for speech: their first partial then lands 2.7 s "late", so
the stricter 240 ms rule is used.

- **GPU memory of a live job** (nvidia-smi, process):
  - 3 624 MiB with one target;
  - 3 728 MiB with two targets on one checkpoint;
  - 5 610 MiB peak while loading: the state dict and the model are briefly on the card together.
  - PyTorch's own figures: 2.5–2.6 GiB allocated after load (fp32 weights), 2.73–2.83 GiB peak reserved per session.
- **Relay overhead** (the Go relay's own forwarding time per message, read to write done):
  - up 14–23 µs p50, 27–62 µs p95;
  - down 13–22 µs p50, 27–42 µs p95.
  - Round trip client→relay→client (the relay answers `ping`): 0.1–0.2 ms. To the worker and back (`keepalive` →
    `pong`): 0.3 ms p50 and 7–11 ms p95, because a pong waits behind a decode step.
- **Co-run:**
  - Training alone ran at 12.3 steps/s (batch 2, clips ≤ 9 s, bf16, AdamW), and 11.85 steps/s beside a live session
    (−3 %).
  - The live job's GPU step went from 13 ms to 20 ms p50 (39 ms p95), and its real-time factor from 0.10 to 0.17.
  - Words were still identical: 12/12.
- **Telephony** (16 → 8 kHz → G.711 μ-law → 16 kHz, streaming): latency is unchanged. Words differ from the clean
  audio, as expected. ru WER: 18.7 at 160 ms (clean 16.7), 15.2 at 1120 ms, 15.7 at 80 ms.

### Browser (headless Chromium 153, fake microphone playing ru03.wav)
- **Capture:**
  - `getUserMedia` with EC/NS/AGC off returned them off. The raw track reports `channelCount: 2` and `sampleRate:
    44100`; with them on, it reports 1 channel at 48000. So the channel-0 rule matters in Chromium too.
  - `AudioContext` ran at 44 100 Hz, so a frame is 3 528 samples (7 056 bytes); the worker resampled 44.1k → 16k.
- **Session:**
  - It streamed for 9 s: 40 partials, then finals "В" (an early end of utterance) and "различных местах Рима … за
    церемонией".
  - `finalize` → final took 14 ms.
- **Gates:**
  - Reusing the ticket was refused (403).
  - A dropped socket (`close 4000`) was followed by a new `transcriptions.new` and a new socket, which streamed again,
    with a `summary` and close 1000.
  - `checks.py`: a foreign Origin, a missing Origin and a wrong ticket were all refused with 403 before the upgrade.

### What the owner should check by hand (Firefox, Safari, Caddy)
To run it:
- start `relay -listen 127.0.0.1:18480 -origins https://<host> -static docs/spikes/a5/web`;
- start `live_worker.py` with `run.sh --gpu`;
- add a Caddy route with basic auth that `reverse_proxy`es to it;
- open `https://<host>/` on a laptop. Then:
1. **Permission prompt:**
   - the prompt appears;
   - "Allow once" works;
   - Safari asks again after a reload.
2. **The log line `started at … Hz, settings …`:**
   - `echoCancellation`, `noiseSuppression` and `autoGainControl` are `false`;
   - `channelCount` is reported (Safari is expected to report 2 with EC off);
   - the `AudioContext` rate: Firefox may report no `sampleRate` in `getSettings()`, and the page uses the context's
     rate.
3. **Safari:** the context starts suspended unless resumed inside the click; the page calls `resume()`. Check that
   partials appear.
4. **WebSocket through Caddy with basic auth:**
   - the upgrade succeeds;
   - Safari re-sends the cached credentials on `wss://`;
   - the `Origin` the browser sends is exactly the allow-listed `https://<host>`.
5. Speak, then press **Finalize**: the final should come in about 100 ms.
6. Press **End**: a `summary` arrives, then close 1000.
7. Drop the network for a few seconds: the old socket closes, and a new start needs a new session (a new ticket).
8. Firefox and Safari with **"raw microphone" off**: `getSettings()` flips to true, and the audio audibly changes.

### Surprises
1. **NeMo 3.0.0's streaming pipeline API is silent for Nemotron 3.5 as shipped.**
   - `CacheAwareRNNTInferenceWrapper.execute_step` accepts the per-stream `prompt_vectors` the pipeline builds from
     `ASRRequestOptions.language_code`, but never applies them.
   - The prompt model's joint therefore gets un-prompted encoder output and emits only blanks: every transcript was
     empty.
   - `patch_prompt()` in `live_core.py` applies `prompt_kernel(cat(encoded, one-hot))` per stream, which is what
     `PromptStreamingMixin._apply_prompt_to_encoded` does for its single global index. That fixes it, and gives a
     language per stream for free.
   - The pipeline also keeps the trailing `<xx-XX>` tag in its text and words; we strip it.
   - Both gaps belong upstream.
2. **160 ms is not a trained look-ahead of this model.**
   - The model lists `att_context_size_all = [[56,0],[56,3],[56,6],[56,13]]`, which is 80, 320, 560 and 1120 ms. NeMo
     warns on `[56,1]` and runs it anyway.
   - R20's primary cell, the gate's `primaryProfile: 160ms`, sits on an interpolated look-ahead. The WER is in line
     with its neighbours (A3 found the same), but this is a deliberate choice for the owner to confirm.
3. **At 80 ms the end-of-utterance detector fires right after the first token in every clip.**
   - In 10 of 22 clips that is mid-word ("Ма" | "дагаскар").
   - The pipeline joins such finals without a space; a client must too. That is the `space: false` field below.
4. **The decoders differ.** The pack's loop (whole-file features, `CacheAwareStreamingAudioBuffer`) and the pipeline
   (chunked cache features) agree on most but not all words. The "same words" promise holds only if live and eval run
   the same decoder.
5. **The input must be bit-exact.**
   - Converting the clips to PCM16 with `×32767` and truncation instead of `×32768` and rounding changed 7 of 10 base
     Hebrew transcripts: the base model is that close to blank on short Hebrew clips.
   - "Identical words" is only testable on identical samples. That is another reason a file should go to the worker as
     bytes (R47), not as browser-decoded PCM.
6. **80 ms frames with a 48 kHz microphone add one frame of lag at times.**
   - The streaming polyphase resampler (exactly `resample_poly`, frame by frame) needs about 1 ms of look-ahead.
   - When chunk boundaries line up with frame boundaries, a chunk then waits for the next 80 ms frame: p95 partial
     lag is 93 ms.
   - With 20 ms frames it is 33 ms. The relay cost of 4× more messages is negligible.
7. **Loading takes 22 s.**
   - A `.nemo` restore unpacks 2.5 GB into the temp directory. The stand host's root disk had 3 GB free, so we used a
     tmpfs (`run.sh`).
   - Two targets of one checkpoint share one model: `att_context_size` is switched on the encoder before each step,
     which only recomputes the streaming config. A second pipeline costs about 8 s of setup and no weights.
8. **Small operational points:**
   - The relay paired a session with a stale job socket left by a killed worker; it must ping idle job sockets before
     pairing.
   - Messages from Python carry `"type": "x"` with a space, so the relay reads `type` tolerantly.

### What the spec should change (proposals; not edited here)
1. **The worker live-job protocol (R48, 06):**
   - The job is a long-poll lease of kind `interactive`. Its worker dials `GET /worker/live/{jobId}` (Bearer `cwk_`)
     once per session and gets the browser's messages byte-for-byte; the relay never parses beyond `type`.
   - The job loads every target before it dials. A target is a checkpoint and a profile; targets of one checkpoint
     share weights.
   - `started.targets[].loadS` reports the load.
   - `finalize` closes the decoder's stream: the right context is padded with silence and end-of-utterance is forced.
     The next audio opens a new stream with a fresh encoder cache, so the 4.48 s of left context is reset. Say so:
     `finalize` is a segment boundary, not just a flush.
   - Decode per stream in one thread. `keepalive` and `finalize` queue behind audio in order, which is what makes
     finalize latency well defined.
   - The relay pings job sockets before pairing.
2. **One decoder for live and evals (to stream Y, R43/R22):**
   - `nemotron_transcribe@2` should decode through NeMo's cache-aware pipeline with the two shims, chunked per profile
     and batched across utterances (`pipeline.run` RTF 0.013–0.04 at batch 8). Live sessions, paced replays and evals
     then give identical words.
   - Keep the shims in the pack (`cadence_nemo/live.py`) until upstream fixes them, and record `"decoder":
     "nemo-pipeline-cache-aware"` in the decoding hash, so old `@1` cells do not mix with new ones.
3. **Latency profiles (R43/R20):**
   - Mark which profiles are trained: `trained: true` for 80, 320, 560 and 1120 ms; 160 ms is interpolated.
   - The owner decides whether the primary cell stays at 160 ms or moves to 320 ms.
   - The family descriptor's `latencyProfiles[]` gains `trained`.
4. **R49 memory reservation for the Nemotron 0.6B family:**
   - `interactive.memoryMb: 6000`: 3.7 GB steady plus the 5.6 GB load peak and margin;
   - `+2600` per additional distinct checkpoint (fp32 weights);
   - targets of the same checkpoint are free.
   - The placeholder "3 GB" is too low by the load peak.
   - bf16 inference would roughly halve the weights, but changes words against fp32 evals; not measured.
   - The daily allowance should count wall time, not GPU-busy time: RTF 0.10–0.17 means the card is mostly idle.
5. **Message schema details (R48 `LiveServerMessage`, as the prototype used them):**
   - `started {targets:[{target, profile, chunkMs, language, loadS}], captureRate, resampler, telephony, input}`;
   - `partial {target, segment, seq, text, audioEnd}` (seconds of session audio covered);
   - `final {target, segment, seq, text, words:[{word,start,end,confidence}], endpoint: eou|finalize|end, audioEnd,
     space}`. `space: false` means "continues the previous final's last word". `text` is the transcript, not the
     words joined: at 80 ms NeMo's word segmenter can split a word;
   - `stats {source: worker|relay, rtf, audioS | upP50Us…}`;
   - `pong {source, t}`; `summary {audioS, rtf, targets{…stepMs}, gpu, load}`.
   - `seq` is per target and covers partials and finals. `ping` is answered by the relay and `keepalive` by the
     worker, so the page can show both hops.
6. **Frames:** "PCM16 frames of 20 ms" (or "≤ 20 ms") instead of 80 ms. The worker's chunking (a profile's chunk) is
   independent of the frame size.
7. **One resampler (R50, 03 Augmentation):**
   - Live telephony uses streaming polyphase (`resample_poly`, the import resampler).
   - The training augmentation's `telephone` stage uses an FFT resampler, which cannot stream.
   - Make polyphase the training resampler for both, so "telephony simulation" equals what training saw.
8. **Endpointing:**
   - With `stop_history_eou` 800 ms the automatic endpoint lands 1.5 s (160 ms) to 2.0 s (1120 ms) after the
     utterance ends, p95 2.2–2.4 s.
   - Expose `stop_history_eou` per target (it is already a per-stream request option), default 800 ms.
   - At 80 ms suppress an end-of-utterance whose segment is shorter than one word, or merge it into the next segment.
9. **Disk:** the worker needs about 3 GB of temp space per model load (the `.nemo` unpack). Point `TMPDIR` at the
   scratch volume, or pre-extract models when a version is materialised.
