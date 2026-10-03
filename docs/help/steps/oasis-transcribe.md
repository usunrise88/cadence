---
title: oasis_transcribe (step kind)
summary: The OASIS pseudo-label member — send every utterance to the running OASIS service (gRPC, oasis.v1) and write its texts; Cadence never starts the service (services pack, CPU).
contexts: [step:oasis_transcribe, artifact:hypotheses, artifact:dataset, registry:auxiliary, error:auxiliary-unavailable]
---

## What this is

`oasis_transcribe@1` is the step kind of the services pack (runtime `services`, CPU, job kind `data`; compose service
`worker-services`, profile `services`). OASIS is the owner's ASR service: an omniASR ensemble (3B beam, 300M, CTC 1B
and a Qwen3 text judge) behind the gRPC contract `oasis.v1.Asr`. The step reads the service from the auxiliary the
`auxiliary` parameter names (`auxiliary/oasis`: `service: {kind: grpc-asr, endpoint, protocol: oasis.v1, tokenSecret:
oasis-token}`), resolved by the control plane to the version the project adopted.

1. `GetModelInfo` must answer within `health_timeout_s`, else the step fails with `auxiliary-unavailable`
   (retryable). The languages it reports must include every utterance's language (bare codes: `sr`, `hr`, `he`, `ru`,
   `en`), checked before any audio is sent: OASIS answers a language it never learned with fluent, wrong text.
2. Each utterance goes as one unary `Transcribe(lang, campaign_id, sample_rate 16000, pcm)` — the canonical 16 kHz
   PCM16 mono audio — `concurrency` calls at a time, each with `timeout_s`.
3. Rows: `{audio, text, member, language, confidence, reason, vote: true, model: {auxiliary, versionId, endpoint,
   engine, modelVersion}, decodingHash}`. A `NON_SPEECH` answer is an empty text.

OASIS is itself a vote of several models, so its rows carry `vote: true` and `pseudolabel_ensemble` prefers its text
when it agrees with another member. Its text is spoken form without punctuation or capitals.

The client is generated from the contract vendored at `worker/packs/services/proto/oasis_contract/v1/asr.proto`; the
copy's source commit is in `proto/SOURCE.yaml` (`23811461…`, the file last changed in `94f74382…`).

**Secret.** OASIS bound off loopback (`security.mode: isolated`) wants a bearer token. The step declares the secret
`oasis-token` (the lease passes it as `OASIS_TOKEN`); create it once with `secrets.new` from the host's
`~/.config/oasis/token`. Without the secret the lease fails before the step starts; with a wrong one the step fails
with an input error (`UNAUTHENTICATED`).

**Starting OASIS.** Cadence never starts it. On the staging host: `scripts/serve.sh ensemble no-300m` in
`/home/era/oasis` (8.6 GB on the card, beside vLLM), port 50051, which compose reaches as
`host.docker.internal:50051`. A pipeline's dry run checks that the endpoint answers.

## Place in the loop

Data. One member of the pseudo-label ensemble, on segments without text.

## Fields and defaults

| Parameter | Default | Source | Range |
| --- | --- | --- | --- |
| `auxiliary` | `packs.services.oasis_auxiliary` (`auxiliary/oasis`) | the owner's decision 1 | an adopted auxiliary with role `pseudolabel` and protocol `oasis.v1` |
| `timeout_s` | `packs.services.oasis_timeout_s` (60) | Cadence recommendation | 1 – 600 |
| `health_timeout_s` | `packs.services.oasis_health_timeout_s` (5) | Cadence recommendation | 1 – 60 |
| `concurrency` | `packs.services.oasis_concurrency` (2) | Cadence recommendation | 1 – 16 |
| `campaign_id` | `packs.services.oasis_campaign_id` (`cadence-pseudolabel`) | Cadence recommendation | — |
| `target_lang` | empty (each utterance's language) | Cadence recommendation | a BCP-47 tag |
| `transliterate` | empty | as `dataset_import` | `""`, `sr-Cyrl-Latn` |

## Commands

`pipelines.run`; `projects.adopt` of `auxiliary/oasis` first (an approval); `secrets.new` for `oasis-token`.

## Playbooks

"Adapt a new language" tells the person to start OASIS before the pseudo-label pipeline.

## Sources

- docs/review/2026-10-03-phase-4-plan.md, "Owner decisions" 1 and "Decisions taken for phase 4" 6.
- The OASIS contract, `oasis.v1` (vendored; see `proto/SOURCE.yaml`).
- Omnilingual ASR (Meta, 2025), Apache-2.0; Qwen3, Apache-2.0.
