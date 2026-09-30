---
title: Recipe
summary: A file of the project repository with its commit history, the repository's files, uncommitted agent edits, and open session and sync branches with their diff against main (three-way on conflict).
contexts: [panel:recipe, command:branches.accept, command:branches.revert, command:branches.compare, command:projects.sync]
---

## What this is

A document for one file of the project repository — `project.yaml`, a pipeline, `AGENTS.md`, `NOTES.md`, a skill —
read at `main`, with the list of every file to move between and the commits that changed this one. Below it, the
open branches of the repository: `session/<id>` branches that agent sessions commit to every turn, and
`sync/<date>` branches that **Sync templates** creates. Selecting a branch shows its diff against `main`, whether it
would fast-forward, and the files a merge would conflict on — each conflicting file opens in a three-way view: the
merge base, `main` and the branch side by side (stacked when the document is narrow), or unified, with every conflict
marked and **Next / Previous conflict** (`n` / `p` inside the view) to move between them. Files that merge cleanly
keep the two-way diff.

While an agent's turn runs, a file it has changed but not committed shows a note above the content ("Edited in
claude-code · session 3's worktree", with lines added and removed) and **Open its Chat**; the session branch in the
list carries an *uncommitted* count. The note goes away when the turn's commit lands — then the file's diff is on the
branch.

## Place in the loop

Recipes are how a project remembers how it works. People commit to `main` from the UI (agent settings, notes) or
by pushing to the repository; agents work on their own session branch and never write `main`. A session branch is
accepted or discarded with its agent session (`agentSessions.accept` / `revert`); a sync branch is accepted or
discarded here. Changes arrive live: every commit emits a `recipe.<path>` event (`recipe.changed`), and the agent
host's worktree watcher emits `recipe.working` on the same topic while a turn edits a file (Activity marks those
*uncommitted*).

## Fields and defaults

| Field | Meaning |
| --- | --- |
| Commit, Size, Last change, By | The file at `main`: the commit it was read at, bytes, the latest commit that touched it |
| Branch kind | `session` (an agent session), `sync` (a template sync) or `other` (pushed by a person) |
| +ahead / −behind | Commits on the branch that `main` lacks, and commits on `main` the branch lacks |
| Fast-forwards main | `main` has not moved since the branch left it; accepting moves `main` to the branch |
| Conflicts | Files a merge into `main` would conflict on; accepting is refused until they are resolved |
| Three-way | For a conflicting file: base, main and branch texts (up to 128 KiB each) and the conflict hunks |
| Uncommitted | Files an agent's running turn changed in its worktree, not yet committed to the branch |

## Commands

- `projects.sync` — Sync templates: re-renders skills, starter pipelines, agent config and `AGENTS.md` (unless
  custom) with the current template versions; differences arrive as a draft branch `sync/<date>`
- `branches.accept` — Accept a sync branch into `main` (fast-forward when possible, otherwise a merge commit); a
  conflict answers `merge-conflict` and leaves `main` unchanged
- `branches.revert` — Discard a sync branch
- `recipes.list`, `recipes.get`, `branches.list`, `branches.get` — the reads behind this document
- `branches.compare` — a branch compared with `main` in three ways, file by file (the three-way view)

## Playbooks

- After a Cadence upgrade: Sync templates, read the diff, accept it.
- Reviewing agent work: open the session branch, read the diff, then accept or discard it from the session.

## Sources

docs/spec/05-agents.md "Worktree, drafts and merge"; docs/spec/02-domain-projects-registry.md "Registry" (templates
and skills sync as a draft commit); docs/spec/08-resolutions.md R1 (`projects.sync`), R10 (the internal repository).
