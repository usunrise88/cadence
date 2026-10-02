---
title: spectrogram_tiles (step kind)
summary: The server tile pyramid of an audio's acoustic spectrogram — uint8 dB tiles of 512 frames at 10 ms, max-pooled levels — for audio too long for the browser to compute.
contexts: [step:spectrogram_tiles, artifact:spectrogram_tiles]
---

## What this is

`spectrogram_tiles@1` is a core step kind (CPU, job kind `data`, runtime-neutral: it ships in every runtime image and
needs NumPy, which every image has). It reads one input, `audio` (an audio file artifact: an utterance's canonical WAV,
or any format the image's `soundfile` reads), and writes `tiles`, a directory artifact the audio view reads through
`spectrogram.get`:

- `manifest.json` — `schema` `cadence.spectrogram-tiles/1`, `audio` (the input's `b3:` hash), `sampleRate` (16 000:
  the audio is resampled to it per channel), `originSampleRate`, `channels`, `windowSamples`, `hopSamples`, `nFft`,
  `bins` (kept up to the origin's Nyquist: 257 for 16 kHz audio, 129 for audio of 8 kHz origin), `binHz`,
  `tileFrames` (512), `encoding` (`floorDb` -120, `stepDb` 0.5), `levels` (`level`, `hopS`, `frames`, `tiles`),
  `peakDb` per channel and `durationS`.
- `c<channel>/l<level>/<index>.u8` — one tile: `bins × 512` bytes, row-major with bin 0 first (ready for a WebGL2 R8
  texture). A byte `v` is `-120 + 0.5 × v` dB re full scale (a full-scale sine's bin reads 0 dB); the last tile of a
  level is zero-padded.

How it is computed: a periodic Hann window of `window_ms` centred in an `n_fft`-point frame, every `hop_ms`, frames
centred with zero padding at both ends (librosa's `center=True`, constant padding), magnitudes scaled so a sine's peak
reads its amplitude. Level 0 is the hop grid; level L takes the maximum over 2^L frames (pairs of the level below, an
odd last frame paired with itself), so a peak never disappears when zoomed out. Levels stop when one tile holds them.
The browser computes the same STFT in a Web Worker for audio up to `views.audio.browser_stft_max_s`, so both paths
draw alike.

Size: about 93 MB for a 30-minute 8 kHz stereo call (10 levels, twice the base level), 132 KB per tile at 16 kHz;
tiles do not compress (the noise floor is incompressible), so they are stored raw (spike S5).

## Place in the loop

Review. The audio view (Audio panel; later Diff, Triage, Transcription) shows a spectrogram for every utterance; for
audio longer than `browser_stft_max_s` (10 minutes) it reads this pyramid instead of computing it. The control plane
finds the newest `spectrogram_tiles` artifact whose `meta.audio` is the utterance's hash. Phase 3 golden sets hold
short utterances, so nothing starts this step automatically yet; long calls arrive with production data in phase 4.

## Fields and defaults

| Parameter | Default | Source | Range |
| --- | --- | --- | --- |
| `window_ms` | `views.audio.window_ms` (25 ms) | R52 ("Model frames") | 5–100 |
| `hop_ms` | `views.audio.hop_ms` (10 ms) | R52 | 5–50 |
| `n_fft` | `views.audio.n_fft` (512) | R52 | 256–1024 |
| `tile_frames` | `views.audio.tile_frames` (512) | Spike S5 | 512 |

Output meta: `audio`, `levels`, `channels`, `bins`, `bytes`.

## Commands

- `spectrogram.get` (people only; tag `media`) — the manifest of the pyramid computed for an utterance's audio, or one
  tile with `tile=c<ch>/l<L>/<i>`.
- `artifacts.get` — the artifact's files and meta.

## Playbooks

- **A long call shows no spectrogram.** Its pyramid was not computed: run a pipeline with this step on the call's
  audio artifact, then reopen the view.

## Sources

- docs/spikes/S5-audio-view.md (tile format, sizes, the comparison with librosa: within 0.25 dB, the quantisation
  step).
- docs/spec/08-resolutions.md R52 (two modes, defaults, computation); docs/spec/10-ui-shell.md "Audio view and charts".
