---
title: hf_push (step kind)
summary: Pushes a frozen dataset version to the Hugging Face Hub as an audiofolder dataset with its card — after the licence check and an approval the admin decides.
contexts: [step:hf_push, artifact:export, command:datasets.export, error:export-not-allowed]
---

## What this is

`hf_push@1` is a runtime-neutral core step kind (CPU, job kind `export`) that `datasets.export` runs for format
`hf-hub`. Before it runs, the control plane checks the version — no production source, every source with a usable
licence, no golden set built on it ([export-not-allowed](../errors/export-not-allowed.md)) — and the real call waits
for an approval the admin decides, for people too (preset rule `hub-export`): a pushed dataset has left the instance.

The step lays the dataset out as an **audiofolder** dataset and pushes it in one commit:

| File | Content |
| --- | --- |
| `data/<split>/<hash>.wav` | The dataset's WAV files (16 kHz mono PCM) |
| `data/<split>/metadata.jsonl` | `file_name`, `transcription`, `language`, `speaker_id?`, `duration` per file |
| `README.md` | The Hub's YAML header — `license` (the version's licence as a Hub id: `CC-BY-4.0` → `cc-by-4.0`, else `other` with `license_name`), `language`, `task_categories: [automatic-speech-recognition]`, one config whose splits point at `data/<split>/*` — then the dataset card |

It creates the repository when it is missing (private unless `private` is false) and records the repository, the
commit and every file pushed in its `export` artifact (`target: hf://datasets/<org>/<name>`, `hub: {repo, commit,
url, private}`).

**The token** is the secret `hf-token` (Settings → Secrets, the admin's): the step declares it, so its value reaches
only this step's lease as `HF_TOKEN` — never a log, an event or an agent. `HF_ENDPOINT` in the worker's environment
points at another Hub. The step needs `huggingface_hub`, which the NeMo Speech runtime image has.

## Place in the loop

Data → published: a dataset a person decided to share. Models go to the Hub with the deployment work (phase 5).

## Fields and defaults

| Parameter | Default | Source | Range |
| --- | --- | --- | --- |
| `repo` | `""` (datasets.export's `hubRepo`) | spec 03 Interoperability | `<org>/<name>` |
| `private` | `storage.export_hub_private` (true) | Cadence recommendation | true, false |
| `licence` | `""` (the version's licence) | spec 03 (a licence check before the push) | required |
| `version` | `""` | Cadence recommendation | the dataset version (`ver_…`) |
| `name` | `""` | Cadence recommendation | the collection without `dataset/` (the card's title) |

## Commands

- `datasets.export {version, format: hf-hub, hubRepo, hubPrivate?}` — 202 with the approval; the approved call starts
  the push. `dryRun=true` shows the plan and `approval: true`.
- `exports.get` — the commit once it is pushed.

## Playbooks

- Push privately first, look at the dataset viewer on the Hub, then make it public there: the push itself never
  publishes more than the repository's visibility allows.

## Sources

- Hugging Face Hub documentation: dataset cards (YAML metadata), the audiofolder layout and `HfApi.upload_folder`.
- docs/spec/03-pipelines-defaults.md "Interoperability" (to the Hub with its generated card after a licence check).
- docs/review/2026-10-03-phase-4-plan.md, stream I.
