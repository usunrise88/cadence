---
title: Project bundles
summary: Move a whole project to another Cadence instance, or keep it whole on your own storage — projects.export writes the repository, data.lock and every registry version it references with their content; bundles.adopt and projects.new with bundle read it back.
contexts: [guide:project-bundles, command:projects.export, command:bundles.adopt, command:projects.new, error:bundle-invalid, entity:export]
---

## What this is

A **project bundle** is a directory on a mount that holds everything a project needs, so another Cadence instance
can rebuild it without reaching this one:

| Path | What it is |
| --- | --- |
| `bundle.json` | `cadence.project-bundle/1`: the project's facts (name, slug, locales, domain, base model), the commit, its aliases, and the registry record of every version it carries with their sources and licences. Written last: a directory without it is not a bundle |
| `repository.bundle` | A git bundle of the project repository, `main` at the exported commit (`git clone repository.bundle` works too) |
| `data.lock` | The repository's `data.lock` at that commit |
| `datasets/<collection>/<version>/` | One dataset bundle per dataset version and noise bank — the layout `datasets.export` with `cadence-bundle` writes, so `dataset_import` can also import one on its own |
| `cas/b3/<ab>/<hash>` | Every other blob a version's payload names: checkpoints of model versions, cards, template files |

**What it carries.** Every version the project adopted (its `data.lock`) and, transitively, every version an adopted
version's payload names — a golden set's dataset version and normalizer, a model's base model. Versions published by
workers (runtimes, step kinds, model families) travel as references only: the receiving instance's workers publish
their own. Auxiliary models carry their payload (the Hugging Face repository and revision, or the service), not
weights. Work — mixes, runs, evals, sessions — is not in a bundle.

**Export.** `projects.export` (If-Match: the project's revision) plans first: every dataset version must be in the
cache (`datasets.materialize` an evicted one), the target must be a writable path mount and hold no bundle yet. The
real call records an export (`exports.get`, format `cadence-project-bundle`, `jobId`, `commit`, `versions`) and a
control-plane job writes it. Audio written unchanged is recorded as mount copies of its blobs, as other exports do,
so the cache may evict those versions.

**Import.** Two ways, both an approval the admin decides, for people too (preset rule `bundle-import`): a bundle
registers versions for every project on this instance.

- `bundles.adopt {bundle: mount://…}` on an existing project: registers what this instance lacks, adopts every version
  the bundle's project adopted, sets the bundle's aliases the project does not have yet (never `production`), and
  commits `data.lock`. The repository is not touched.
- `projects.new {name, bundle: mount://…}`: a new project whose description, locales, domain and base model default to
  the bundle's, whose internal repository is the bundle's history, and whose bootstrap job imports and adopts the
  versions, then commits `project.yaml`, `AGENTS.md`, `CLAUDE.md`, `data.lock` and the agent permission files
  rendered for this instance (the bundle's permission files are replaced).

How each version is treated:

| Version | Here already (same collection and content)? | What happens |
| --- | --- | --- |
| any | yes | Reused (`reuse`); nothing registered |
| dataset version, noise bank | no | Its blobs are copied into the content store and checked against their hashes; its sources are made (a source cleared for training there is cleared here when it is new here); the dataset is imported from its bundle as `dataset_import` `cadence-bundle` would (a freeze's cut becomes an import) |
| golden set | no | Re-frozen through this instance's checks: eval-only data, leakage against what this instance trains on |
| normalizer, auxiliary, template, base model, model | no | Registered from its record; ids in its payload point at the versions here |
| runtime, step kind, model family | no | Not registered (`missing`): payloads keep the bundle's id |

Registered versions keep the **version string** the bundle's instance gave them (`YYYY-MM-DD.<sha>`), so pipelines
that pin `collection@version` resolve; the job's result lists any that came out different (`changed`).

Adoption runs the usual checks: a licence that forbids it ([licence-forbids-adoption](../errors/licence-forbids-adoption.md)),
an auxiliary whose outputs may not be used commercially, a golden set that overlaps what the project trained on
([golden-set-leakage](../errors/golden-set-leakage.md)). The locale check was the bundle's project's.

## Place in the loop

Outside the loop: a project's whole state at a commit, for a move, a second site, or an archive on your own storage.

## Fields and defaults

| Field | Default | Meaning |
| --- | --- | --- |
| `projects.export` `ref` | `main` | The branch or commit bundled |
| `projects.export` `target` | `mount://<storage.export_mount>/projects/<slug>/<commit, 12 hex>` | A directory on a writable path mount |
| `bundles.adopt` `bundle` | required | `mount://<mount>/<dir>`, the directory holding `bundle.json` (any mount kind: path, S3, Hub) |
| `bundles.adopt` `aliases` | true | Set the bundle's aliases the project lacks |
| `projects.new` `bundle` | — | Make the project from this bundle |

## Commands

- `projects.export` — `dryRun=true`: commit, target, versions (adopted or referenced, blobs, bytes), aliases, sources.
- `bundles.adopt` — `dryRun=true`: per version `register`, `reuse`, `reference` or `missing`, blobs to copy, aliases
  (`set`, `keep`, `skip`); no approval is asked for a dry run.
- `exports.get`, `exports.list` — the export; `jobs.get` — the import job's result (registered, reused, adopted,
  aliases, missing, changed).

## Playbooks

- Move a project: `projects.export` here → copy the directory to the other instance's mount (or mount the same
  storage) → `projects.new {name, bundle}` there → the admin approves.
- Agents: plan both with `dryRun=true` and tell the person what an import would register; the import itself waits for
  the admin.

## Sources

- docs/spec/03-pipelines-defaults.md "Interoperability"; docs/spec/02-domain-projects-registry.md "Registry".
- ROADMAP "Phase 4 notes" (project bundles).
