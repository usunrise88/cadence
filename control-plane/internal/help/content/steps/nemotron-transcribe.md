---
title: nemotron_transcribe (step kind)
summary: The Nemotron family's transcribe role — decode a dataset with a checkpoint in true cache-aware streaming at a latency profile, optionally with static phrase boosting, and write hypotheses with words, confidence and partial events.
contexts: [step:nemotron_transcribe, artifact:hypotheses, artifact:boost_list, family:nemo.fastconformer-rnnt.cache-aware]
---

## What this is

`nemotron_transcribe@2` fills the `transcribe` role of the Nemotron 3.5 streaming family (runtime `nemo-speech`, a
card, job kind `eval`). It streams every utterance of a `dataset` (input `data`) through a `checkpoint` (input
`model`) with NeMo's **cache-aware streaming decoder** — chunk by chunk with the encoder caches carried, never an
offline decode relabelled — at the latency `profile`:

| Profile | `att_context_size` | Latency |
| --- | --- | --- |
| `80ms` | [56,0] | 80 ms |
| `160ms` (default, primary cell) | [56,1] | 160 ms |
| `320ms` | [56,3] | 320 ms |
| `560ms` | [56,6] | 560 ms |
| `1120ms` | [56,13] | 1120 ms |

Decoding is greedy RNN-T in fp32 with the language prompt of the dataset's language (or `target_lang`) and the locale
tag stripped. The `hypotheses` artifact (R42) has one JSON line per utterance: `audio` (the BLAKE3 hash of its audio),
`text`, `words` (`word`, `start`, `end` in seconds, `confidence` — NeMo's word confidence, minimum over the word's
tokens, or null), `decoding` (profile, context, decoder, prompt) and its `decodingHash`, `family`, `weightsHash`, and
`partials`: after each chunk that reached the utterance, `audioOffsetMs` (audio consumed including the chunk's right
context), `emitMs` (wall time since the batch's decode started; a batch's streams decode together) and the text so far;
the last is `final`. Word times come from those emissions, so their resolution is one chunk (the profile's latency).

One language per step; scoring (normalisation, WER) belongs to the core [`wer_score`](wer-score.md) kind.

### Phrase boosting (version 2)

The optional input `boost` takes a `boost_list` artifact; a pipeline may leave it unwired (the kind publishes
`optionalInputs: [boost]`), and then the decode and its `decodingHash` are exactly those of version 1. A boost list is
either

- JSON `{"terms": ["Tel Aviv", "Haifa"], "weight": 0.5}` — `weight` optional, `format` `cadence.boost_list/1` when
  named, other keys ignored; what the control plane renders from a language pack's boost file; or
- that pack file, `lang/<locale>/boost/<domain>.txt`: UTF-8, one phrase per line, blank lines and `#` comments
  skipped, and a line `# weight: <number>` for the list's weight.

Phrases are NFC, trimmed, inner whitespace collapsed and deduplicated (at most 10 000, each ≤ 100 characters); write
them in the model's output style (cased, spelled as in transcripts). They are fused into greedy RNN-T decoding as
NeMo's GPU phrase boosting tree (the label-looping decoder, `context_score` 1.0, `depth_scaling` 2.0, its state carried
across streaming chunks): score = acoustic + weight × tree score, with the list's weight or else `boost_weight`. The
decoding config gains `boost` (`method: nemo-phrase-boosting`, `list` — the BLAKE3 hash of the boost artifact,
`terms`, `weight`, `contextScore`, `depthScaling`), so the list and its weight are part of the `decodingHash` an eval
record is keyed by.

Measured on the staging card (2026-10-02; FLEURS he_il test, 120 utterances, base model, 160 ms; the 40 words of ≥ 5
letters the plain decode missed, boosted): entity recall 0.11 → 0.29 / 0.36 / 0.50 / 0.52 / 0.54 and WER 0.466 →
0.462 / 0.460 / 0.461 / 0.490 / 0.623 at weight 0.3 / 0.5 / 0.7 / 1 / 2. Above about 0.7 listed words replace others
(94 extra listed-word insertions at weight 1). Six distractor words that occur nowhere were never inserted at 0.5 but
still moved WER from 0.466 to 0.472 — boosting is evaluated, not assumed (R24): compare entity recall and WER with and
without the list. Decode time and memory are unchanged (45 s and 4.9 GB peak for the 120 utterances).

## Place in the loop

Evaluate — the `transcribe-<cell>` step of an eval pipeline (a cell's decoding names its boost list, or none); the
conformance suite runs it at every profile.

## Fields and defaults

| Parameter | Default | Source | Range |
| --- | --- | --- | --- |
| `profile` | `packs.nemo.profile` (160ms) | Key defaults (eval latency [56,1]) | 80ms, 160ms, 320ms, 560ms, 1120ms |
| `batch_size` | `packs.nemo.transcribe_batch_size` (32) | Spike A3 | 1–256 |
| `target_lang` | `packs.nemo.target_lang` (from the data) | Cadence recommendation | a prompt key |
| `cuda_context_reserve_mb` | `packs.nemo.cuda_context_reserve_mb` (1024) | Spike A3 | 0–8192 MiB |
| `boost_weight` | `packs.nemo.boost_weight` (0.5) | Measured on the staging card (above) | 0–10 |

## Commands

`evals.new` (decoding axis `{boost: none}` or `{boost: <list>, weight}`); `pipelines.run`.

## Playbooks

None yet (phase 3 evaluation).

## Sources

- NeMo `examples/asr/asr_cache_aware_streaming/speech_to_text_cache_aware_streaming_infer.py` (v3.0.0), as run in spike
  A3 step 3; docs/spec/08-resolutions.md R24 (boosting in evaluation), R42 (hypotheses), R43 (latency profiles).
- NeMo 3.0.0 `nemo/collections/asr/parts/context_biasing/boosting_graph_batched.py` (`BoostingTreeModelConfig`,
  `GPUBoostingTreeModel`) and `parts/submodules/rnnt_decoding.py` (`greedy.boosting_tree`, `boosting_tree_alpha`).
