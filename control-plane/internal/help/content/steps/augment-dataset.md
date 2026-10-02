---
title: augment_dataset (step kind)
summary: The robustness axis of an eval — an augmentation profile applied deterministically to a golden dataset (speed, noise from a noise bank, level, band-limit, G.711 codecs), written as a new dataset the eval transcribes and scores.
contexts: [step:augment_dataset, artifact:augment_profile, artifact:dataset, entity:evals]
---

## What this is

`augment_dataset@1` is a core step kind (CPU, job kind `eval`, shipped in every runtime image). `evals.new` inserts it
before transcription for every cell whose augmentation is not `none`: the golden set is decoded and scored a second
time as a telephone line would deliver it, so the eval report can show how much WER a model loses under the project's
augmentation profile (the robustness matrix: golden set × augmentation profile × latency profile).

Inputs:

- `data` — the golden set's `dataset` artifact;
- `profile` — an `augment_profile` artifact: the project's `augment/<name>.yaml` at the commit `evals.new` named,
  rendered by the control plane with every value the file leaves out taken from `defaults.yaml` `augment.*`, the seed
  (the request's, else the file's, else `augment.seed`) and the profile's content hash;
- `noise` (optional) — a noise bank (`noise-bank/<name>`, a `dataset` artifact of purpose noise) when the profile has
  a `noise` transform.

Output `data`, a `dataset` artifact: the golden rows in the same order with their text, language, speaker and call ids;
each row's new `audio`, `duration`, `augment` (the stages applied, e.g. `speed:1.043`, `codec:g711-ulaw`) and
`augmentedFrom` (the golden audio's hash). Its `dataset.json` carries `purpose: augmented` — it is an eval input, never
registered as a dataset version — and `augmentation` (`profile`, `seed`, `hash`, the count of utterances per stage,
the codecs left out).

How:

- Each utterance draws from its own generator, seeded by the profile's seed and the golden audio's hash: the same
  profile and seed give the same audio whatever the order or the subset, and every coin is drawn whether its stage
  applies or not.
- The chain: speed (resampling, tempo and pitch together) → noise (a noise-bank clip looped from a random offset, at an
  SNR drawn from `snr_db` against the utterance's speech power) → level (a gain in dB, clipped at full scale) →
  narrowband telephony (resampled to 8 kHz, low-passed at `cutoff_hz` when `band_limit` applies, through a G.711
  codec when `codec` applies, back to the dataset's rate).
- Codecs: G.711 μ-law and A-law are computed here. GSM-FR, AMR-NB and Opus need native codecs whose output differs
  between library versions, so they are left out of the draw, logged as a warning and listed in `unavailable` — a
  profile that names only those applies no codec.

## Place in the loop

Evaluate — `augment-g<n>a<k>` in an eval pipeline, once per golden set and augmentation; the transcribe and score steps
of the augmented cells read its output. The pipeline engine reuses the step when another eval asks for the same golden
set, profile and seed. The eval-record key of an augmented cell includes the profile's content hash and the seed
(through the decoding hash), so a record is shared only by evals that applied the same augmentation.

## Fields and defaults

The step has no parameters; the profile carries everything:

| Profile field | Default | Source | Range |
| --- | --- | --- | --- |
| `seed` | `augment.seed` (1234) | Cadence recommendation | 0 – 2³¹−1 |
| `codec.probability`, `codec.codecs` | `augment.codec_probability` (0.5), `augment.codecs` | docs/spec/03 "Augmentation" | 0 – 1 |
| `band_limit.probability`, `band_limit.cutoff_hz` | `augment.band_limit_probability` (0.5), `augment.band_limit_hz` (3400) | ITU-T G.712 | 3000 – 8000 Hz |
| `level.probability`, `level.gain_db` | `augment.level_probability` (0.3), `augment.level_gain_db` ([-10, 6]) | Cadence recommendation | -30 – 20 dB |
| `speed.probability`, `speed.factor` | `augment.speed_probability` (0.3), `augment.speed_factor` ([0.9, 1.1]) | Ko et al. 2015 | 0.8 – 1.2 |
| `noise.probability`, `noise.snr_db`, `noise.bank` | `augment.noise_probability` (0.5), `augment.noise_snr_db` ([0, 20]); the bank has no default | docs/spec/03 "Augmentation" | -5 – 40 dB |

A transform absent from the file is not applied; an unknown transform or key is refused.

## Commands

`evals.new` with `augmentations: [{profile: none}, {profile: augment/telephony.yaml@<commit>, seed}]`; `evals.get`
answers the cells per augmentation and the robustness matrix.

## Playbooks

The fine-tune playbook evaluates through `evals.new`, which plans this step when the eval needs it.

## Sources

- docs/spec/03-pipelines-defaults.md "Augmentation"; docs/review/2026-10-02-phase-3-plan.md stream R.
- ITU-T G.711 (μ-law, A-law), G.712 (telephone channel passband); T. Ko et al., "Audio augmentation for speech
  recognition", Interspeech 2015; MUSAN (OpenSLR 17) for public noise banks.
