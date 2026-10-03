---
title: Lineage — what something was built from and what uses it
summary: registry.lineage walks the graph around any registry version, source, run, checkpoint, mix or pipeline run, upstream and downstream, with the field or table that links each pair.
contexts: [guide:lineage]
---

## What this is

Everything a model was built from is reachable from the model, and every registry entry answers "used by".
`registry.lineage` (`GET /registry/{id}:lineage`) walks that graph from one entity:

- **Upstream** — what it was built from: a model's checkpoint, run, mix, datasets and base model; a dataset
  version's sources and pipeline run; a golden set's dataset version and normalizer; a run's mix revision, base
  model and start checkpoint.
- **Downstream** — what was built from it or uses it: projects that adopted a version (with the aliases pointing at
  it), golden sets over a dataset, mixes that read it and the runs over those mixes, checkpoints of a run, models
  from a base or a checkpoint, pipeline runs whose steps read a version's artifact.

Edges point from the entity that was used to the one that used it; `relation` says how (`datasetVersionId`,
`sourceIds`, `lineage.runId`, `mix r3`, `init`, `adopted (@baseline)`).

## Place in the loop

Decide and audit: before promoting a model, see every dataset and mix it came from; before changing or deprecating a
dataset version or normalizer, see which golden sets, mixes, runs and projects depend on it.

## Fields and defaults

| Parameter | Default | Meaning |
| --- | --- | --- |
| `direction` | `both` | `upstream`, `downstream` or both |
| `depth` | 3 (1–10) | Hops each way |
| `limit` | 200 (1–1000) | Most nodes; `truncated` says the walk stopped there |

Each node carries its kind, a label, its project (project work only), its state and its distance from the root.
Projects are end points: what two projects adopted is not lineage of each other. Nodes in projects your credential
cannot reach are left out and counted in `hidden`; without registry read, registry entries are too.

## Commands

- `registry.lineage` — the agent tool and the Lineage panel's data.

A registry version links to every entity id its payload names, at any depth (`datasetVersionId`,
`normalizerVersionId`, `lineage.datasetVersionIds`, `checkpointId`, …): that one convention covers golden sets,
normalizers, models and any kind added later, in both directions. Project work kept in tables — runs, checkpoints,
mixes, pipeline runs — links through its columns.

## Playbooks

- "Where did this model come from?": `registry.lineage` on the model version with `direction=upstream`.
- "Who uses this dataset?": `direction=downstream&depth=1`, then follow a mix to its runs.

## Sources

- docs/spec/02-domain-projects-registry.md "Registry" (lineage both ways, "used by"); the W&B Registry pattern it
  follows. See also [Training runs](runs.md) and [Language packs](language-packs.md).
