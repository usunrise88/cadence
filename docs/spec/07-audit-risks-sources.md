# Cadence spec — Coverage audit, risks, open questions, sources

_Part of the Cadence specification v0.2 (2026-09-29). Source of truth: the Claude Doc "Cadence — spec v0.2"; these files are its export, split by area. Figures referenced below live in the Doc._

## Coverage audit

Twelve blocks were missing at the audit of 2026-09-29; eleven are now specified in the sections above, and data governance under immutability is deliberately deferred to a later phase.

| Block | What is missing | Why it matters | Priority, phase |
| --- | --- | --- | --- |
| Authentication and access | How the admin logs in (passkey or password), session lifetime, how reviewer invitations work (link, expiry), how MCP session tokens and CLI keys are issued and revoked | Everything else assumes an identity; the reviewer role needs invitations | Must, phase 1 |
| Security and threat model | Agent sandbox specifics (container, filesystem limited to the worktree, egress allowlist), prompt injection through data an agent reads (transcripts, notes, help), secret handling end to end, TLS and reverse proxy, dependency pinning and SBOM | Agents read production transcripts and run shell; an injected instruction must not become a command | Must, phase 1 |
| Operations of Cadence itself | Install and upgrade, database and data migrations, backup and restore, disaster recovery, failure handling (worker crash, stuck queue, OOM retry with a smaller batch), release versioning against the NeMo container | A single-user tool still has to survive its own upgrades and a bad night | Must, phase 2 |
| Hot words and language packs | Boost lists (names, terms, brands) as a project asset applied at decode and evaluated; per-locale resources as registry assets: normalizer, inverse text normalisation, transliteration (Serbian Cyrillic and Latin), language-ID config, golden-set recipe | Hot words were a first-line requirement and appear only inside one playbook; every new locale needs the same resource set | Must, phase 2 |
| Task metrics and streaming metrics | Entity accuracy for names, numbers, dates and addresses on annotated golden sets; latency to final, partial-hypothesis stability, end-of-utterance quality; RTF and streams per card | A voice agent fails on a wrong phone number, not on average WER | Must, phase 3 |
| Annotation workflow | Building a golden set from own calls: sampling, guidelines, double annotation, adjudication, inter-annotator agreement; the reviewer role today only triages | Without it the telephone golden set cannot be produced | Should, phase 3 |
| Testing and quality strategy | Unit, contract and end-to-end layers; the smoke project as the e2e; fixtures for pipelines on tiny datasets; evals for skills and playbooks against both agents; Playwright for the shell | Spikes and contract tests exist; nothing says what CI runs every day | Should, phase 1 onward |
| Notifications | Channels beyond the in-app history (Telegram, email), routing by event class, quiet hours, digests; approvals while the admin is away | A nightly flywheel needs to reach a person | Should, phase 2 |
| Experiments and sweeps | Grouping runs into an experiment, parameter sweeps over a mix, comparison tables across many runs | Compare covers two entities; tuning needs ten | Should, phase 3 |
| Augmentation policy | Telephony band-limiting, codec simulation, noise and speed perturbation as step kinds with defaults and sources | The 8 kHz gap is measured; the fix is named only in a playbook | Should, phase 2 |
| Data governance under immutability | Deleting a caller's audio from immutable versions (tombstones, partial versions), access audit for who listened to what, data-subject requests | Immutability and deletion rights collide; the collision needs a rule | Could, phase 4 |
| Interoperability | Import of NeMo manifests, Lhotse cuts, Hugging Face datasets; export of datasets and models to the Hub or another project | Public data arrives in three formats; models may need to leave | Could, phase 4 |

Also unspecified but small: compute availability windows (pausing jobs in business hours on a shared card), the `cadence` CLI surface, capacity assumptions for the database and the search index, and decoder extensions such as end-of-utterance or speaker-gender tokens as a later training feature.

## Risks and open questions

The biggest unknowns are how complete the Claude Code ACP adapter is and how rough Nemotron 3.5 fine-tuning still is in NeMo; both get a spike before anything is built on them.

| Risk | Impact | Mitigation |
| --- | --- | --- |
| The Claude ACP adapter lags the native Claude Agent SDK | Missing hooks or permission detail for Claude sessions | Driver interface; native Claude driver for named gaps only |
| Agent runaway: loops, spend, GPU queue flooding | Cost, blocked card | Budgets, turn limits, inactivity timeout, one training slot per card |
| Nemotron 3.5 tooling moves fast (NeMo main vs release, prompt-key bugs) | Broken runs after upgrades | Pin the NeMo Speech container; smoke-test fine-tune and export on each upgrade |
| A TypeScript sidecar breaks the single-binary shape | More to deploy | A separate service in the same compose file, health-checked by the control plane; one version for all services |
| Licence mistakes in training data | Commercial use blocked later | Licence is a required source field; exports check every member's licence |
| Test leakage | Inflated scores, bad promotions | Runs cannot reference golden sets; fingerprint exclusion at freeze |
| Framework seams erode while NeMo is the only framework | A second framework later needs control-plane rewrites and migrations | The CPU toy pack runs the conformance suite on every pull request (R45); spike F1 before a real second pack |
| A second framework's stack rots (icefall changes little now; k2 must match PyTorch exactly) | Broken pack after an upgrade | One runtime image per pack, pinned by digest; conformance suite on every upgrade; sherpa-onnx (active) for serving exports |
| Live sessions share the staging card with training | OOM, slower training, latency numbers that lie | Interactive memory reservation under the card cap, never beside a benchmark (R49); A5 measures |
| Audio views in many panels | Blank views at the WebGL context limit, memory growth, jank | One renderer per window, peaks and tiles for long audio, context-loss handling; S5 sets the budgets |
| People's voices in manual tests | Personal data kept by accident | Nothing is stored: session audio lives in the worker's temporary directory until the socket closes, and a sweep removes leftovers within an hour (R47) |

Spikes:

- [ ] A1: ACP client against `opencode acp` and `claude-agent-acp`: chat, tool-call diffs, permission round trip, cancel, resume
- [ ] A2: Cadence MCP server with ten generated tools; both agents create a mix and launch a dry-run
- [ ] A3: Nemotron 3.5 fine-tune on the staging card under a 24 GB cap, then ONNX export and a parity check
- [ ] A4: Outbox → SSE → cache patching with an agent editing a mix while the Mix panel is open
- [ ] A5: live microphone → WebSocket relay → Nemotron checkpoint in the worker and back, beside a training job (before phase 3's live mode)
- [ ] S5: one audio view with a 30-minute call, spectrogram and word tracks at 60 fps across popouts (before phase 3's Audio panel)
- [ ] F1: k2/icefall through the framework seams without changes outside its pack (deferred with the packs beyond NeMo)

Open questions:

- [x] Add Chat, Agent sessions, Recipes, Sources, Golden sets and Approvals to the UI shell's panel catalogue
- [x] Stress marking and TTS datasets from the July concept: later phase, or dropped? — deferred to a later phase, out of v1
- [x] Default model for opencode sessions? — chosen per project in the wizard, editable in Agent settings
- [ ] Where production samples may be captured, and the retention limit — from call recordings on a mount, 90 days proposed, awaiting confirmation
- [x] Single user, or a team with roles? — single user in v1, roles deferred
- [ ] Projects need a state template the spec does not list: `container` (active → archived). Phase 0 adds it beside registry / work / promotion; confirm or fold projects into one of the three
- [ ] Client-only commands (float, dock, theme, palette) are not API operations; phase 0 names them `view.<name>`, exempt from the verb vocabulary like the `me`/`auth` tags (R1). Confirm the namespace
- [ ] Unknown `/api` path answers 404 `not-found`, wrong method 405 `method-not-allowed` (a tenth error type with its article); the review proposed 400 for both
- [x] Training from scratch or a second framework (k2/icefall) in v1? — Only the seams, in phase 2; training from scratch, packs beyond NeMo and spike F1 are deferred (owner, 2026-09-29; R44, R45)
- [x] Keep uploads and microphone recordings? — No: a transcription is a manual test and stores nothing (owner, 2026-09-29; R47)
<<<<<<< HEAD
- [ ] `project://summary` needs the connection's project, which the spec does not say how to name. Phase 1 reads a
  `Cadence-Project: <slug>` header that the agent host sends next to `Authorization` in ACP `session/new` →
  `mcpServers` (R2); once `cst_` tokens are project-bound, the token's scope decides and the header must agree.
  `project://{p}/summary` reads any project the token may see. Confirm
- [ ] MCP tool names keep the dotted `<entity>.<verb>` on the wire, but both agents rewrite them: Claude Code shows
  `mcp__cadence__projects_new`, opencode `cadence_projects_new` (model APIs allow only `[a-zA-Z0-9_-]` in tool names).
  Spike A2 checks whether agents still map them to the names in help, errors and skills, or whether the vocabulary
  should use `_` in tool names
- [ ] Automation keys (`cdk_`) follow the agent rules of their scope's preset (default deny), like agent sessions; people are allowed everything except the `everyone` rules (phase 1, stream C). Confirm, or treat a person's API key as the person
- [ ] `jobs.wait` is a read verb (a `GET` action, no Idempotency-Key): waiting changes nothing. `api/vocabulary.yaml` now says `class: read` for `wait`; the spec table groups it with run/pause/resume
- [ ] A dry run of a gated command runs as a dry run and answers `Cadence-Policy: approval; rule=<id>` instead of creating an approval, so an agent can see the estimate first (R7 is silent)
- [ ] The phase-1 fixture gates `projects.archive` for agents (`archive-project` rule); the Guardrails table says "delete anything: not allowed" — once real gated commands exist, move `projects.archive` to `no-deletes`
- [ ] Until phase 2 meters GPU use, the budget check uses a fixed 8 GPU-hours per day (`policy.StubBudget`); the value should come from `defaults.yaml` budgets (R11)
- [ ] opencode names MCP tools `<server>_<tool>` with other characters replaced by `_` (`cadence_projects_get`); the rendered `permission` block assumes it — verify in spike A1 with both drivers
- [ ] Registry API shape (phase 1): versions are served per kind (`baseModels`, `datasets`, `templates` under
      `/registry/<kind>`, so `datasets.materialize` and `models.export` fit later) and collections generically
      (`collections.list|get`); a collection name in a path escapes its slash (`dataset%2Ffleurs-he-smoke`). Adoption is
      `POST /projects/{p}:adopt` (`projects.adopt`, If-Match on the project) instead of `POST /projects/{p}/adoptions`.
      Confirm
- [ ] Adoption checks: phase 1 accepts any frozen version; the licence and locale checks of 02 "Registry" wait for
      project locales (wizard) and a licence policy — which licences may a project adopt without a person?
- [ ] An alias may point only at a version the project adopted (enforced by a foreign key); versions and the staging
      card's class (`blackwell-96gb`, from spike A3's "96 GB" and the Blackwell toolchain note) are assumptions until
      the staging host is inventoried
- [ ] Secrets: no rotation or archive yet (`secrets.new` refuses a taken name); the master key defaults to
      `$CADENCE_DATA_DIR/master.key`, generated on first start, until the compose secret of R9 is wired
=======
- [ ] Identity (phase 1) assumptions, confirm: login throttling counts failed attempts only (5/min, 20/h per address and per username, in memory); `X-Forwarded-For`/`-Proto` are trusted from loopback and private peers (the host's Caddy, Docker's gateway); the TOTP secret lives in the `users` row, not the R9 file store (it is a sign-in factor, not a secret handed to jobs); passwords need 12+ characters; first start may rename the admin account; out-of-scope reads answer 403 `forbidden` rather than hiding the entity behind 404; a credential without a project may not open the event stream
>>>>>>> phase1/identity

## Sources

- [Nemotron 3.5 ASR model card](https://huggingface.co/nvidia/nemotron-3.5-asr-streaming-0.6b) — release date, languages, latency settings, licence
- [How to fine-tune Nemotron 3.5 ASR](https://huggingface.co/blog/nvidia/fine-tuning-nemotron-35-asr) — recipe, streaming evaluation, replay
- [NeMo Speech repository](https://github.com/NVIDIA-NeMo/Speech) — NeMo Speech 3.0 and the 26.07 container
- [NeMo ASR fine-tuning skill, PR #15733](https://github.com/NVIDIA-NeMo/NeMo/pull/15733)
- [Community Nemotron 3.5 fine-tune kit](https://github.com/Piyazon/nemotron-3.5-asr-streaming-0.6b-custom-finetune-kit) — learning-rate and tokenizer pitfalls
- [Kenyan-language adaptation of Nemotron 3.5](https://arxiv.org/abs/2607.18912) — leakage finding
- [Agent Client Protocol overview](https://agentclientprotocol.com/protocol/overview)
- [Claude Agent SDK TypeScript reference](https://code.claude.com/docs/en/agent-sdk/typescript)
- [opencode server](https://opencode.ai/docs/server/), [SDK](https://opencode.ai/docs/sdk/) and [skills](https://opencode.ai/docs/skills)
- [Lhotse dataloading in NeMo](https://docs.nvidia.com/nemo/speech/nightly/dataloaders.html)
- [NeMo Curator audio curation](https://docs.nvidia.com/nemo/curator/curate-audio)
- W&B Registry documentation — organisation-level registries, collections, aliases, lineage (pattern for the Cadence registry)
- M. Bisani, H. Ney, "Bootstrap estimates for confidence intervals in ASR performance evaluation", ICASSP 2004 — significance of WER deltas
- T. Gebru et al., "Datasheets for Datasets", CACM 2021; M. Mitchell et al., "Model Cards for Model Reporting", FAT* 2019 — generated cards
- ISO 9241-110:2020 interaction principles; WCAG 2.2 as ISO/IEC 40500:2025; RFC 9457 and RFC 9110 — standards cited in the UI shell tab

Added 2026-09-29 with R40–R54 (extensibility, trying models, audio views and charts):

- Frameworks:
  - [icefall](https://github.com/k2-fsa/icefall) and its [installation order](https://k2-fsa.github.io/icefall/installation/index.html)
  - [k2 CUDA wheels](https://k2-fsa.github.io/k2/cuda.html) and [Lhotse](https://pypi.org/project/lhotse/)
  - [sherpa-onnx releases](https://github.com/k2-fsa/sherpa-onnx/releases) and its [Nemotron 3.5 export](https://github.com/k2-fsa/sherpa-onnx/tree/master/scripts/nemo/nemotron-3.5-asr-streaming-0.6b)
  - Zipformer, [arXiv:2310.11230](https://arxiv.org/abs/2310.11230) — training cost table; CR-CTC, [arXiv:2410.05101](https://arxiv.org/abs/2410.05101); FastConformer, [arXiv:2305.05084](https://arxiv.org/abs/2305.05084)
  - Precedents: [Kubeflow Trainer](https://www.kubeflow.org/docs/components/trainer/overview/), [W&B Launch](https://docs.wandb.ai/platform/launch/launch-terminology), [MLflow models](https://mlflow.org/docs/latest/ml/model/)
  - OpenAI-shaped speech servers: [vLLM speech-to-text](https://docs.vllm.ai/en/latest/serving/online_serving/speech_to_text/)
- Live testing:
  - NVIDIA: [NeMo streaming inference pipelines](https://github.com/NVIDIA-NeMo/Speech/tree/main/nemo/collections/asr/inference); [NVIDIA Speech NIM realtime ASR](https://docs.nvidia.com/nim/speech/latest/reference/api-references/asr/realtime-asr.html); [NeMo telephony tutorial](https://github.com/NVIDIA-NeMo/Speech/blob/main/tutorials/asr/ASR_for_telephony_speech.ipynb)
  - Vendor protocols: [Deepgram live](https://developers.deepgram.com/reference/listen-live); [AssemblyAI streaming](https://www.assemblyai.com/docs/streaming/message-sequence.md); [Speechmatics real-time](https://docs.speechmatics.com/introduction/rt-guide); [Soniox real-time](https://soniox.com/docs/stt/rt/real-time-transcription)
  - Capture: [Google STT audio best practices](https://docs.cloud.google.com/speech-to-text/docs/best-practices-provide-speech-data); [MDN AudioWorklet](https://developer.mozilla.org/en-US/docs/Web/API/AudioWorklet); [WebKit bug 281978](https://bugs.webkit.org/show_bug.cgi?id=281978) (Safari stereo capture)
  - Metrics: [Pipecat STT benchmark](https://github.com/pipecat-ai/stt-benchmark) (time to final segment); Y. Shangguan et al., "Analyzing the Quality and Stability of a Streaming End-to-End On-Device Speech Recognizer", Interspeech 2020, [arXiv:2006.01416](https://arxiv.org/abs/2006.01416); J. Yu et al., "FastEmit", ICASSP 2021, [arXiv:2010.11148](https://arxiv.org/abs/2010.11148); Z. Liu, F. Peng, "Statistical Testing on ASR Performance via Blockwise Bootstrap", [arXiv:1912.09508](https://arxiv.org/abs/1912.09508)
- Audio views and charts:
  - Spectrogram defaults in tools: [Praat editor preferences](https://github.com/praat/praat.github.io/blob/master/foned/SoundAnalysisArea_prefs.h); [Audacity spectrogram settings](https://github.com/audacity/audacity/blob/master/src/spectrogram/internal/globalspectrogramconfiguration.cpp); [librosa display](https://github.com/librosa/librosa/blob/main/librosa/display.py)
  - What models see: [Kaldi fbank options](https://github.com/kaldi-asr/kaldi/blob/master/src/feat/feature-fbank.h); [NeMo FastConformer configs](https://github.com/NVIDIA/NeMo/tree/main/examples/asr/conf/fastconformer); [Whisper audio](https://github.com/openai/whisper/blob/main/whisper/audio.py)
  - Colormaps:
    - [matplotlib colormaps, Smith & van der Walt 2015](https://bids.github.io/colormap/)
    - Nuñez et al., cividis, [PLOS ONE 2018](https://doi.org/10.1371/journal.pone.0199239)
    - Borland & Taylor, "Rainbow Color Map (Still) Considered Harmful", IEEE CG&A 2007
    - Crameri, Shephard & Heron, [Nature Communications 2020](https://doi.org/10.1038/s41467-020-19160-7)
    - [Turbo](https://research.google/blog/turbo-an-improved-rainbow-colormap-for-visualization/)
  - Libraries: [wavesurfer.js 8.0.0](https://github.com/katspaugh/wavesurfer.js/releases/tag/8.0.0); [uPlot](https://github.com/leeoniya/uPlot); [Apache ECharts](https://echarts.apache.org/)
  - ASR views: [NeMo Speech Data Explorer](https://docs.nvidia.com/nemo/speech/latest/tools/speech_data_explorer.html); [entropy-based word confidence](https://developer.nvidia.com/blog/entropy-based-methods-for-word-level-asr-confidence-estimation)
  - Standards: [Web Audio API, AnalyserNode windowing](https://webaudio.github.io/web-audio-api/#fft-windowing-and-smoothing-over-time); [W3C Media Fragments URI 1.0](https://www.w3.org/TR/media-frags/)
