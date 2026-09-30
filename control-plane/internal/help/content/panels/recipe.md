---
title: Recipe
summary: A file of the project repository with its commit history, the repository's files, and open session and sync branches with their diff against main.
contexts: [panel:recipe, command:branches.accept, command:branches.revert, command:projects.sync]
---

## What this is

A document for one file of the project repository — `project.yaml`, a pipeline, `AGENTS.md`, `NOTES.md`, a skill —
read at `main`, with the list of every file to move between and the commits that changed this one. Below it, the
open branches of the repository: `session/<id>` branches that agent sessions commit to every turn, and
`sync/<date>` branches that **Sync templates** creates. Selecting a branch shows its diff against `main`, whether it
would fast-forward, and the files a merge would conflict on.

## Place in the loop

Recipes are how a project remembers how it works. People commit to `main` from the UI (agent settings, notes) or
by pushing to the repository; agents work on their own session branch and never write `main`. A session branch is
accepted or discarded with its agent session (`agentSessions.accept` / `revert`); a sync branch is accepted or
discarded here. Changes arrive live: every commit emits a `recipe.<path>` event.

## Fields and defaults

| Field | Meaning |
| --- | --- |
| Commit, Size, Last change, By | The file at `main`: the commit it was read at, bytes, the latest commit that touched it |
| Branch kind | `session` (an agent session), `sync` (a template sync) or `other` (pushed by a person) |
| +ahead / −behind | Commits on the branch that `main` lacks, and commits on `main` the branch lacks |
| Fast-forwards main | `main` has not moved since the branch left it; accepting moves `main` to the branch |
| Conflicts | Files a merge into `main` would conflict on; accepting is refused until they are resolved |

## Commands

- `projects.sync` — Sync templates: re-renders skills, starter pipelines, agent config and `AGENTS.md` (unless
  custom) with the current template versions; differences arrive as a draft branch `sync/<date>`
- `branches.accept` — Accept a sync branch into `main` (fast-forward when possible, otherwise a merge commit); a
  conflict answers `merge-conflict` and leaves `main` unchanged
- `branches.revert` — Discard a sync branch
- `recipes.list`, `recipes.get`, `branches.list`, `branches.get` — the reads behind this document

## Playbooks

- After a Cadence upgrade: Sync templates, read the diff, accept it.
- Reviewing agent work: open the session branch, read the diff, then accept or discard it from the session.

## Sources

docs/spec/05-agents.md "Worktree, drafts and merge"; docs/spec/02-domain-projects-registry.md "Registry" (templates
and skills sync as a draft commit); docs/spec/08-resolutions.md R1 (`projects.sync`), R10 (the internal repository).
