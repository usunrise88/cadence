---
title: Parity check not passed
summary: A canary promotion needs the export's newest parity check to have passed — the served engine must decode the parity sample like the model's own decoder. It has not passed, has not finished, or failed.
contexts: [error:parity-failed, command:models.parity, command:deployments.promote, artifact:parity_report]
---

## What this is

A `422 Unprocessable Entity` problem of type `parity-failed`, answered before an approval is asked when a canary
promotion's export has no passed parity check. Parity (R31) decodes the fixed parity sample — the first
`deploy.parity_sample_utterances` (200) utterances by audio hash of the project's first target golden set — twice:
with the family's own decoder at the eval's batch, and through the staging server with the export's engine. It
passes when all three hold:

| Check | Default | What it catches |
| --- | --- | --- |
| \|WER(served) − WER(reference)\| | ≤ `deploy.parity_max_wer_delta` (0.001 = 0.1 WER points) | The engine is less accurate |
| identical token sequences | ≥ `deploy.parity_min_identical_share` (0.97) | The engine decides differently on many utterances |
| word disagreement (served against reference) | ≤ `deploy.parity_max_disagreement` (0.005) | A precision change that WER alone averages away |

Spike E1 measured an fp32 TensorRT engine at 99.0 % identical and Δ WER 0.000 at 80 ms; an fp16 engine at 76 % and
+0.10 points, which this check refuses. The model's own decoder agrees with itself on only 98.5 % between batch 8 and
batch 1, which is why the identical share is 0.97, not R31's first 0.995 (owner to confirm).

## Place in the loop

Block 4, Deploy: export → **parity** → benchmark → shadow → canary.

## Fields and defaults

| Field | Meaning |
| --- | --- |
| `type` | `https://cadence.local/help/errors/parity-failed` |
| `status` | `422` |
| `detail` | The export, its parity state and which threshold failed |

## Commands

- `models.get` — `exports[].parity`: state, `werDelta`, `identicalShare`, `disagreement`, `reasons`, `reportHash`.
- `artifacts.get` on the report — the differing utterances with both transcripts.
- `models.parity` — run the check again (after a new export or a server upgrade).

## Playbooks

- A failed parity is a finding, not a retry: read the differing utterances; an engine built at another precision,
  or a server version the export was not built for, is the usual cause.

## Sources

- R31; docs/spec/03-pipelines-defaults.md "Export, parity and benchmark (phase 5)"; docs/spikes/E1-onnx-triton.md
  "Parity".
