# S5 — Audio view: long calls, many tracks, many windows

Status: done with caveats
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

**Passed in headless Chromium, with two caveats:** memory counts as under 150 MB only for the renderer process
(the GPU process adds about 34 MB on SwiftShader), and Firefox and Safari still need the owner's manual pass (list
below). Measured on 2026-10-02 by `sh docs/spikes/s5/run.sh all`, which runs fixtures → tiles → `s5.spec.ts` → librosa
compare. The setup: Playwright 1.63 in `mcr.microsoft.com/playwright:v1.63.0-noble`, headless Chromium 153. No GPU:
WebGL2 runs on ANGLE over SwiftShader Vulkan (`--enable-unsafe-swiftshader`), a pessimistic bound. The harness is
Vite in dev mode with unminified React and Dockview 8.3.1. Frame times are measured as in S1: rAF intervals in the
window that hosts the view, tracing off. "Work" is the time the frame loop spends inside rAF (hooks plus every dirty
view). Raw numbers are in `web/test-results/s5/S5-{A,B,C,D,M}.json` and `compare.json`.

Fixtures (generated at run time, nothing committed):
- 20 s clip at 16 kHz: FLEURS sr speech read from the stand's import, plus a 1 kHz tone and a 100 Hz–7 kHz chirp.
- 30-minute 8 kHz stereo call: FLEURS turns alternating between the caller (left) and the bot (right), band-limited
  to 300–3400 Hz, with noise at -55 dBFS.
- Two word tracks of 5 000 words each: caller words from the FLEURS transcripts; bot words in Hebrew mixed with
  digits and Latin text (`050-1234567`, `₪249.90`, `WhatsApp`).
- 80-mel float16 log-mel `analysis` arrays for both inputs.

| Acceptance | Result |
| --- | --- |
| Magnitudes within 0.5 dB of librosa | **Yes.** Bins within 80 dB of the peak, compared with `librosa.stft(n_fft=512, win_length=400, hop_length=160, hann, center=True, constant pad)`. Browser float dB: max 0.003 dB with PFFFT/WASM, 0.000 dB with the JS FFT (clip: 2 001 frames, 497 362 bins). Browser uint8 (0.5 dB steps): max 0.25 dB, mean 0.125. Tile pyramid L0 against librosa on the call's first 5 minutes: max 0.25 dB, mean 0.125 (3.65 M bins). The quantisation step is the whole error |
| Zoom and scroll p95 ≤ 16.7 ms | **Yes.** Every pass: p50 16.7, p95 16.7–16.8 ms, 0 % of frames over 20 ms, 0 long animation frames. Full table below |
| Range or colormap change under one frame | **Yes.** From the change to pixels in the view's canvas, GPU finished (`gl.finish`): 4.3–5.0 ms for range 60/80, gain +6, viridis/grey/magma and Hz/mel axis on the docked call at 30 s. **0 texture uploads**: uniforms only |
| No view blank with 12 open or after a context loss | **Yes.** 14 views (the docked call, the floating clip, 6 per popout in two popouts) ran on **3 WebGL contexts**, one per window, with 0 blank. After `WEBGL_lose_context` in all three windows: restored in 235 ms, 0 blank during or after (views keep their last image in their own 2D canvas). After a colormap change to grey, every view redrew grey pixels, which proves each one renders again |
| 30-minute call under 150 MB | **Renderer: yes (≈ 130 MB); renderer + GPU process: no (≈ 165 MB).** PSS above an empty Dockview page after a full zoom/scroll/playback workout: renderer +131 MB (79–80 → 211–212 MB), GPU process +34 MB (SwiftShader: there texture memory is CPU memory). Attribution below; the media element alone is ≈ 32 MB |
| wavesurfer.js decision recorded | **Not used** (reason below). Waveform, regions, timeline and minimap stay Cadence code |

Frame times (p50 / p95 ms, work p50 / p95 ms per frame; 178–238 frames per pass):

| Operation | Window | Frames p50 / p95 | Work p50 / p95 |
| --- | --- | --- | --- |
| Call, zoom 30 min → 2 s, cold tiles (fetched during the zoom) | main, docked | 16.7 / 16.8 | 5.4 / 6.7 |
| Call, zoom 2 s → 30 min, cold | main | 16.7 / 16.8 | 5.4 / 6.8 |
| Call, zoom in / out, warm | main | 16.7 / 16.7–16.8 | 5.4 / 6.7–6.8 |
| Call, scroll at 2 s / 30 s / 5 min span (one width per second) | main | 16.7 / 16.7–16.8 | 3.9–4.9 / 4.6–5.4 |
| Call, playback with continuous follow, 2 s / 30 s span (MSE; media advanced 3.97 / 3.93 s in 4 s) | main | 16.7 / 16.7–16.8 | 4.6–5.0 / 5.3–5.6 |
| Clip, zoom 20 s ↔ 2 s; playback follow at 2 s | main, floating | 16.7 / 16.8 | 1.9–2.0 / 2.3–2.4 |
| Call (compact), zoom in cold / out / in warm | popout, 6 views open there | 16.7 / 16.7–16.8 | 2.7–2.9 / 3.4–4.6 |
| Call, playback follow at 2 s | popout | 16.7 / 16.7 | 2.1 / 2.4 |
| Docked call zoom while 12 popout views are open | main | 16.7 / 16.8 | 5.3 / 7.0 |
| Cadence canvas waveform only (stereo, 30 min), zoom in / out / scroll 30 s | popout | 16.7 / 16.7–16.8 | 0.5–0.7 / 1.1–1.7 |
| **Same axis path, plus wavesurfer.js following it** | popout | **33.3 / 66.8–83.3** (53–55 % over 20 ms, 70–74 long frames); scroll alone 16.7 / 16.7 | **17.2–17.6 / 62.4–62.9** |

Record:
- **FFT time per minute of 16 kHz audio** (worker; windowing, FFT, dB, uint8; 10 minutes timed):
  - PFFFT WASM SIMD (`@echogarden/pffft-wasm` 0.4.2, BSD-3-Clause, 54 KB, last release 2024-10): 45.3 ms/min.
  - JS (`fourier-transform` 2.5.1, MIT, released 2026-09): 45.6 ms/min.
  - The FFT kernel is not the cost; the per-frame windowing and `log10` are. So the 10-minute browser limit costs
    ≈ 0.45 s in a worker.
  - The 20 s clip's STFT took 14 ms; loading it with the fetch took 194 ms.
  - No maintained MIT/BSD WASM FFT exists (pffft-wasm and kissfft-wasm last shipped in 2024; webfft in 2024-01).
- **Tile sizes** (the throwaway step `docs/spikes/s5/tiles.py`):
  - Tiles hold 512 frames × the bins kept, as uint8 dB (floor -120 dB, step 0.5 dB). Bins are kept up to the
    origin's Nyquist: the 8 kHz call keeps 129 of 257 (66 KB per tile); 16 kHz audio keeps 257 (132 KB per tile).
  - 30-minute stereo call: 10 levels (L0 180 001 frames, 352 tiles per channel), 1 410 files, **93.1 MB** in all,
    about 2× the base level. gzip shrinks it only to 76 % (the noise floor is incompressible), so store it raw.
  - Compute: 3.2 s for the whole call (numpy STFT 1.8 s). Fetching a tile from the local dev server: p95 15–56 ms.
  - Zooming once through the whole call fetched 162–202 tiles.
  - Peaks (10 ms int8 min/max): 720 KB per channel-hour, so the call's peaks are 720 KB, plus 720 KB for the pyramid
    built in memory. R51's ≈ 450 KB per hour assumed coarser peaks.
- **Memory** (PSS, MB, renderer / GPU process; `S5-M`, two runs each, after `HeapProfiler.collectGarbage`):

  | State | Renderer | GPU process | Notes |
  | --- | --- | --- | --- |
  | Empty Dockview page | 79–80 | 44–46 | baseline |
  | One WebGL2 context with its tile array (128 slots × 512 × 257 = 16.8 MB) | 83 | 44–45 | +3 MB renderer |
  | Call loaded, not touched | 155–156 | 66–68 | of which the MSE media element ≈ 32 MB (renderer 124 without it), word tracks ≈ 2 MB |
  | Call after zoom, scroll and playback | 211–212 | 78–80 | tile byte cache 11.6 MB (LRU cap 24 MB), features 3.9 MB, MSE 3.7 MB, JS heap 10 MB |
  | 14 views in 3 windows | 315 | 133 | 3 tile arrays (50.5 MB) |

  What brings the call under 150 MB in all processes: tile slots 128 → 64, a 12 MB byte cache, and MSE segments
  around the playhead instead of the whole file.
- **Context count:**
  - Shared renderer: 1 per window (3 for 14 views), 0 losses Cadence did not force.
  - Naive variant, one context per track (`?mode=per-track`, same 14 views): 57 contexts created (popouts rebuild
    their views), **41 lost by Chrome** with "Too many active WebGL contexts. Oldest context will be lost." The views
    that could not redraw were the main window's two and all six in popout A. **Chrome's 16-context limit is shared
    by the opener and its popouts** (one renderer process), so the rule "one renderer per window" is required, not
    an optimisation.
- **wavesurfer.js 8.0.1** (BSD-3-Clause; waveform, regions, timeline and minimap):
  - Works in a Dockview popout: 12 canvases, none blank, all in the popout's document.
  - Its ResizeObserver follows a popout resize (1184 → 784 px).
  - Follows the external axis through `zoom(px/s)` + `setScrollTime`: scroll 1233.98 s against the axis's 1234.
  - Plays an MSE-backed element given as `media` together with precomputed `peaks` and `duration`: advanced 1.43 s
    in 1.5 s; its own clock read 101.43.
  - **But:**
    1. Every zoom step re-renders its canvases at the new pixels per second. The wrapper was 29 520–35 520 px wide
       for the call; work p95 was 62 ms, so zooming drops half its frames.
    2. **Region drag is dead in a popout.** Its draggable listens for `pointermove` on the *opener's* `window`: the
       same drag moved the region 5 → 12.3 s in the main window and 0 s in the popout. It also creates elements
       with the opener's `document` (adopted on append, which works) and runs on the opener's
       `requestAnimationFrame`.
    3. A region added before `ready` is clamped to zero length.
    4. 158 KB minified / **38 KB gzip** (core + regions + timeline + minimap), against 35 KB / **13.2 KB gzip** for
       the whole Cadence view: axis, ruler, waveform, WebGL renderer, words, MSE, tile sources. The STFT worker is a
       separate chunk.
- **Rest of step 5:**
  - **Keyboard only.** Two Tabs reach the docked view (`tabIndex=0`, `role=group`,
    `aria-roledescription="audio view"`). Then:
    - Space plays and pauses;
    - ←/→ seek by 10 % of the span;
    - +/− halve and double the span around the playhead;
    - `[`/`]` set the loop in and out, announced in the live region ("Loop 15:00.21 to 15:01.21");
    - `.`/`,` step between words, announced ("gotovo, 15:01.35"); the playhead moves to the word's start.
  - **Screen-reader summary** (`aria-describedby`, refreshed at most every 500 ms): "Call · 30 min · 8 kHz stereo:
    30:00, stereo, caller left and bot right. Showing 14:57.2 to 15:07.2. Playhead 15:01.3. Loop 15:00.21–15:01.21.
    Tracks: waveform, spectrogram, model input, Hypothesis · caller (5000 words), Hypothesis · bot (he) (5000
    words). Word at playhead: gotovo."
  - **Reduced motion.** A zoom key applies within one frame (10 → 5 s); without reduced motion, one frame in, the
    zoom is still animating (2 → 1.64 s). Playback follow pages instead of scrolling: 2 distinct starts in 180
    frames.
  - **Theme switch.** The tokens flip (`--s5-fg` goes from rgb(28,32,36) to rgb(237,238,240)); the spectrogram's
    pixels stay identical.
  - **Hebrew.** Every word is its own `<bdi dir=auto>` with `unicode-bidi: isolate`. Hebrew words compute
    `direction: rtl`; `050-1234567` and `Visa` compute `ltr`. The track itself stays `ltr` and its words keep time
    order left to right. Word tracks pool at most 300 visible words; above that they show 4 px density blocks
    (1 036–1 116 word DOM nodes for the call).
- **Other findings:**
  - **MSE quota.** Chromium refused the call as 48 kbps stereo Opus in WebM: "SourceBuffer is full" at 7.4 MB.
    At 8 kbps per channel the call is 3.7 MB and fits.
  - **Model-input LOD.** The model-input track for the call loads float16 chunks of 4 096 frames only when the span
    is ≤ 60 s. The 8 kHz origin dims the top ≈ 19 of 80 mel filters, which hold dither only.
  - **Moving to a popout.** Dockview moves a panel's DOM into the popout. The views must rebuild on
    `onDidLocationChange` (new document, window, renderer and scheduler); otherwise they keep drawing through the
    opener's realm.
  - **Not done:** hidden panels releasing textures (the LRU evicts them, and Dockview's `onlyWhenVisible` unmounts
    hidden panels anyway); the narrowband and Praat presets; emissions, a reference track and exports.

Browser differences: headless Chromium only. **For the owner, Firefox and Safari** (desktop). Start the harness with
`sh docs/spikes/s5/run.sh fixtures && sh docs/spikes/s5/run.sh tiles`, then `cd web/src/spikes/s5 && npm ci &&
../../../node_modules/.bin/vite --config vite.config.ts --host 0.0.0.0`, and open `http://<host>:5175/`. Then:
1. Both views draw: the call docked, the clip floating. The clip's spectrogram proves the WASM worker STFT ran
   (Safari needs WASM SIMD: 16.4+). The clip's model-input track proves R16F textures work.
2. Click the call view, press Tab until it has the focus ring, then press Space (audio plays; Safari: does WebM/Opus
   play through MSE? If not, note it, since R25 segments then need `audio/mp4`), ←, →, `+`, `-`, `[`, `]`, `.`,
   `,`. Ctrl/Cmd+wheel zooms; the wheel scrolls.
3. In the console run `await __s5.zoomSweep('call')` and `await __s5.scrollSweep('call', 2)`. Paste the returned
   p95 values.
4. In the console run `await __s5.openPopout('popA', {views:[{id:'p1',fixture:'call',compact:true},{id:'p2',fixture:'clip',compact:true}]})`
   (allow pop-ups). Both views draw in the new window; resize that window and check that they follow.
5. Run `await __s5.loseAll()`, then `__s5.probeAll()`: every entry must show `blank: 0`. Then run
   `__s5.setColormapAll('grey')`: every view must turn grey.
6. Run `__s5.views.call.setRange(1203, 10)`: Hebrew words read right to left inside their boxes; `050-1234567` stays
   left to right.
7. Run `__s5.setTheme('dark')`, then toggle the OS setting "reduce motion" and press `+` (the zoom must jump).
8. Run `await __s5.openPanel('wsp', {views:[{id:'wsdrv',fixture:'call',spectrogram:false,features:false}],ws:true})`
   and then `await __s5.openPopout('wsp', {views:[{id:'wsdrv',fixture:'call',spectrogram:false,features:false}],ws:true})`.
   Drag the shaded region in the popout: in Chromium it does not move.
9. Open `/?mode=per-track` and repeat step 4 twice, then run `__s5.renderers().global` and note
   `contextsLostUnforced` (the Firefox and Safari context limits differ from Chrome's 16).

### Proposed spec change (`@/shell/audio`, R51 and R52)

The proposal for the decision log:
- **The waveform, regions, timeline and minimap are Cadence code, not wavesurfer.js.**
  - Reasons: wavesurfer's region drag is dead in popouts, following an external axis drops half the frames while
    zooming, and it is 3× the size of the whole Cadence view.
  - Peaks, max-pooled in memory, are enough for the waveform tracks. Use PCM below about 10 s spans.
  - The other R51 exclusions stand.
- **`@/shell/audio` exports `AudioView` (React) and `useAudioAxis()`.**
  - The axis owns start, span, playhead and loop, and is controlled or uncontrolled. Diff rows and Compare share one
    axis across views.
  - Tracks are declared as props: `waveform`, `spectrogram` (`{tiles: url} | {pcm: url}`), `modelInput`, `words[]`
    and `streaming`.
  - Media comes as an MSE source of signed segments, appended around the playhead and evicted with `remove()`. Do
    not append the whole file: the SourceBuffer quota is ≈ 8 MB, and the element costs ≈ 32 MB.
- **The internals that held up:**
  - One WebGL2 renderer and one frame scheduler per window, keyed by the view's `ownerDocument.defaultView`. Each
    view draws through it into its own 2D canvas (copy-out), so it keeps its image during a context loss.
  - Views rebuild on Dockview `onDidLocationChange`.
  - R8 tiles live in one `TEXTURE_2D_ARRAY` with LRU slots. A 256×N RGB LUT texture holds the colormaps. Gain,
    range, colormap and the mel/Hz axis are uniforms.
  - Coarser levels are drawn first as the fallback while finer tiles load.
  - Words are DOM: pooled `<bdi dir=auto>`, at most 300 visible, otherwise density blocks.
  - Model input loads only at spans of 60 s or less.
- **New `defaults.yaml` `views.audio` keys:**
  - `tile_slots: 64`
  - `tile_cache_mb: 12`
  - `model_input_max_span_s: 60`
  - `words_max_visible: 300`
  - `browser_stft_max_s: 600`
- **Worker step `spectrogram_tiles@1`** (core, CPU):
  - Output: a directory artifact (`manifest.json` + `c<ch>/l<L>/<i>.u8`), uint8 dB at -120 dB + 0.5 dB × value.
  - 512-frame tiles, bins up to the origin's Nyquist, levels max-pooled by 2.
  - About 93 MB per 30-minute 8 kHz stereo call, so cache by content hash and compute on demand (R52).
  - Consider one file per level read with HTTP Range instead of 1 410 small files.
- **The `peaks` artifact:** 10 ms int8 min/max per channel, 720 KB per channel-hour (correct R51's 450 KB).
- **R52 "WASM FFT" becomes "an FFT in a Web Worker".**
  - The maintained JS `fourier-transform` (MIT) matches PFFFT-WASM at 45 ms per minute of audio.
  - No maintained WASM FFT exists. Revisit only for a full WASM STFT loop.
- **The 150 MB budget should name the process.** The renderer met it (≈ 130 MB); renderer + GPU did not
  (≈ 165 MB on SwiftShader).
