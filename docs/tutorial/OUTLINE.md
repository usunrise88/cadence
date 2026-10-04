# Outline, term map and status

The table of contents of **Fine-Tuning ASR Models with Cadence: From Start to Finnish**. The method is in
`GUIDELINES.md`.

Keep this file current:

- A chapter's status changes here when it changes in the chapter's front matter.
- A term moves here before it moves in a chapter.

Statuses: `planned` (outline only), `draft`, `reviewed`, `published`.

The running project throughout is **Serbian on Nemotron 3.5 ASR Streaming 0.6B with FLEURS** (`GUIDELINES.md`
§2.1). On the staging stand it is project `test`. Its recorded entities:

| What | Id | Numbers |
| --- | --- | --- |
| Training data | `dataset/fleurs-sr-latn` | 824 train, 112 validation and 700 test utterances; Latin script |
| Run (1 000 steps, prompt `hr-HR`, no replay) | `run_01a0f902…` | Best validation WER 0.216 |
| Best checkpoint | `ckp_01a0f902-029d…` | |
| Golden set | `golden-set/fleurs-sr-latn-test` | 700 utterances, 2.1 h |
| Gate eval | `evl_01a0fd78…` | Target WER 0.346 → 0.256 at 160 ms; 32 of 34 replay sets regressed (forgetting) |

## Part I · Getting started (Cadence phases 0–1)

| # | Chapter | Phase | Status | The reader can… |
| --- | --- | --- | --- | --- |
| 0 | Preface | — | planned | know who the book is for, what they need, the conventions, how to get a stand |
| 1 | What a speech recogniser learns | — | planned | explain ASR, what fine-tuning changes, and why "better" needs a measurement |
| 2 | A first look at Cadence | 0 | planned | move around the shell: panels, documents, workspaces, palette, Library, Help |
| 3 | Your first project | 1 | planned | create a project, read its defaults, find its recipe repository |
| 4 | Working with an agent | 1 | planned | ask the agent, read its plan, approve or deny, review its branch |

## Part II · The loop: data, training, evaluation (phases 2–3)

| # | Chapter | Phase | Status | The reader can… |
| --- | --- | --- | --- | --- |
| 5 | Data you can trust | 2 | planned | import a corpus, read its licence and splits, freeze a dataset version, normalise script |
| 6 | Mixing data and remembering old languages | 2 | planned | build a mix, set replay, preview what training will read |
| 7 | Training a model | 2 | planned | calibrate, read an estimate, start a run within budget, read its metrics and checkpoints, resume |
| 8 | Measuring quality honestly | 3 | planned | freeze a golden set, run an eval matrix, read WER/CER/S/D/I with intervals, open Diff and Audio |
| 9 | The gate says no | 3 | planned | configure a gate, read a failed verdict (forgetting), fix it with replay, evaluate again |
| 10 | Beyond word error rate | 3 | planned | read latency profiles, streaming metrics, robustness and boosting; choose a primary profile |
| 11 | Trying models by hand | 3 | planned | run a live transcription with two targets, blind compare, type a reference |
| 12 | Experiments, registration and lineage | 3 | planned | sweep a parameter under a cap, compare runs, register the best model, trace its lineage |

## Part III · Data at scale (phase 4)

| # | Chapter | Phase | Status | The reader can… |
| --- | --- | --- | --- | --- |
| 13 | Your own recordings: mounts and ingest | 4 | draft | register a mount, ingest recordings in place, pseudo-label untranscribed audio with an ensemble, freeze the draft and train on it |
| 14 | Annotation and the telephone golden set | 4 | draft | sample an annotation batch, annotate with double annotation and adjudication, read inter-annotator WER, work the triage queue, freeze a golden set and align its words |

Both chapters are drafts written against `7ab0067`: the prose and the steps are checked against the code; the
numbers and the screenshots are marked `TBD gate` until the phase-4 gate run on the stand records them.

## Part IV · Production (phase 5) — outline only until phase 5 ships

| # | Chapter | Phase | Status |
| --- | --- | --- | --- |
| 15 | Export, shadow and canary | 5 | planned |
| 16 | The flywheel: learning from production | 5 | planned |

## Appendices

| | Appendix | Status |
| --- | --- | --- |
| A | Glossary (generated from the term map) | planned |
| B | Installing a stand | planned |
| C | Answers to the exercises | planned |
| D | Further reading | planned |

## Term map

Each term is introduced in exactly one chapter (`GUIDELINES.md` §2.2). It may not appear in an earlier chapter.
The chapters in this map are the plan; correct the map when a chapter is written.

| Term | Chapter | Plain definition to start from |
| --- | --- | --- |
| automatic speech recognition (ASR) | 1 | Turning speech audio into text |
| utterance | 1 | One stretch of speech with its own recording, usually a sentence |
| transcript, reference, hypothesis | 1 | The text of an utterance; the correct one; the one a model wrote |
| word error rate (WER) | 1 | Word edits needed to turn the hypothesis into the reference, divided by the reference's words |
| base model | 1 | The pretrained model you start from |
| fine-tuning | 1 | Continuing a pretrained model's training on your own data |
| streaming vs offline recognition | 1 | Writing words while the audio arrives vs after it ends |
| latency | 1 | How long after speech the words appear |
| panel, document, tool panel | 2 | A window in the shell; one that shows an entity; one that helps |
| workspace | 2 | A saved arrangement of panels for a kind of work |
| command palette, command | 2 | The searchable list of every action; one action, named `<entity>.<verb>` |
| entity | 2 | Anything Cadence keeps with an id: a project, a run, an eval |
| registry vs project | 2 | Shared, immutable assets vs the work of one team |
| project | 3 | The unit of work: a goal, a language, a recipe repository, budgets |
| recipe repository, recipe | 3 | The project's Git repository; the files in it that say how work is done |
| default, departure | 3 | The value Cadence uses unless told otherwise; a value that differs from it |
| budget | 3 | GPU-hours and agent spend a project may use per day |
| agent, agent session | 4 | A coding agent working inside Cadence; one conversation with it, on its own branch |
| approval | 4 | A request a person must accept before a gated action happens |
| branch, merge | 4 | The agent's own line of commits; accepting them into the project |
| playbook | 4 | A ready-made plan an agent follows step by step |
| source, licence | 5 | Where recordings come from, and what you may do with them |
| corpus | 5 | A collection of recordings with transcripts |
| split (train / validation / test) | 5 | Parts of a dataset used to learn, to choose, and to judge |
| dataset version, freeze | 5 | An immutable named copy of data; making it so |
| eval-only | 5 | Data you may measure on but not train on |
| text normalisation, transliteration | 5 | Making text comparable; converting between scripts |
| pipeline, step, step kind | 5 | A recipe of steps; one unit of work; its versioned type |
| mix, weight | 6 | The datasets a run trains on and how much of each |
| catastrophic forgetting | 6 | A model losing what it knew while learning something new |
| replay | 6 | Mixing in data of what the model already knows, so it keeps it |
| run | 7 | One training stage from a start point, a mix and a recipe |
| batch, step (optimiser), learning rate | 7 | Examples per update; one update; how big each update is |
| calibration | 7 | Measuring how fast training runs on this card, to estimate cost |
| estimate, GPU-hour | 7 | The predicted cost of a job; one hour of one card |
| queue, worker, card | 7 | Waiting jobs; the process that runs them; the GPU |
| loss, validation WER | 7 | What training minimises; the error on the validation split |
| checkpoint | 7 | A saved copy of the model during training |
| training state, resume | 7 | What a stopped run needs to continue; continuing it |
| golden set | 8 | A frozen eval-only set the model never trained on, used to judge it |
| leakage | 8 | Test audio reaching training, which makes a score lie |
| scoring normalizer | 8 | The rules both texts pass through before WER is counted |
| substitution, deletion, insertion (S/D/I) | 8 | The three kinds of word error |
| character error rate (CER) | 8 | WER counted on characters, for languages without spaces |
| baseline | 8 | The model you compare against, usually the base model |
| eval, eval matrix, cell | 8 | A comparison; its grid of golden sets × latency × decoding; one square of it |
| bootstrap, confidence interval, significant | 8 | Resampling to see how much a number could move; the range; a change whose range excludes zero |
| gate, verdict | 9 | The rules a model must pass to be accepted; their answer |
| target vs replay golden set | 9 | Sets of the language you are improving; sets of languages you must not break |
| latency profile, look-ahead, primary cell | 10 | A named streaming setting; how much future audio it waits for; the cell the gate reads |
| partial, final, endpoint | 10 | Words that may still change; words that won't; where the recogniser decides an utterance ended |
| partial stability, latency to final | 10 | How often shown words change; how long after speech ends the final arrives |
| augmentation, robustness | 10 | Changing audio on purpose (noise, telephone codec); how well a model survives it |
| boosting (hot words), language pack | 10 | Nudging the decoder towards listed terms; a locale's text rules and lists |
| transcription session, target | 11 | A live manual test; one model in it |
| experiment, sweep | 12 | Runs that answer one question; runs generated from a parameter grid under a cap |
| model version, register | 12 | A checkpoint published to the registry with its eval; publishing it |
| lineage | 12 | What an entity was made from and what uses it |
| mount | 13 | A named place Cadence reads audio from without copying it (a directory, a share, a bucket) |
| segment | 13 | A stretch of a recording (start, end, channel) that becomes an utterance |
| voice activity detection (VAD) | 13 | Finding where speech is, frame by frame, from its energy |
| draft (of a dataset version) | 13 | A dataset version you can inspect but not train on until it is frozen |
| pseudo-label | 13 | A transcript a model wrote, not a person |
| ensemble agreement | 13 | Keeping only the segments several different models transcribe alike |
| language identification (LID) | 13 | Naming a segment's language from its sound |
| auxiliary model | 13 | A model Cadence uses to prepare data (label, identify, align) but never trains or ships |
| two-channel call | 14 | A call recording with each party on its own channel |
| annotation batch | 14 | A fixed, stratified sample of segments people transcribe, with its guidelines and people |
| annotation guidelines | 14 | The written rules of what a correct transcript is, versioned in the repository |
| double annotation | 14 | Two people transcribing the same item independently |
| adjudication | 14 | A third person deciding where two transcripts disagree |
| inter-annotator WER | 14 | The WER between two people's transcripts of the same items; the batch's quality number |
| triage | 14 | Working through the segments the pseudo-label ensemble could not agree on |
| forced alignment | 14 | Finding when each word of a known text was spoken in the audio |

## Building the book (engineering work, not writing)

| Item | Status |
| --- | --- |
| `docs/tutorial/chapters/` with front matter (§5.1) | planned |
| Help bundle section `tutorial` (`cmd/helpsync`, `internal/help`), contexts from front matter | planned |
| Term-order check: no term before its chapter (a script over chapters + this map, in CI) | planned |
| Tour and `annotated` block parser shared by the HTML build and the shell | planned |
| Annotated-screenshot component (SVG overlay, numbered regions, linked notes, keyboard, zoom) for the HTML edition and the Help panel | prototype: `build/annotated-prototype.html`, published as an Artifact for review (Figure 9-1) |
| Capture script (Playwright): plays tours, saves screenshots and target boxes | figures done (`capture/capture.mjs`, read-only); tours planned |
| Documentation API key `.doc-capture-key` (owner creates: Settings → Credentials, project scope + registry read, expiry) | done (project `test`) |
| Reference project `book-serbian`, `reference/scenario.md`, `reference/numbers.yaml` and the refresh script | planned |
| HTML edition builder: chapters, glossary with hover, search, tours over screenshots; published as an Artifact | planned |
| In-app tour engine ("Show me" in the Help panel): spec in `11-ui-panels.md` + decision-log row first | planned |
| `data-tour` attributes for tour targets that have no `data-command` / `data-panel` | planned, per chapter |

## Open questions

- Language of the first edition: English (repository convention); a Russian edition would translate reviewed
  chapters.
- The reference project `book-serbian` does not exist yet. Until it does, numbers come from project `test`
  (table above), which development will break. Build it by replaying the scenario (GUIDELINES §5.4) once the
  scenario covers chapters 5–9.
