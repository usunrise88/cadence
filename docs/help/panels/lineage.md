---
title: Lineage
summary: The graph around the active document from registry.lineage — what it was built from on the left, what uses it on the right — with every node one click from its document.
contexts: [panel:lineage, command:registry.lineage]
---

## What this is

A tool panel that follows the active document: a golden set, model, run, mix or dataset version, or an Eval report
(whose **subject** it draws). It walks `registry.lineage` both ways and lays the graph out in columns, one per hop:

- **Left — built from** (upstream): sources → dataset versions → mix → run → checkpoint → model version; a golden
  set's dataset version and normalizer; a model's base model.
- **Right — used by** (downstream): projects that adopted it and their aliases, golden sets over a dataset, mixes and
  runs that read it, models trained from a base.

Edges point from what was used to what used it; hover an edge for how (`datasetVersionId`, `mix`, `adopted`, …).
Click a node to open its document (or the Inspector for kinds without one; a pipeline run opens in Pipeline run).
**List** shows the same nodes as a table with **Id** to copy each version id.

**Direction** limits the walk to one side; **Depth** is the number of hops each way (default 3). Nodes in projects
you cannot see are left out and counted; a large graph is cut at the node limit and says so.

## Place in the loop

Every step · Record: where a number came from, and what a change would touch.

## Fields and defaults

| Field | Default | Meaning |
| --- | --- | --- |
| Direction | both | Built from, used by, or both |
| Depth | 3 hops | 1–5 here; the API allows up to 10 |
| Node limit | 200 | The API's default; the graph says when it is cut |

## Commands

| Command | API | Notes |
| --- | --- | --- |
| Open a node | — | Its document, Pipeline run, or the Inspector |
| Copy id | — | List view |

## Playbooks

- **Which datasets trained this model?** Open the model, set Direction to Built from and Depth to 4: the dataset
  versions sit in the leftmost column.
- **What uses this golden set?** Open it, Direction Used by: the projects that adopted it.

## Sources

- docs/spec/11-ui-panels.md "Panel catalogue" (Lineage); docs/spec/02-domain-projects-registry.md (registry and
  lineage).
- Layered graph drawing: Sugiyama, Tagawa & Toda (1981), with one barycentre ordering sweep.
