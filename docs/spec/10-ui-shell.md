# Cadence spec — UI shell: Dockview, stack, shell concepts, uniform workflow, windows, snapping, theming, accessibility, performance

_Part of the Cadence specification v0.2 (2026-09-29). Source of truth: the Claude Doc "Cadence — spec v0.2"; these files are its export, split by area. Figures referenced below live in the Doc._

## Status and scope

Cadence gets a desktop-style web shell: docked panels, in-layout floating windows, popouts for extra monitors and named workspaces, built on Dockview with our own snapping. It is agent-native: the built-in agents (Claude Code, opencode) work on the same entities the panels show, their changes arrive live and attributed, and the user steers them from a Chat panel.

In scope: window management, panel model, theming, input and accessibility, layout persistence, plus the panel catalogue, default workspaces, commands and the backend contract the shell relies on. Out of scope: detailed design inside each panel, the backend implementation and the agent host — the latter two are specified in the Cadence system tab. The shell is API-first: every action it offers is a backend call that a built-in agent makes without the UI, through the matching MCP tool.

### Decision log

| Date | Decision | Why |
| --- | --- | --- |
| 2026-09-29 | Chat shows session kind, state and a budget meter; Agent sessions shows merge state; Recipe shows session branches with accept or discard; Triage gains Annotate mode; Training, Eval and Data workspaces gain Experiment, Annotation batch and Language pack | Aligned with the rewritten Agent integration and the audit blocks |
| 2026-09-29 | Three more documents — Language pack, Annotation batch, Experiment — and Settings gains notification rules, credentials and backup status; Triage gets an Annotate mode | Coverage audit blocks specified in the system tab |
| 2026-09-29 | One search index with a qualifier query language shared by the palette, Library, Find in panel and the agent; help as versioned markdown bundled with the app, linked from every panel, field and error, with field docs generated from schemas | Finding and understanding things is part of the workflow, not a separate site |
| 2026-09-29 | Recommended path first: a three-field wizard, playbooks on the Project home, Recommended / Advanced views on every form with description, default, source and safe range from the step schema | A non-expert starts without reading the spec; an expert is never blocked |
| 2026-09-29 | Library browses the Cadence-wide registry with a this-project / all filter and Adopt; a Lineage panel draws the graph around the selection | Datasets, golden sets and models are shared registry assets, not project files |
| 2026-09-29 | Accent scale: Indigo; snapping to docked-group boundaries stays in v2 | Indigo pairs with Slate in the Radix palette; v1 snapping covers container edges and floats |
| 2026-09-29 | Project switcher in the menu bar; workspaces saved per user per project; Storage and Pipeline run panels | Projects, mounts and pipelines added to the system spec |
| 2026-09-29 | Six new panels: Chat, Agent sessions, Approvals, Recipe, Source, Golden set | Cover the agent loop and all five blocks of the Cadence cycle |
| 2026-09-29 | Agents are first-class in the shell: a Chat panel per agent session, live attributed changes, agent drafts with Accept or Revert, inline approvals | The user watches and steers Claude Code or opencode without leaving the work surface |
| 2026-09-29 | The product is Cadence; this tab is its UI shell, the system spec is its own tab | One product for the full Nemotron ASR cycle |
| 2026-09-29 | UI stack: shadcn components on Base UI primitives, Radix Colors with Slate as the neutral scale, Iconoir icons | Matches the standing frontend stack; Dockview is the layout base underneath |
| 2026-09-29 | Snapping for floating windows is our own implementation | The ready-made smart guides are in the commercial Dockview Enterprise package |
| 2026-09-29 | Floating windows stay inside the layout; popout browser windows only for extra monitors | Photoshop-style work surface in one tab; popout is a separate feature |
| 2026-09-29 | Docking engine: Dockview, MIT core only | Docking, floating groups, popouts and JSON layout serialization out of the box |
| 2026-09-29 | Desktop-style "web OS" shell instead of page-based routes | Many concurrent views of runs, evals and data; layouts tuned per task |

The resolutions of 2026-09-29 (R1–R39, `08-resolutions.md`) take precedence over older text in this tab where they conflict — notably tool names (R1), `mixes.edit` (R13), evaluation axes (R24) and shortcut changes (R37).

## Docking engine: Dockview

Dockview (`dockview-react`, MIT core) is the layout base; the shell never positions panels itself except floating windows during a snapped drag (see Snapping).

[Dockview](https://dockview.dev/) is a zero-dependency docking layout manager for IDE-like interfaces with React bindings. The features we rely on:

| Feature | How the shell uses it |
| --- | --- |
| Grid of groups, tabs, split views, drag and drop | The main work surface: documents in the centre, tool panels at the edges |
| Floating groups | In-layout windows over the grid; any number at once |
| Popout windows | Moving a group to a separate browser window for a second monitor; it stays connected and can be moved back |
| `api.toJSON()` / `api.fromJSON()` | Workspace save and restore |
| Theming through CSS variables | Mapped to Radix Slate tokens (see Theming) |
| Events: `onDidLayoutChange`, `onDidLocationChange`, `onWillDragPanel`, `onWillDragGroup` | Persistence, stream subscription on visibility, custom drag gestures |

Constraints we accept, per the [floating groups docs](https://dockview.dev/docs/core/groups/floatingGroups):

- A floating container holds exactly one group (with any number of tabs); groups cannot be docked together inside one floating container.
- Floating groups cannot be maximized.
- `addFloatingGroup` takes only existing panels or groups: open with `addPanel`, then float.

Licensing rule: only the MIT packages (`dockview-core`, `dockview-react`). The `dockview-enterprise` package is proprietary; we do not install it and do not read or copy its code when building equivalents. The Dockview version is pinned in the lockfile, and upgrades go through the layout-schema migration check (see Persistence).

## UI stack

The full preferred stack is feasible as of September 2026: shadcn/ui made [Base UI its default primitive library](https://ui.shadcn.com/docs/changelog/2026-07-base-ui-default) in July 2026, and Radix Colors and Iconoir are independent of it.

| Layer | Choice | Role in the shell |
| --- | --- | --- |
| Layout engine | Dockview (MIT) | Grid, groups, tabs, floating and popout windows, drop overlays |
| Components | shadcn/ui on Base UI | Everything inside panels and in the chrome: menus, command palette, forms, tables, tooltips, popovers |
| Color | Radix Colors, Slate as neutral | One token set for Dockview, shadcn variables and charts (see Theming) |
| Icons | Iconoir (`iconoir-react`) | Panel icons, tab icons, toolbar and menu glyphs |
| App frame | Vite + TanStack Router | Root shell = the OS chrome; routes are deep links, not pages |
| Agent chat | Streamdown (streaming Markdown with Shiki code blocks) | Agent replies render cleanly mid-stream; entity references become links that open panels |
| Diffs and recipe editing | CodeMirror 6 with its merge view | Recipe documents, tool-call cards and agent drafts show the same live diff |
| Charts | uPlot (MIT) for time series and live data; Apache ECharts 6 (Apache-2.0, tree-shaken) for analytics (R53) | Metrics, latency and GPU curves; histograms, forest plots, heatmaps and scatter in Eval report, Dataset version, Experiment and Model; panels reach both only through `@/shell/charts` |
| Audio view | Cadence's own tracks on one time axis: WebGL2 spectrogram with a colour lookup texture and FFT in a Web Worker; waveform, regions, timeline and minimap are Cadence code too (spike S5 rejected wavesurfer.js; R51, R52) | Audio, Diff, Triage, Transcription, Language pack and Recipe previews compose `@/shell/audio`; no maintained WebGL spectrogram library exists, and LGPL/GPL/AGPL audio libraries are excluded |

Boundaries:

- Dockview owns geometry and drag-and-drop. Its tab and header chrome is replaced with our own React components built from shadcn parts, so tabs, close buttons and header actions look like the rest of the app.
- Panels never import each other and never talk to Dockview directly; they get context from the shell (see Shell concepts).
- Base UI uses the `render` prop, not Radix's `asChild`. Coding agents default to the Radix pattern, so the lint rules reject `asChild` and a Base UI skill is added to the Claude Code setup.
- Icons: one `IconoirProvider` at the root sets size 16 and stroke width 1.5 for the compact chrome; panels do not override it.
- Audio and charts are shell primitives like the entity primitives: panels import `@/shell/audio` and `@/shell/charts`, never uPlot, ECharts, wavesurfer or WebGL directly, so the theme bridge, context budget and accessibility rules live in one place.
- Existing conventions carry over inside panels: Page/Section/Stack primitives, single Skeleton and Spinner, notification history, language and theme switches.

### Audio view and charts (`@/shell/audio`, `@/shell/charts`)

Two shell primitives draw everything acoustic and numeric (R51–R53; spike S5, `docs/spikes/S5-audio-view.md`, set the
budgets and decided against wavesurfer.js).

- Lint allowlist: a panel imports only `@/shell/panel`, the entity primitives, `@/shell/charts` and `@/shell/audio`
  (each through its index). uPlot, ECharts and zrender are imported only inside `src/shell/charts`
  (`web/eslint.config.js`); the FFT worker and WebGL live only inside `src/shell/audio`, enforced the same way. No panel draws audio or charts itself, as no document draws its own `EntityHeader`.
- `AudioView` owns one time axis — visible range, zoom, playhead, loop span — and stacks tracks against it (Sonic
  Visualiser layers, Praat tiers); the axis runs left to right in every locale. Phase-3 tracks: waveform overview
  (min/max peaks per channel, clipping marks), acoustic spectrogram, model input and emissions (from the transcribe
  step's `analysis` artifact), hypothesis words (one lane per target, confidence shading, S/D/I against the reference
  by glyph and colour), the streaming timeline (each word from first partial to final), boosted-term hits, and the
  live energy meter; reference words and energy/VAD from steps arrive in phase 4 (the R51 table). As built (phase 4
  tail, 2026-10-04): the reference track is drawn under the hypothesis lane from `words.get?goldenSet=` (the golden
  set's newest `align_reference` alignment; `AudioView` prop `goldenSet`, selection items `&gs=ver_…`, passed by Eval
  report and Diff rows), outlined so it never reads as a hypothesis, with an unaligned reference shown as text and
  its reason, never at guessed times.
- Spectrogram defaults live in `defaults.yaml` `views.audio` (R52): 25 ms Hann window, 10 ms hop, FFT 512, mel axis
  0–8 kHz (4 kHz with a Nyquist line for audio of 8 kHz origin), range 80 dB below the peak, gain 0, magma; presets
  "Praat broadband", "Narrowband", "Model frames" (the default). Model-input mode shows the checkpoint's own features
  (sequential colormap without normalisation, diverging over ±3σ with it) and dims mel filters above 4 kHz for
  upsampled 8 kHz audio. Colormaps: magma, viridis, cividis, inferno, Roseus, grey and inverse grey; Turbo only on
  request; never jet or rainbow.
- Computation: session audio and spans under 10 minutes in the browser (served 16 kHz PCM → FFT in a Web Worker,
  a JavaScript FFT (`fourier-transform`, MIT; no maintained WASM FFT exists) → uint8 dB into WebGL2 R8 textures with a 256×1 lookup texture, so gain, range and colormap are shader
  parameters; Canvas 2D fallback); long audio from a server tile pyramid cached in the content store (as built: built
  once by the control-plane job `media.spectrogram` on the first view, which polls `spectrogram.get` while it answers
  `202`; narrowband pyramids stop at 4 kHz; long audio's waveform reads a stored overview level plus 10 ms detail for
  the window around the visible range, 06 "Media"); the live
  microphone as a waterfall from AudioWorklet frames, never an AnalyserNode. One WebGL2 renderer per window (Chrome's
  16-context limit); views survive context loss and hidden panels release their textures.
- Playback goes through an HTMLMediaElement, with Media Source Extensions for signed segments (R25, 06 "Media").
- As decided by spike S5 (2026-10-02; proposals in its "Proposed spec change"; built by plan stream A):
  - `@/shell/audio` exports `AudioView` and `useAudioAxis()`; the axis (start, span, playhead, loop) is controlled or
    uncontrolled, so Diff rows and Compare share one axis. Tracks are props: `waveform`, `spectrogram`
    (`{tiles: url} | {pcm: url}`), `modelInput`, `words[]`, `streaming`.
  - Waveform, regions, timeline and minimap are Cadence code drawn from max-pooled peaks (PCM below ≈ 10 s spans).
    wavesurfer.js was rejected: its region drag is dead in popouts, following an external axis cost 62 ms p95 and
    dropped half the frames while zooming, and it adds ≈ 38 KB, three times the whole view.
  - One WebGL2 renderer and one frame loop per window (keyed by the view's `ownerDocument.defaultView`); each view
    copies out into its own 2D canvas, so it keeps its image through a context loss; views are rebuilt when Dockview
    moves a panel to another window. R8 tiles live in one texture array with LRU slots; coarser levels draw first.
  - Media arrives as an MSE source of signed segments appended around the playhead and evicted behind it (a
    SourceBuffer holds ≈ 8 MB; never append the whole file).
  - Words are pooled DOM (`<bdi dir=auto>`), at most 300 visible, else density blocks; model input loads only for
    spans ≤ 60 s.
  - New `defaults.yaml` `views.audio` keys (added with stream A): `tile_slots` 64, `tile_cache_mb` 12,
    `model_input_max_span_s` 60, `words_max_visible` 300, `browser_stft_max_s` 600.
  - The 30-minute-call memory budget (150 MB) is for the renderer process (S5: ≈ 130 MB); renderer plus GPU process
    measured ≈ 165 MB under SwiftShader.
- Spans are selections in the W3C Media Fragments temporal syntax (`utt:123#t=1.20,2.35`): chat references, deep
  links, the Inspector's span statistics and Ask agent all take them.
- Words are DOM, not canvas: each word a bidi-isolated run; the flowing transcript beside the view follows the
  locale's direction, and hovering a word highlights it in both places.
- Keys are `view.audio.*` commands, active only while a view has focus (Keyboard map below). Exports: Praat TextGrid,
  NIST CTM and WebVTT; TextGrid and CTM also import as a reference track.
- As built (2026-10-02, stream A): `AudioView` composes the imperative `AudioEngine` (ruler, waveform per channel
  with clipping ticks, spectrogram per channel, hypothesis word lane with confidence shading, ≠/+ glyphs and deletion
  markers, minimap, playhead, loop and drag-to-select) on an `AudioAxis` several views may share; the engine is built
  in the container's own document and rebuilt when the panel changes window, and each window has one frame loop and
  one WebGL2 renderer (`TEXTURE_2D_ARRAY` of R8 tile slots with LRU, a lookup texture of the colormaps, copy-out into
  each view's 2D canvas so a lost context keeps the last picture). Browser STFT (≤ `browser_stft_max_s`) is a
  TypeScript radix-2 FFT in a Web Worker, not WASM: S5 measured the FFT at ≈ 45 ms per minute, so WASM buys nothing
  yet. Colormaps: magma, viridis, cividis, inferno, grey, inverse grey (Roseus and Turbo not shipped). Not built yet:
  model input and emissions (no `analysis` artifact), the streaming timeline lane (partials are returned by
  `words.get`), presets, Canvas 2D fallback (a note says WebGL2 is missing), MSE playback, Inspector span statistics,
  TextGrid/CTM import. Keys are `view.audio<Action>` ids (client-only ids take one dot). A chat reference with a
  temporal fragment (`@utt:<id>#t=…`) opens the Audio panel. Lint rejects `<audio>`, `<video>`, `<canvas>` and
  `new Audio/AudioContext` in panels.
- `@/shell/charts` offers a time-series chart (uPlot: synced cursors, EMA smoothing over a faint raw line, min/max
  envelopes) and an analytics chart (ECharts 6: histograms, bars, forest plots with intervals, heatmaps, scatter,
  Pareto fronts). Chart data is contract data: bins, intervals and aggregates arrive from the same `get` operation an
  agent reads, and the browser only zooms, smooths and switches scales. Every chart has a table view with CSV copy, a
  keyboard cursor and a text summary; live charts redraw at most 4 times per second. The per-panel chart list is in
  11 "Panel catalogue" and R53.

## Shell concepts

The shell is seven pieces around Dockview; panels are plug-ins that know only the shell, never each other.

| Concept | What it is | Persisted |
| --- | --- | --- |
| Panel registry | Every panel registers a manifest; nothing is rendered that is not registered | No (code) |
| Documents and tools | Documents (a run, dataset, eval report, triage item) open as tabs in the centre; tool panels (Metrics, Checkpoints, Queue, GPU, Diff, Audio) sit at the edges and follow the active document | Open documents: yes, in the workspace |
| Selection bus | One store holds the active document and the selection inside it (utterance, checkpoint, matrix cell). Tool panels subscribe; each can be pinned to a document to compare two runs side by side | Pins: yes |
| Workspaces | Named layouts per task: Training, Eval, Data, Triage, Ops, plus user-defined; "Reset to default" per workspace | Yes (see Persistence) |
| Commands | One registry of commands; menus, buttons, shortcuts and the Ctrl/Cmd+K palette all call it. Each mutating command is one backend call, exposed to agents as the matching MCP tool | Recent commands only |
| Chrome | Thin top menu bar with the project switcher, status bar (GPU memory, queue, agent connection), notification history | No |
| Agent bridge | Chat panels per agent session (interactive, playbook, scheduled, read-only); the selection attaches to prompts as references; agent changes arrive as attributed events or drafts; file edits as session branches to merge; approvals appear inline, in Approvals and on Telegram; budgets shown in the Chat header | Sessions and transcripts: yes, on the server |

Panel manifest:

```ts
type PanelManifest = {
  id: string;                  // stable; renames go through the alias map
  kind: "document" | "tool";
  title: string;
  icon: IconoirIcon;
  singleton: boolean;          // tools usually true; Chat is one per agent session
  defaultSize: { w: number; h: number };
  defaultLocation: "centre" | "left" | "right" | "bottom" | "floating";
  renderer?: "onlyWhenVisible" | "always";
  commands?: CommandId[];      // contributed to the palette
  agentContext?: (sel: Selection) => Reference[]; // what "Ask agent" attaches
  acceptsDrafts?: EntityKind[];   // shows agent drafts with Accept / Revert
  empty: React.ComponentType;  // shown when there is no active document
};
```

Enforced by ESLint and TypeScript: a panel must register through a manifest, may not import another panel, may not call Dockview, and fetches only through the shared query layer.

## Uniform workflow

Every entity in Cadence is worked the same way: the same six-step loop, the same document anatomy, the same verbs with the same keys and icons, and the same list, compare and draft behaviour — all generated from one entity manifest, so consistency is enforced by construction rather than by review.

This follows the interaction principles of ISO 9241-110:2020 (in particular conformity with user expectations and self-descriptiveness) and Nielsen's heuristic "consistency and standards" (1994).

### One loop for every block

Each block is the same loop; only the nouns change. The stepper in every document header shows where the entity is in it. An agent session runs the same loop: prepare is the prompt and plan, check is the dry runs, run is the turns, review is the diffs and events, decide is accepting drafts and merging session changes, record is the note it leaves.

| Step | Data | Training | Evaluation | Deployment | Flywheel |
| --- | --- | --- | --- | --- | --- |
| Prepare (draft) | Source, filters, split | Mix, recipe | Golden sets, `gates.yaml` | Export config | Sampling policy |
| Check (no spend) | Preview hours, leakage check | Calibrate, dry run | Eval dry run: cells to compute, cached cells, estimate | Parity, benchmark | Signal preview |
| Run (job) | Ingest, pseudo-label | Train | Eval matrix | Shadow replay | Capture, judge |
| Review (results) | Dataset version stats, Diff | Metrics, checkpoints | Report, Diff, Audio | Divergence, latency | Triage queue |
| Decide (gate) | Freeze | New stage or register | Gate verdict, baseline | Promote (approval) | Accept or reject |
| Record | Version + lineage | Note, lineage | Eval record | Promotion record | Correction batch |

### Document anatomy

> **Figure:** document anatomy · header, stepper, facts, tabs, body, next-step bar — see the drawing in the Claude Doc "Cadence — spec v0.2".

The header carries title, version, status chip, actor badge and the AI icon in its corner (a menu: Ask agent about this entity, Ask agent about the next step, Explain this — kept apart from the entity's verbs); under it one wrapping row of verbs, the single primary action first; the stepper shows the loop; the facts strip holds the four numbers that matter for this kind; the tabs are always Overview · Details · Lineage · Activity · Notes; the next-step bar names the next loop step and offers it as a button. Tool panels share the header and a filter bar instead of the stepper.

### Verb vocabulary

One word per action, everywhere: in menus, the palette, API operation ids and MCP tool names.

| Verb | Applies to | Reversible | Confirmation |
| --- | --- | --- | --- |
| get, list, search, compare, lineage | Every kind (read verbs; `search` includes `registry.search`, `utterances.search`) | — | None |
| new, open, edit | Every mutable kind; `edit` covers gates and agent profiles | edit yes | None |
| preview, calibrate | Anything that spends: mixes, datasets, runs | — | None |
| run, pause, resume, cancel, retry, wait | Jobs, pipelines, runs, evals; `wait` is agent-only and a read (a `GET` action that returns when the job ends or its timeout passes) | cancel is not | cancel: inline confirm |
| stage | Runs: a new stage from a checkpoint | — | None |
| freeze | Dataset versions, golden sets, annotation batches | No | Inline confirm; golden sets and batches need approval |
| materialize, evict, scan, export | Dataset versions, mounts, models | evict yes | None |
| register, parity, benchmark | Model versions | No | register: inline confirm |
| average | Checkpoints | — | None |
| gate, set | Evals (gate verdict); baselines and aliases (`set`) | set yes | baseline: approval |
| adopt, pin | Registry versions | Yes | None |
| promote, rollback | Deployments | rollback yes | Modal for production |
| accept, correct, reject | Triage items; `accept` also adjudicates a batch item (`batchItems.accept`) | No | None |
| accept, revert | Agent drafts | Yes | None |
| approve, deny | Approvals | No | None (the card is the confirm) |
| package | Correction batches | No | None |
| note, sync | Projects (learnings; template and skill sync) | Yes | None |
| archive | Projects, sources, registry versions (`versions.archive`, phase 4: terminal for a version) | Yes | Inline confirm |
| revoke | Credentials, tokens (R1) | No | Inline confirm |
| verify | Agent credentials: a tiny real request through the agent, the result recorded (2026-09-30); mounts: the health check a worker runs (`mounts.verify`, phase 4); promotions: a person pastes the delivery script's receipt and Cadence checks it against the signed record (`promotions.verify`, phase 5) | — | None |
| align | Golden sets: word timings of their reference texts, several sets in one pipeline run (`goldenSets.align`, phase 4 tail) | — | None (GPU spend follows the policy) |

This table is the whole vocabulary: every MCP tool, API operation id and command id is <entity>.<verb> with a verb from it (the system tab lists the tools). Each verb has one Iconoir icon and one default key, defined once in the command registry; a panel that needs a new verb adds it here first.

### Entity manifest

```ts
type EntityManifest = {
  kind: EntityKind;              // "run", "dataset_version", "model_version", ...
  layer: "registry" | "project";
  states: string[];              // the kind's state machine, from one of three templates
  verbs: Verb[];                 // subset of the vocabulary, with enabled(ctx)
  facts: FactSpec[];             // the four header facts
  comparable: boolean;           // Compare view available
  draftable: boolean;            // agent drafts with Accept / Revert
  nextStep: (e) => Suggestion;   // fills the next-step bar
  icon: IconoirIcon;
};
```

Three state templates cover every kind: registry assets `draft → frozen → deprecated`; work items `planned → queued → running → paused → done | failed | cancelled`; promotions `proposed → approved | denied → applied → rolled back`. The header, action bar, status chip and next-step bar are rendered from the manifest by shared primitives (`EntityHeader`, `ActionBar`, `StatusChip`, `NextStep`); ESLint rejects a document panel that draws its own.

### Lists, compare, drafts, empty states

- Every list (Library, queues, triage) has the same columns first — name, version, status, actor, updated, tags — the same filters, saved views, and keys: arrows move, Enter opens, Space previews in Inspector.
- Any two entities of a comparable kind open the same Compare view: side by side, differences highlighted, facts strip diffed; the selection bus pins the left side.
- Agent drafts look the same on a mix, a recipe, a gate or a note: dashed accent outline, diff on hover, Accept and Revert in the same place.
- Every empty state names the loop step to take next and offers Ask agent; no panel is ever blank.
- The Project document is the home: the five blocks as a checklist with counts, the last decisions, open approvals, and notes.

### Glossary

| Term | Meaning | Not to be confused with |
| --- | --- | --- |
| Version | An immutable registry snapshot, `YYYY-MM-DD.<sha>` | Revision |
| Revision | The edit counter of a mutable entity, used for `If-Match` | Version |
| Freeze | Turn a draft into a version. A dataset version's freeze (`datasets.freeze`) checks leakage and the project's quota, then cuts its segments from the mount into the content store; a golden set's (`goldenSets.freeze`) and an annotation batch's (`batches.freeze`) need the admin's approval | Register, Promote |
| Register | Publish a checkpoint as a model version | Promote |
| Promote | Move a model version to a deployment stage | Register |
| Adopt | Reference a registry version from a project | Import |
| Alias | A project pointer to a version | Tag |
| Draft | An unaccepted change, usually by an agent | Revision; Draft dataset version |
| Draft dataset version | A dataset version an ingest ended in (`frozen: false`): segments indexed in place on a mount, previewable, leakage-checked, never trained on until frozen (phase 4) | Draft (an agent's change) |
| Pin | Keep a version materialised or a panel on an entity | Alias |
| Note | A dated learning attached to an entity and committed to the project | Comment |
| Registry | The Cadence-wide store of immutable, versioned assets that projects adopt | Library (the panel that browses it) |
| Golden set | A frozen held-out test set a project adopts for its gates: an eval-only dataset version tied to one scoring normalizer version; never trainable | Validation split |
| Scoring normalizer | A registry version of the text normalisation WER is computed after; golden sets and eval records pin it (R21) | Text style (the language pack's rules for training targets) |
| Eval | One evaluation in a project: models × golden sets × latency profiles × decoding configs, assembled from eval records, with a gate verdict | Eval record (the cached cell); Transcription |
| Eval record | The cached result of one cell — model weights × golden set version × normalizer version × decoding × scorer — shared by every project (R22) | Eval (the project's matrix) |
| Baseline | The model version every eval cell is compared with: the project's `baseline` alias, the default base model until set (R23) | Production (the deployed model) |
| Primary cell | The latency profile the gate reads (`160ms` by default, R20); other profiles are reported | Latency profile |
| Span | A time range of an utterance as a selection, `utt:123#t=1.20,2.35` (W3C Media Fragments) | Segment (an utterance itself) |
| Gate | A project's pass rule over an eval, kept in its `gates.yaml`: target and replay golden sets, allowed regression, the deletions/insertions check, significance | Approval |
| Playbook | A pipeline chain with defaults, a prefilled agent prompt and an estimate, run from the Project home | Pipeline |
| Session | One agent conversation with its own worktree, branch, token and budget; a first-class entity | Chat (the panel that shows it) |
| Session changes | Commits on a session branch not yet merged to main; accepted or discarded as a whole | Draft (an entity change awaiting Accept) |
| Transcription | A manual test: models run on a file, the microphone or an utterance span, words shown live, nothing stored (R47) | Transcript (text attached to an utterance as data); Eval (stored and comparable) |
| Latency profile | A named streaming setting of a model family with its latency in milliseconds: `160ms` is `[56,1]` for Nemotron (R43) | Latency to final (a measured metric) |
| Model family | The framework and architecture a model belongs to, with its capabilities, latency profiles and step kinds (R41) | Base model (one upstream checkpoint of a family) |
| Runtime | The pinned worker image a step kind runs in (R40) | Compute (the host and its cards) |
| Framework pack | The unit of extension for a training framework: runtime image, step kinds, family descriptor, templates, defaults section, help and skill; proven by the conformance suite (R45) | Runtime (the image alone); Language pack |
| Artifact | A step input or output in the content store, addressed by its BLAKE3 hash (`b3:…`), with a neutral type (R15, R42) | Version (a named registry entry that may reference artifacts) |
| Lease | A worker's claim on one step job: spec, inputs, card and memory cap, kept alive by heartbeats and reaped after three missed beats (R14) | Job (the unit of work the lease runs); Approval |
| Card slot | The control plane's reservation of memory on one card of a compute host; one training job per card, other kinds share what is left under the cap (R40) | Lease (the worker's claim that holds a slot) |
| Availability window | Weekly hours in which a card takes jobs of one kind; training is stopped at a close and resumes from its training state (R19) | Budget (GPU-hours a project may spend) |
| Step kind | A versioned worker plugin a pipeline pins as `kind@version`: parameter schema, artifact types consumed and produced, resources (R40) | Job (one execution of a step) |
| Replay | Training samples from the base model's other locales, mixed in to stop forgetting (R17) | Shadow (replayed production calls) |
| Track | One lane of the audio view on the shared time axis: waveform, spectrogram, words, timeline (R51) | Channel (one side of a stereo recording) |
| Mount | A named storage location Cadence reads audio from or writes exports to — a local path, NFS or SMB share, S3-compatible bucket or Hub repository; URIs `mount://<name>/<path>` (phase 4) | Content store (the cache, addressed by hash) |
| Pseudo-label | A transcript the machine wrote for untranscribed audio, kept when ensemble members agree (origin `pseudo-label`); a disagreement becomes `pseudo-label:disputed` and goes to triage, never to training (R26) | Transcription; human transcript |
| Triage | Resolving disputed pseudo-labels one by one — accept the best candidate, correct it, or reject the segment; the Triage panel's queue (phase 4; production samples in phase 5) | Annotation (transcribing a sampled batch) |
| Annotation batch | A fixed, stratified sample of segments people transcribe by hand under pinned guidelines, double-annotated in part and adjudicated; frozen into a golden set or a training dataset version (04 "Annotation workflow") | Triage item; Correction batch |
| Reviewer invitation | A link the admin gives a person outside Cadence: it signs them in as a reviewer of one batch only — its items and their audio, played without download — until the due date or the freeze | API key; Agent session token |
| Auxiliary model | A registry model a step uses beside the trained one: a LID classifier, pseudo-label member or aligner; adopted with the admin's approval after a licence check (R26) | Base model (what is fine-tuned) |
| Alignment | Word timings of a golden set's reference text against its audio, from a CTC aligner (`align_reference@1`); the reference track of the audio view and the basis of emission delay (R51, R54) | Diff alignment (reference vs hypothesis words) |
| Emission delay | How long after a reference word ends (by the alignment) a streaming model first shows it in a partial that stays, PR50/PR90 in milliseconds (`latency_score@3`); `n/a` with a reason when the reference is unaligned (R54) | Latency to final (time to a final result) |
| Deployment target | Where models are served: `staging` (the compose Triton Cadence reaches) or `delivery` (a production server only a person's delivery script reaches), with the families, formats and latency profiles it serves (R46, phase 5) | Compute (the host Cadence trains on) |
| Export | A model version turned into a `deployable` for one latency profile, with its parity and benchmark reports (`models.export`, phase 5) | Dataset export (`datasets.export`) |
| Parity | The check that an export decodes the same words as the model it came from: WER difference and identical token sequences on a fixed sample (R31) | Eval (a model against references) |
| Promotion record | An append-only, hash-chained record signed with the instance's key: which model version's files went to which stage of a delivery target, approved by whom; confirmed by the receipt the delivery script prints (R33, phase 5) | Approval (the decision it names) |
| Delivery script | The `deliver.sh` of a promotion's bundle, run by a person on the production host: it checks the signature and files, loads the model, runs a smoke check and prints the receipt | Pipeline |
| Shadow | A deployment on the staging target that replays the night's calls beside the production model and records how far they diverge; no effect on calls | Replay (training samples from other locales); Canary |

## Window states

A group is always in one of three places — the grid, a floating overlay or a popout window — and only a docked group can be maximized.

> **Figure:** window states · 4 states, 7 transitions — see the drawing in the Claude Doc "Cadence — spec v0.2".

Docked is the home state, floating is the only state where snapping applies, and a popout returns to the grid when its window closes.

| Transition | Pointer | Command |
| --- | --- | --- |
| Float | Tab context menu → Float | Float panel |
| Dock | Drag a tab onto a grid drop target | Dock left / right / top / bottom |
| Pop out | Header button, or tab context menu → Pop out | Pop out group |
| Return | Header button inside the popout, or closing its window | Return to grid |
| Maximize / Restore | Double-click the tab bar | Toggle maximize |

- Closing a popout window returns its group to its previous grid position.
- Maximize on a floating group is disabled with a tooltip giving the reason, since Dockview does not support it.
- Dockview's Shift-drag float gesture stays on as a shortcut; the menu and commands are the documented path.

## Snapping for floating windows

Floating windows snap to the container edges and to each other while being dragged or resized. It is our own code: one pure function plus a thin Dockview adapter.

### Behaviour

- Scope: floating groups only. Docked groups are laid out by the grid; popout windows by the operating system.
- Targets: container edges; a gutter line 8 px inside each edge; for every other visible floating window its edges (flush adjacency), same-side edges (alignment) and centres. Docked-group boundaries as targets are v2, behind a flag.
- Probes: left, centre and right of the dragged window on x; top, centre and bottom on y. During resize only the moving edge is probed.
- The two axes resolve independently: a window can snap to one neighbour horizontally and another vertically.
- Bypass: holding Ctrl/Cmd disables snapping for the current drag. A persisted View → Snap toggle turns it off globally.
- No magnetic groups: snapped neighbours do not move together. Snapping is a positioning aid, not a constraint.
- The title bar stays reachable: the window's top edge is clamped to 0 or below.

### Parameters

| Parameter | Default | Note |
| --- | --- | --- |
| Snap distance | 8 px | Probe-to-line distance that captures; tuned in the spike |
| Release distance | 12 px | Hysteresis: a captured line holds until the raw position moves this far from it |
| Gutter | 8 px | Inset line from each container edge |
| Target priority | container edge > window edge > centre | Tie-break when two lines are equally close |
| Guide line | 1 px, accent step 9 | Drawn only for active snaps, at most one per axis |
| Bypass key | Ctrl/Cmd | Configurable; Alt is avoided because Linux window managers grab Alt+drag |

### Algorithm

1. Drag start: snapshot target lines per axis in container coordinates. The layout does not change during a drag, so lines are computed once.
2. Each pointer move, coalesced to one per animation frame: raw rect = start rect + pointer delta.
3. Per axis: if already snapped to a line and still within the release distance, keep it. Otherwise take the closest probe-to-line pair within the snap distance, ties by priority.
4. Apply the offsets, then clamp: top edge at 0 or below, minimum visible area per Dockview's `floatingGroupBounds`.
5. Write the bounds through the adapter and redraw the guides.
6. Drag end: guides disappear at once (animations are off by default); Dockview's layout-change event triggers workspace persistence.

The core is a pure, unit-tested function; the adapter only feeds it and applies its result:

```ts
computeSnap(raw: Rect, lines: SnapLines, prev: SnapState, opts: SnapOptions)
  : { rect: Rect; guides: Guide[]; state: SnapState }
```

### Dockview integration

- One module, `shell/floating-snap/dockview-adapter.ts`, is the only code allowed to touch Dockview internals; lint enforces it.
- Move: `floatingGroupDragHandle: "tabbar"` for a compact header. The adapter captures pointer-down on the empty header space and runs the drag itself; tabs keep Dockview's drag-and-drop for docking back into the grid. Fallback if capture proves fragile: the `"titlebar"` handle.
- Resize: the same capture on the floating overlay's resize handles.
- Writing bounds: moves go through the public `transformFloatingGroupDrag` option (Dockview 8.x), which hands each pointer frame's proposed box to `computeSnap` (S1). Resize snapping, keyboard moves and align commands use the internal overlay bounds setter, which clamps to the container ([issue #318](https://github.com/mathuo/dockview/issues/318)). A contract test runs on every Dockview upgrade: set bounds, read them back, check `toJSON()`. Floating overlays are `border-box`, or every restore grows them by their border (S4).
- Stacking: floating overlays live in a shell-level host with z-index `calc(999 + i*2)` ([PR #1203](https://github.com/mathuo/dockview/pull/1203)). The guides layer is a sibling above them with `pointer-events: none`.
- Nothing extra is stored: final bounds reach the workspace through Dockview's own serialization.

### Keyboard and non-drag alternatives

With a floating window focused: arrows move it 1 px, Shift+arrows 10 px, Alt+arrows jump to the next snap line on that axis. Commands cover align-to-edge and move-to-neighbour. This satisfies the single-pointer alternative required for dragging (see Accessibility).

### Basis

Snapping to geometric targets during direct manipulation follows Bier and Stone, "Snap-dragging", SIGGRAPH 1986 (Computer Graphics 20(4), 233–240). Its benefit follows from Fitts' law: capture enlarges the effective target width, lowering pointing difficulty (P. M. Fitts, Journal of Experimental Psychology 47(6), 381–391, 1954). If we measure it, we use the Fitts-based throughput method standardized in ISO/TS 9241-411:2012.

### Tests

- [ ] `computeSnap` table tests: each target type, hysteresis, tie priority, bypass, clamping
- [ ] Playwright: drag a float near an edge → bounds land on the edge; same drag with Ctrl held → raw position
- [ ] Adapter contract test against the pinned Dockview version

## Theming

One token source — Radix Colors, Slate as the neutral scale and Indigo as the accent — feeds Dockview's `--dv-*` variables, shadcn's CSS variables and chart colors. No hex values anywhere else in the code.

Step roles follow Radix's [scale guide](https://radix-ui.com/colors/docs/palette-composition/understanding-the-scale): 1–2 backgrounds, 3–5 component states, 6–8 borders and focus, 9–10 solid fills, 11–12 text.

| Shell element | Token | Note |
| --- | --- | --- |
| Empty desktop, document panels | slate-1 | Centre work area |
| Tool panels, tab bars, menus, popovers | slate-2 | Slightly set back from documents, Photoshop-style |
| Chat tab icon | Outline in the text colour, filled by the session's status: blue working, grass waiting for you, amber waits for a decision, slate-a paused, red failed, empty when over | The outline carries the shape; the fill is a hint beside the label's status for screen readers |
| Chat | slate-3 (dark: slate-2) | Set apart from the documents around it; messages, cards and the composer stay on slate-1. Running and done text and diff counts take step 12 on light slate-3 (step 11 is under 4.5:1 there) |
| Inactive tab hover, list row hover | slate-3 / slate-4 |  |
| Selected row, pressed control | slate-5 |  |
| Group separators, panel borders | slate-6 | Decorative, 1 px |
| Sash hover and drag, floating window border | slate-9 (sash) / slate-7 (border) | Floating windows are the only elements with a shadow; slate-8 fails 3:1 (S3) |
| Focus ring | accent-9 (dark: accent-10) | accent-8 is 2.3:1 on slate-1 (S3) |
| Drop-target overlay | accent alpha 4 fill, accent-8 border | Dockview drag-and-drop targets |
| Snap guides | accent-9 | 1 px |
| Secondary text, inactive tab labels | slate-11 |  |
| Primary text, active tab label, icons | slate-12 |  |
| Status: running, done, warning, failed | blue, grass, amber, red (step 9 fills, step 11 text; warning text amber-12) | The only saturated color on screen besides the accent; amber-11 is 4.498:1 (S3) |
| Agent attribution badge | accent alpha 3 fill, accent-11 text | Marks changes an agent made; click jumps to the tool call in Chat |
| Agent draft, not yet accepted | accent-8 dashed outline | Amber stays reserved for warnings |
| Diff added / removed | grass-3 / red-3 background, step-11 text | Recipe documents, tool-call cards, drafts |
| Chart series (categorical) | Eight Radix hues at step 9 (dark: 10), excluding blue, grass, amber, red and the accent, in this order: crimson, violet, bronze, plum, lime, sky, orange, teal. Light mode takes lime-11, sky-11, orange-10 and teal-10: their step 9 is under 3:1 (lime 1.3, sky 1.4, orange 2.8, teal 2.9 on slate-2) | Inside charts only, on slate-1 or slate-2: every series ≥ 3:1 on both in both modes (lowest: orange-10 3.17:1 light on slate-2). Colour-vision-deficiency check (Machado 2009, OKLab ΔE×100): neighbours ≥ 8 under protanopia and deuteranopia, ≥ 6 under tritanopia, ≥ 15 in normal vision; any two ≥ 8. Each slot also has its own dash, symbol or decal (R53) |
| Chart axes and labels, grid, crosshair, checkpoint marks | slate-11 / slate-a4 / slate-9 / slate-11 | Labels 4.5:1 and crosshair and marks 3:1 on slate-1 and slate-2; the grid is decorative |
| Heatmaps, spectrograms | magma by default; viridis, cividis, inferno, Roseus, grey on request | Independent of light and dark; axes, grid and labels use slate-11/12 (R52) |
| Deltas (diverging) | blue ↔ slate ↔ orange (blue-11, 9, 7, 5, slate-4, orange-5, 7, 9, 11) | Never red–green: red and grass stay status colours; the poles are ≥ 15 ΔE apart under every simulation (R53) |

Implementation:

- Import the `@radix-ui/colors` CSS for slate, slate-alpha, the accent and the status scales, light and dark; dark mode swaps scales through the root class.
- One `theme.css` maps shadcn variables (`--background`, `--foreground`, `--muted`, `--border`, `--ring`, …) and a custom Dockview theme to those steps. The exact `--dv-*` list is pinned to the Dockview version.
- Popout windows are separate documents: the adapter injects the stylesheet and the root theme class into each one.
- Density: 12–13 px UI text, 28 px tab height, one-line panel headers.
- Contrast pairs are checked automatically (see Accessibility); the table above is the only allowed list of pairings.

## Accessibility and input

The target is WCAG 2.2 level AA, which is also the international standard [ISO/IEC 40500:2025](https://www.w3.org/WAI/news/2025-10-21/wcag22-iso). A drag-heavy shell is exactly where it bites, so these criteria are acceptance requirements, not guidelines.

| WCAG 2.2 criterion | Requirement in the shell |
| --- | --- |
| 2.5.7 Dragging Movements (AA) | Every drag has a single-pointer, non-drag path: tab context menu (Float, Pop out, Move to…, Dock left/right/top/bottom) and the same commands in the palette |
| 2.1.1 Keyboard (A) | Every window operation works from the keyboard, including sash resize and floating-window moves |
| 2.4.11 Focus Not Obscured, Minimum (AA) | A focused element under a floating window must not be fully hidden: covering floats drop to 20% opacity and ignore the pointer until focus leaves |
| 2.4.7 Focus Visible (AA) | Accent-9 focus ring (dark: accent-10) on tabs, sashes, window chrome and panel controls |
| 2.5.8 Target Size, Minimum (AA) | Hit areas at least 24×24 CSS px, including tab close buttons with 16 px icons |
| 1.4.3 Contrast, Minimum (AA) | Text pairs at least 4.5:1 in light and dark themes |
| 1.4.11 Non-text Contrast (AA) | Focus rings, sash handles and state indicators at least 3:1 against their background |
| 1.1.1 Non-text Content (A) | Every chart has a table view and a text summary; the audio view's words and transcripts are DOM text, not pixels (R51, R53) |
| 1.4.1 Use of Color (A) | Substitutions, deletions, insertions, gate verdicts and chart series differ by glyph, pattern or label as well as colour |
| 4.1.3 Status Messages (AA) | Agent progress, approval requests and job completion reach assistive technology through a polite live region; streamed tokens are not announced, the finished turn is |

Keyboard map (all remappable, all also in the command palette):

| Keys | Action |
| --- | --- |
| Ctrl/Cmd+K | Palette: plain text searches, `>` commands, `?` help |
| F6 / Shift+F6 | Next / previous group, floating windows included |
| Ctrl/Cmd+Alt+] / Ctrl/Cmd+Alt+[ | Next / previous tab in the group |
| Alt+W | Close panel |
| Ctrl/Cmd+\ | Hide or show all tool panels |
| Arrows, Shift+arrows, Alt+arrows | Move focused floating window 1 px, 10 px, to next snap line |
| Esc during a drag | Cancel and restore the start position |
| Ctrl/Cmd+I | Focus Chat and attach the current selection |
| Ctrl/Cmd+. | Stop the agent's current turn |
| Enter / Backspace on an approval card | Approve / deny |
| Space, ←/→, +/−, [ ], `,` `.` (audio view focused) | Play or pause, seek, zoom, loop in / out, previous / next word (`view.audio.*`, R51) |

Notes:

- Photoshop's bare Tab to hide panels is deliberately not copied: it would break keyboard focus navigation.
- Dockview's announcements are routed through its `announcer` option into the shell's polite live region. Its `keyboardNavigation` option needs a `dockview-enterprise` module (S1), so the command registry owns F6, tab cycling, close and float moves ([typedocs](https://dockview.dev/typedocs/interfaces/dockview-core.DockviewOptions.html)).
- `prefers-reduced-motion` is honored; animations are off by default anyway.
- A CI script computes contrast for every pairing in the Theming table, in both modes, and fails the build below the thresholds above.

Browser-reserved shortcuts — Ctrl/Cmd+W, T, N and Ctrl+Tab — are never assigned, because web pages cannot intercept them; Close panel and tab cycling moved accordingly.

## Performance and persistence

A panel costs nothing while hidden, and a workspace is versioned data that survives panel renames and Dockview upgrades.

### Performance

| Rule | Detail |
| --- | --- |
| Streams only while visible | Panels subscribe through the shell on the panel API's visibility event and unsubscribe when hidden, whatever the renderer mode |
| One live connection | One multiplexed server-sent-events stream per app; panels subscribe to topics. Popout content renders from the main window, so it shares the stream |
| Throttled charts | Live metrics redraw at most 4 times per second |
| Virtualized lists | Every list or table that can exceed 200 rows (eval utterances, logs, datasets) |
| Renderer `always` | Only for panels that must keep live DOM when hidden, such as the audio player |
| Audio and heavy charts | FFT and tiling run in Web Workers; one WebGL2 renderer per window (Chrome allows 16 active contexts per page); views survive context loss and hidden panels release textures; audio over 10 minutes uses server peaks and tiles (R52) |
| Live audio | The microphone waterfall renders in the browser at display rate; only 80 ms PCM frames go to the server (R48) |
| Agent bursts | Events from an active agent session are coalesced per animation frame; a panel under agent edit batches its updates |

Budgets: restoring a 20-panel workspace under 300 ms; floating-window drag at 60 fps with 10 floats open.

### Persistence

```ts
type Workspace = {
  schemaVersion: number;
  name: string;               // "Training", "Eval", "Triage", "Ops", or user-defined
  layout: SerializedDockview; // api.toJSON()
  panels: Record<string, { pinnedTo?: DocRef; viewState?: unknown }>;
  updatedAt: string;
};
```

- Saved per user through the backend API 2 s after the last layout change (Dockview's layout-change event, pins), only
  when the serialized workspace differs from the stored one, and at once when the page is hidden or the workspace is
  switched. A layout save is a preference, not a domain command: its committed runs are not in the audit log.
- Migrations: an integer `schemaVersion` and a chain of migration functions. The panel registry keeps an alias map for renamed panel ids.
  As built: schema 2 adds Chat to the right column; schema 3 (phase 4 tail) removes an Audio panel from single-group
  floating windows (default workspaces no longer open it; 11 "Default workspaces").
- An unknown panel id restores as a placeholder panel ("Panel X no longer exists — remove"), never as a failed restore.
- Default workspaces are built by code factories, not stored JSON, so "Reset to default" always matches the current registry.
- Dockview upgrades run `fromJSON(toJSON())` round-trips over fixtures of every stored workspace in CI.
- Two browser tabs editing one workspace: last write wins with a version check, and the losing tab gets an inline notice.
- Deep links: `/``p/:project/``w/:workspace?doc=run:123&sel=ckpt:4` opens the workspace, then opens or focuses the document and restores the selection.
