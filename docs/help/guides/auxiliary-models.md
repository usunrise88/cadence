---
title: Auxiliary models
summary: The registry kind auxiliary — language classifiers, pseudo-label members and aligners, each with its licence check; adoption is an approval, and steps read the adopted version's payload.
contexts: [guide:auxiliary-models, registry:auxiliary, kind:auxiliary, runtime:services, view:triage]
---

## What this is

An auxiliary model is a model Cadence uses to prepare data, never one it trains or deploys: a language classifier
(role `lid`), a member of the pseudo-label ensemble (`pseudolabel`) or an aligner for reference texts (`align`). Each
is a registry version in a collection `auxiliary/<name>`, global and immutable like every registry version. Its
payload (the contract's `AuxiliaryPayload`):

| Field | Meaning |
| --- | --- |
| `roles` | `lid`, `pseudolabel`, `align` — one or more |
| `licence`, `conditions`, `sources`, `checkedAt` | The licence check: what the licence is, what it requires, where it was read and when |
| `outputsCommercialUse` | Whether the licence allows commercial use of what the model outputs; `false` is never adopted (`auxiliary-licence-refused`) |
| `languages` | BCP-47 primary tags it is fit for, or `["*"]` |
| `hfRepo`, `revision`, `engine` | Weights a worker step loads for one job, at a pinned commit, and what loads them |
| `service` | Or a running service: `{kind: grpc-asr, endpoint, protocol, tokenSecret}`. Cadence never starts it |

The control plane reads only the roles, the licence verdict and a service's endpoint; the worker pack that serves the
role reads the rest. No control-plane code names a model or a service.

**Seeded versions** (the licence check of 2026-10-03; only the rows marked OK):

| Auxiliary | Roles | Licence | Conditions |
| --- | --- | --- | --- |
| `auxiliary/whisper-large-v3` | pseudolabel, lid | Apache-2.0 | Its language detection reaches the ensemble from the `whisper_transcribe` member (`detectedLanguage`) |
| `auxiliary/whisper-he-ivrit` | pseudolabel | Apache-2.0; data CC-BY-4.0 | Attribution; never for voice cloning; Hebrew only |
| `auxiliary/oasis` | pseudolabel | omniASR Apache-2.0, Qwen3 Apache-2.0 | A running service; start it before the pipeline |
| `auxiliary/lid-voxlingua107` | lid | Apache-2.0; VoxLingua107 CC-BY-4.0 | Attribution; runs in the omni runtime (`lid_classify@2`) |
| `auxiliary/omniasr-ctc-1b` | align | Apache-2.0 | heb_Hebr, srp_Cyrl, hrv_Latn and bos_Latn confirmed in its language list (2026-10-04; srp_Latn is not in it); runs in the omni runtime (`align_reference`) |

Excluded and never registered: `facebook/mms-lid-*`, `facebook/mms-1b-all`, torchaudio `MMS_FA` (CC-BY-NC-4.0) and
community Hebrew wav2vec2 fine-tunes (licence or data unverified).

**Adoption.** `projects.adopt` of an auxiliary version is a registry-scope approval for everyone, people included
(preset rule `auxiliary-adoption`): the admin reads the licence and its conditions and decides. A version whose
licence forbids commercial use of its outputs is refused before anyone is asked. The adoption lands in `data.lock`.

**Steps read the adopted version.** A step parameter marked `x-cadence.registryRef: {kind: auxiliary, role}` names
an auxiliary (`auxiliary/<name>`, `ver_…` or `@alias`). When a pipeline is planned the control plane resolves it to
the newest version of that collection the project adopted, checks the role and the licence, and puts the version with
its payload in the step spec (`auxiliaries`); the step reads it with `ctx.auxiliary(param)`. A new version of the
collection runs the step again (its input hash covers the version). A version the project has not adopted is a plan
problem (`pipeline-invalid`). A service's endpoint must answer before anything is queued (`auxiliary-unavailable`).
An optional step whose kind no runtime publishes, or whose only publishing worker has not been seen for two minutes,
is skipped at start with the dry-run warning `step-kind-unavailable`, instead of waiting in the queue.

**The services pack.** Members that are services run in the CPU worker `worker-services` (runtime `services`,
`docker compose --profile services up -d worker-services`): today `oasis_transcribe`. The others load weights per job:
`whisper_transcribe` in the NeMo pack, `lid_classify` (VoxLingua107) and `align_reference` (omniASR CTC) in the omni
pack (runtime `omni`, `worker-omni`), whose torch 2.8 and torchaudio 2.8 the nemo-speech image cannot hold.

**The triage queue.** `pseudolabel_ensemble` marks segments its members disagree on as `pseudo-label:disputed`; the
control plane adds each to the project's triage queue (`triage.list`, event `triage.item_added` on `triage.new`) with
every member's text, the best candidate and the reason. They never reach training; the Triage panel resolves them
(phase 4, wave 2).

## Place in the loop

Data: language identification and pseudo-labels for untranscribed audio; alignment of reference texts for emission
delay ([align_reference](../steps/align-reference.md)).

## Fields and defaults

`pseudolabel.*` (the ensemble's agreement rules), `packs.nemo.whisper_*`, `packs.omni.lid_*`, `packs.omni.align_*` and `packs.services.*`
in `defaults.yaml`; each step's help page lists them.

## Commands

- `registry.search` with `kind:auxiliary`, `collections.list?kind=auxiliary` — the auxiliaries.
- `projects.adopt` — adopt one (an approval).
- `pipelines.run` with `dryRun=true` — resolves the auxiliaries and checks services.
- `triage.list` — disputed pseudo-labels.

## Playbooks

"Adapt a new language" adopts the members it needs, has the person start OASIS, pseudo-labels, previews and freezes.

## Sources

- docs/spec/08-resolutions.md R26 (auxiliary models and their licences), R45 (one-off runtimes).
- docs/review/2026-10-03-phase-4-plan.md, "Owner decisions" 1–2, "Decisions taken for phase 4" 5–8 and the R26 table.
