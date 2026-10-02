---
title: nemotron_transcribe (step kind)
summary: The Nemotron family's transcribe role — decode a dataset with a checkpoint through NeMo's cache-aware streaming pipeline (the decoder live sessions use) at a latency profile, optionally with phrase boosting, and write hypotheses with words, confidence and partial events.
contexts: [step:nemotron_transcribe, artifact:hypotheses, artifact:boost_list, family:nemo.fastconformer-rnnt.cache-aware]
---

## What this is

`nemotron_transcribe@3` fills the `transcribe` role of the Nemotron 3.5 streaming family (runtime `nemo-speech`, a
card, job kind `eval`). It streams every utterance of a `dataset` (input `data`) through a `checkpoint` (input
`model`) at the latency `profile` with **NeMo's cache-aware streaming pipeline** (`nemo.collections.asr.inference`) —
the decoder a live session runs ([`nemotron_live`](nemotron-live.md)), so an eval, a paced replay and a live session
give the same words (spike A5):

| Profile | `att_context_size` | Latency |
| --- | --- | --- |
| `80ms` | [56,0] | 80 ms |
| `160ms` (default, primary cell) | [56,1] | 160 ms |
| `320ms` | [56,3] | 320 ms |
| `560ms` | [56,6] | 560 ms |
| `1120ms` | [56,13] | 1120 ms |

Each utterance is one pipeline stream (16 kHz, the training resampler for any other rate, channel 0): its log-mel
features are those of the whole utterance, cut like the reference cache-aware loop's (a short first chunk without
cache, then the pre-encode cache and a chunk), and the stream is closed with a forced end of utterance; the utterances
of a batch (`batch_size`, default 1) step together. Decoding is
greedy RNN-T in fp32 with the language prompt of the dataset's language (or `target_lang`), the locale tag stripped,
and the pipeline's end-of-utterance endpointing (`stop_history_eou_ms`, as live sessions): a long utterance may come
out as several finals, joined into one text (a final that continues a word split by an end of utterance is joined
without a space).

The `hypotheses` artifact (R42) has one JSON line per utterance: `audio` (the BLAKE3 hash of its audio), `text`,
`words` (`word`, `start`, `end` in seconds and `confidence` from the pipeline's word segments: NeMo's entropy-based
confidence, the minimum over the word's tokens), `decoding` and its `decodingHash`, `family`, `weightsHash`, and
`partials`: one event per partial or final the stream emitted, with `audioOffsetMs` (the audio the event covers),
`emitMs` (wall time since the batch's decode started) and the text so far; the last is `final`.

`decoding` names the decoder — `"decoder": "nemo-pipeline-cache-aware"` with `profile`, `attContextSize`,
`targetLang`, `stopHistoryEouMs` — so records of versions 1 and 2 (NeMo's cache-aware loop, `"decoder":
"rnnt-greedy-batch"`) never mix with these; an eval keys its records by the transcribe kind's version too. On the
stand card (FLEURS: ten he fixtures, twelve ru clips, the base model) version 3 gives the same WER as version 2 at
every profile — he 75.6 / 77.9 / 65.1 / 66.3 / 69.8 and ru 16.2 / 17.2 / 17.2 / 16.2 / 14.1 at 80 / 160 / 320 / 560 /
1120 ms, the same two empty he clips — and its words differ only where version 2 drops an utterance's last tokens (a
tail shorter than the subsampling, "כתבית." → "כתבי"). At batch 1 an utterance decodes to the same words as a live
session of the same audio in 20 ms frames (22/22 clips at every profile); a larger batch is about 4× faster and
changes a few words through the order of floating-point sums (batch 8: 3 of 22 clips at 80 ms, none at 160 ms, same
WER).

Five shims fix NeMo 3.0.0 gaps (`cadence_nemo/pipeline.py`): the per-stream language prompt is applied (as shipped the
pipeline builds it and drops it, and Nemotron 3.5 emits only blanks); the trailing locale tag is stripped; the model
is restored on the CPU; features are computed once their whole window has arrived (NeMo's frame path featurizes each
chunk alone, so chunk edges saw zero padding: at 160 ms 5 empty he transcripts instead of 2, WER 80.2 against 77.9);
and a feature buffer holds the encoder's cache plus chunk (NeMo rounds 16.999 down at 80 ms and dropped a frame, WER
91.9 against 75.6).

One language per step; scoring (normalisation, WER) belongs to the core [`wer_score`](wer-score.md) kind.

### Phrase boosting

The optional input `boost` takes a `boost_list` artifact; a pipeline may leave it unwired (the kind publishes
`optionalInputs: [boost]`). A boost list is either

- JSON `{"terms": ["Tel Aviv", "Haifa"], "weight": 0.5}` — `weight` optional, `format` `cadence.boost_list/1` when
  named, other keys ignored; what the control plane renders from a language pack's boost file; or
- that pack file, `lang/<locale>/boost/<domain>.txt`: UTF-8, one phrase per line, blank lines and `#` comments
  skipped, and a line `# weight: <number>` for the list's weight.

Phrases are NFC, trimmed, inner whitespace collapsed and deduplicated (at most 10 000, each ≤ 100 characters); write
them in the model's output style (cased, spelled as in transcripts). Each stream gets the list as NeMo's per-stream
phrase boosting tree (the label-looping greedy decoder's biasing multi-model, `context_score` 1.0, `depth_scaling`
2.0): score = acoustic + weight × tree score, with the list's weight or else `boost_weight`. The decoding config gains
`boost` (`method: nemo-phrase-boosting`, `list` — the BLAKE3 hash of the boost artifact, `terms`, `weight`,
`contextScore`, `depthScaling`), so the list and its weight are part of the `decodingHash`.

Version 2's measurement (the same boosting tree in NeMo's cache-aware loop; staging card, 2026-10-02, FLEURS he_il
test, 120 utterances, base model, 160 ms, the 40 words of ≥ 5 letters the plain decode missed): entity recall 0.11 →
0.29 / 0.36 / 0.50 / 0.52 / 0.54 and WER 0.466 → 0.462 / 0.460 / 0.461 / 0.490 / 0.623 at weight 0.3 / 0.5 / 0.7 / 1 /
2. Boosting is evaluated, not assumed (R24): compare entity recall and WER with and without the list.

## Place in the loop

Evaluate — the `transcribe-<cell>` step of an eval pipeline (evals take the newest published transcribe kind of the
family's role); the conformance suite runs it at every profile.

## Fields and defaults

| Parameter | Default | Source | Range |
| --- | --- | --- | --- |
| `profile` | `packs.nemo.profile` (160ms) | Key defaults (eval latency [56,1]) | 80ms, 160ms, 320ms, 560ms, 1120ms |
| `batch_size` | `packs.nemo.transcribe_batch_size` (1) | Stream T GPU check | 1–256 |
| `target_lang` | `packs.nemo.target_lang` (from the data) | Cadence recommendation | a prompt key |
| `cuda_context_reserve_mb` | `packs.nemo.cuda_context_reserve_mb` (1024) | Spike A3 | 0–8192 MiB |
| `boost_weight` | `packs.nemo.boost_weight` (0.5) | Measured on the staging card (above) | 0–10 |
| `stop_history_eou_ms` | `packs.nemo.live_stop_history_eou_ms` (800) | Spike A5 | 80–10 000 ms |

## Commands

`evals.new` (decoding axis `{boost: none}` or `{boost: <list>, weight}`); `pipelines.run`.

## Playbooks

None yet (phase 3 evaluation).

## Sources

- docs/spikes/A5-live-transcription.md "Result" (one decoder for live and evals; the shims).
- NeMo 3.0.0 `nemo/collections/asr/inference/pipelines/cache_aware_rnnt_pipeline.py` (per-stream prompts, biasing,
  endpointing) and `parts/context_biasing/biasing_multi_model.py` (`BiasingRequestItemConfig`).
- docs/spec/08-resolutions.md R24 (boosting in evaluation), R42 (hypotheses), R43 (latency profiles).
