# S5 — Audio view: long calls, many tracks, many windows

Status: todo
Box: 2 days

## Goal
Show that one `AudioView` (R51, R52) renders smoothly within the browser's WebGL limits, in docked, floating and
popout panels, for two inputs:
- a 30-minute stereo call;
- a 20-second 16 kHz clip.

Each input carries a waveform, a spectrogram, the model input and word tracks. Decide whether wavesurfer.js 8
provides the waveform, regions, timeline and minimap tracks, or whether those are Cadence code too.

## Setup
- The phase-0 shell.
- Audio fixtures: a 30-minute 8 kHz stereo call and a 20-second 16 kHz clip.
- For the call, peaks and a uint8 dB tile pyramid (10 ms base level), computed by a throwaway script in the shape of
  the future worker step.
- A synthetic `analysis` array (80 × frames, float16).
- Two word tracks of 5 000 words each, Hebrew included.
- Headless Chromium for the measurements, plus manual checks in Firefox and Safari.

## Steps
1. Compute the STFT in a Web Worker with a WASM FFT (25 ms Hann, hop 10 ms, 512 points). Compare it with librosa on
   the fixtures.
2. Render with WebGL2: R8 magnitude textures and a 256×1 colour lookup texture. Then:
   - change range, gain and colormap (magma, viridis, grey);
   - zoom from the whole call down to 2 seconds and back, through the tile pyramid;
   - scroll during playback, with the playhead followed.
3. Try the wavesurfer.js 8 variant: waveform, regions, timeline and minimap following the view's external time axis,
   inside a popout window (its `ownerDocument`), with playback through a Media Source Extensions element. Compare it
   with a Cadence canvas waveform over the same peaks.
4. Open 12 audio views across two popouts and count the WebGL contexts. Force a context loss (`WEBGL_lose_context`)
   and check that every view comes back.
5. Check the rest:
   - theme switch;
   - reduced motion;
   - keyboard-only use (play, seek, zoom, loop, step between words);
   - the screen-reader summary;
   - Hebrew words inside tracks (bidi isolation, digits and Latin text).

## Acceptance
- Magnitudes are within 0.5 dB of librosa.
- Zoom and scroll hold p95 frame time ≤ 16.7 ms, measured as S1 measures.
- A range or colormap change takes under one frame.
- No view is blank with 12 open or after a context loss.
- The 30-minute call stays under 150 MB of memory.
- The wavesurfer.js decision is recorded with its reason.

## Record
- Frame times per operation.
- FFT time per minute of audio.
- Tile sizes.
- Memory use.
- Context count.
- wavesurfer.js findings: popout, external time axis, bundle size.
- Browser differences.

## Result
_(fill in)_
