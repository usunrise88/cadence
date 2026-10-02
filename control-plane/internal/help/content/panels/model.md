---
title: Model
summary: A registered model version — the checkpoint it publishes, its gate verdict and eval, lineage, the projects that use it, and the model card.
contexts: [panel:model, command:models.get, command:models.register]
---

## What this is

A document for one **model version** (`models.get`), a registry entry in `model/<name>`. A model version is a
checkpoint whose gate passed, published with the eval that passed it (R22). It shows:

- **Gate and eval**: the verdict, the `gates.yaml` commit it used, and a link to the Eval report.
- **What it publishes**: the checkpoint, its weights hash and artifact, the model family, the base model it was
  trained from, and the run.
- **Used by**: projects that adopted it, with their aliases (`@baseline` once it is set as one).
- **Model card**: Markdown the control plane writes at registration — the gate's checks, the eval's cells, the
  training composition, lineage and departures from defaults. It renders without raw HTML; links open in a new tab.

The Lineage tab lists the run, mix, recipe commit and dataset versions; the Lineage panel draws them.

## Place in the loop

Evaluation · Record: the result of a passed gate, ready to become the next baseline (and, from phase 5, a
deployment).

## Fields and defaults

| Field | Default | Meaning |
| --- | --- | --- |
| Collection | `model/<project slug>` | Where `models.register` publishes the version |
| Card | generated at registration | Markdown; raw HTML is not rendered |

## Commands

| Command | API | Notes |
| --- | --- | --- |
| Register model version | `models.register` | From a passed Eval report (inline confirm) |
| Set as baseline | `aliases.set` | `baseline` is gated: it waits for an approval |
| Export, promote, roll back | — | Arrive with deployment (phase 5) |

## Playbooks

- **Make it the next baseline.** Ask the agent (or an admin) to set the project's `baseline` alias to this version;
  the next evals compare against it, and its cells come from the cache.

## Sources

- docs/spec/11-ui-panels.md "Panel catalogue" (Model); R22, R23 in docs/spec/08-resolutions.md.
- Model cards: Mitchell et al., "Model Cards for Model Reporting" (FAT* 2019).
