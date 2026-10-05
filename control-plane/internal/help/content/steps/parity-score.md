---
title: parity_score (step kind)
summary: The neutral parity judge — compares the served decode of the parity sample with the model's own decode (WER difference, identical token sequences, word disagreement) and writes a parity_report with the delivery bundle's smoke set.
contexts: [step:parity_score, artifact:parity_report, artifact:hypotheses, artifact:smoke_inputs, command:models.parity]
---

## What this is

`parity_score@1` is the last step of the pipeline `models.parity` generates (R31). It runs on the CPU in every
runtime and never names a family. Inputs:

| Input | Type | What |
| --- | --- | --- |
| `reference` | `hypotheses` | The family's parity reference: its own decoder at the eval's batch |
| `served` | `hypotheses` | The family's serve role through the staging server, with the export's engine |
| `data` | `dataset` | The parity sample (the first utterances of the golden set's dataset by audio hash) |
| `normalizer` | `normalizer` | The golden set's scoring normalizer |
| `smoke` (optional) | `smoke_inputs` | What the family's smoke client sends for the first utterances (`cadence.smoke-inputs/1`) |

It writes `report` (`parity_report`, `cadence.parity/1`): `report.json` with both WERs, `werDelta`
(served − reference), `identicalShare` (token sequences when both sides carry `tokens`, else the NFC texts —
`compared` says which), `disagreement` (word errors of the served transcripts with the reference's as the reference),
the thresholds, `verdict` (`passed` when all three hold), `reasons` and up to 200 `differing` utterances with both
texts; and `smoke/<name>`, the smoke inputs copied with `expected` — the served token ids joined by spaces (what the
family's smoke client prints), else the served text. The delivery bundle's smoke set is read from here.

## Place in the loop

Block 4, Deploy: export → **parity** → benchmark. The control plane records the verdict on the export
(`models.get` → `exports[].parity`); a canary promotion needs `passed` (`parity-failed`).

## Fields and defaults

| Parameter | Default | Source | Range |
| --- | --- | --- | --- |
| `max_wer_delta` | `deploy.parity_max_wer_delta` (0.001 = 0.1 WER points) | R31; A3 and E1 acceptance | 0–0.01 |
| `min_identical_share` | `deploy.parity_min_identical_share` (0.97) | Spike E1 (NeMo against itself: 0.985) | 0.9–1 |
| `max_disagreement` | `deploy.parity_max_disagreement` (0.005) | Spike E1 (fp32 ≤ 0.0033, fp16 0.017) | 0–0.1 |
| `smoke_utterances` | 20 | 02 "Delivery bundle" | 0–20 |

## Commands

`models.parity` (dry run first); `models.get`; `artifacts.get` on the report.

## Playbooks

None — a failed parity is read, not retried: the differing utterances say where the engine decides differently.

## Sources

R31; docs/spec/03-pipelines-defaults.md "Export, parity and benchmark (phase 5)"; docs/spikes/E1-onnx-triton.md
"Parity".
