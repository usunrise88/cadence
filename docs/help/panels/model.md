---
title: Model
summary: A registered model version — the checkpoint it publishes, its gate verdict and eval, lineage, the projects that use it, the model card, and from phase 5 its exports, deployments, promotions and deliveries.
contexts: [panel:model, command:models.get, command:models.register, command:aliases.set, command:models.export, command:models.parity, command:models.benchmark, command:deployments.new, command:deployments.list, command:deployments.get, command:deployments.promote, command:deployments.rollback, command:promotions.list, command:promotions.get, command:promotions.verify, entity:deployment]
---

## What this is

A document for one **model version** (`models.get`), a registry entry in `model/<name>`. A model version is a
checkpoint whose gate passed, published with the eval that passed it (R22). It shows:

- **Gate and eval**: the verdict, the `gates.yaml` commit it used, and a link to the Eval report.
- **What it publishes**: the checkpoint, its weights hash and artifact, the model family, the base model it was
  trained from, and the run.
- **Used by**: projects that adopted it, with their aliases (`@baseline` once it is set as one).
- **Exports** (phase 5): one deployable per latency profile and format, each with its newest parity check (ΔWER,
  identical share, the reasons when it failed) and its newest benchmark (p95 chunk latency at the verdict's
  concurrency against the budget, the streams per card, the card class; *contended* when processes outside Cadence
  used the card). A chart draws every finished benchmark's p95 beside its budget.
- **Deployments** (phase 5) of this version in the open project: the stage (shadow, canary, production, retired) and
  state (active, pending delivery, rolled back, retired), the target and slot, the canary's traffic share, the shadow's
  progress (hours replayed against `deploy.shadow_min_hours`, calls, nights, the divergence from the comparison model
  with its interval, the next nightly replay), the boost lists it ships, and — on a delivery target — the slot's
  promotion chain, each record verified again on read.
- **Receipt box** of a pending promotion or rollback: the delivery bundle's state, `deliver.sh`, the smoke check's
  size, a short-lived download link (people only) and a box for the `CADENCE-RECEIPT` line the script printed.
- **Model card**: Markdown the control plane writes at registration — the gate's checks, the eval's cells, the
  training composition, lineage and departures from defaults. It renders without raw HTML; links open in a new tab.

The Lineage tab lists the run, mix, recipe commit and dataset versions; the Lineage panel draws them.

## Place in the loop

Evaluation · Record, then Block 4, Deploy: export → parity → benchmark → shadow → canary → production, each step
reversible and the last two signed by a person.

## Fields and defaults

| Field | Default | Meaning |
| --- | --- | --- |
| Collection | `model/<project slug>` | Where `models.register` publishes the version |
| Card | generated at registration | Markdown; raw HTML is not rendered |
| Latency profile | the primary profile | Of an export, parity check, benchmark or shadow deployment |
| Traffic share | `deploy.canary_share` (0.05) | A canary's share of the slot's traffic |
| Shadow volume | `deploy.shadow_min_hours` (20 h) | Replayed call audio a canary needs |

## Commands

| Command | API | Notes |
| --- | --- | --- |
| Register model version | `models.register` | From a passed Eval report (inline confirm) |
| Set as baseline | `aliases.set` (name `baseline`) | The version must be adopted by the project (registration adopts it). `baseline` is gated: the answer is an approval id; the alias moves once a person approves |
| Export…, Parity check…, Benchmark… | `models.export`, `models.parity`, `models.benchmark` | **Plan** dry-runs (the profiles or steps and the estimate); the real call starts a pipeline run, or answers an approval when the GPU time is over budget. A benchmark can take a delivery target's concurrency for its verdict |
| Deploy to shadow… | `deployments.new` | Replay mount, path and registered source; optional language, profile and the model to compare with. **Check** dry-runs |
| Promote… | `deployments.promote` | The confirm modal: **Check** answers every check (target serves the export, the engine loads there, parity, benchmark, shadow volume, slot free, confirmed canary for production); **Confirm** asks for the approval and decides it as you, so the signed record names you as its approver. Promoting to the stage a deployment holds with another traffic share or boost lists is a config-only promotion |
| Roll back… | `deployments.rollback` | The same modal: the slot returns to its confirmed earlier production version |
| Confirm delivery | `promotions.verify` | People only: paste the receipt line; a mismatch changes nothing |

## Playbooks

- **Make it the next baseline.** Press **Set as baseline** (or ask the agent: `aliases.set name=baseline`). It
  answers an approval id; once approved, `@baseline` points at this version.
- **Ship it.** Export, check parity, benchmark at the delivery target's concurrency, deploy to shadow, wait for the
  shadow volume, then Promote… to canary. Download the bundle, run `sh deliver.sh` on the production host, paste the
  receipt. Later, Promote… the canary to production the same way.
- An agent may export, check, benchmark and deploy to shadow within its budget; its promotions and rollbacks wait in
  Approvals for a person, and it never confirms a delivery.

## Sources

- docs/spec/11-ui-panels.md "Panel catalogue" (Model); R22, R23, R30–R33 in docs/spec/08-resolutions.md;
  docs/spec/02-domain-projects-registry.md "Deployment entities" and "Promotion records".
- Model cards: Mitchell et al., "Model Cards for Model Reporting" (FAT* 2019).
- [Delivery script](../guides/delivery-script.md); [Staging serving](../guides/staging-serving.md).
