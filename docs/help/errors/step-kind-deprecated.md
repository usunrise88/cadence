---
title: Step kind deprecated
summary: A pipeline file newly pins a step kind version whose pack deprecated it and whose cut-off day has passed; pin the replacement instead.
contexts: [error:step-kind-deprecated, command:recipes.edit, command:recipes.new]
---

## What this is

A `422 Unprocessable Entity` problem of type `step-kind-deprecated`. A worker pack deprecates a step kind version in
what it publishes (`stepKinds.get`: `deprecation` with `after`, `replacedBy`, `note`). A deprecated kind keeps
running:

- every plan that pins it carries a warning (`pipelines.run` dry run: `warnings[]`, code `step-kind-deprecated`; the
  plan step repeats its `deprecation`);
- from the day `after` (UTC), saving a pipeline file (`recipes.new`, `recipes.edit`) that pins it while the file on
  `main` did not is refused with this problem. Pins already on `main` keep working until the file changes them.

Nothing was committed.

## Place in the loop

Recipes: pipelines pin `kind@version`; a pack replaces a version with a new one (a new parameter changes the schema
and bumps the version) and deprecates the old one so new work moves over while running work finishes.

## Fields and defaults

| Field | Meaning |
| --- | --- |
| `type` | `https://cadence.local/help/errors/step-kind-deprecated` |
| `status` | `422` |
| `errors[]` | `steps[<n>].kind` for each step that newly pins a closed kind, with the replacement |

## Commands

- `stepKinds.list` / `stepKinds.get` — what workers publish, with each version's deprecation.
- `recipes.edit` — pin `replacedBy` and save again.
- `projects.sync` — brings the bundled pipelines up to date.

## Playbooks

- Agents: change the pin in the session worktree to `replacedBy` and check the plan (`pipelines.run` dry run)
  before asking for the merge.

## Sources

- docs/spec/02-domain-projects-registry.md "Registry": step kinds can be deprecated but not removed while a pipeline
  pins them.
