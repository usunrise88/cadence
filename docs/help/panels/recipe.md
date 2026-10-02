---
title: Recipe
summary: A file of the project repository with its commit history, the repository's files, uncommitted agent edits, and open session and sync branches with their diff against main (three-way on conflict).
contexts: [panel:recipe, command:recipes.new, command:recipes.edit, command:branches.accept, command:branches.revert, command:branches.compare, command:projects.sync]
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

The status bar's **Branches** badge counts the branches that wait for you (a sync, another branch, an ended
session's unmerged changes) in every panel, blinks while any wait, and opens the chosen one here.

An **augmentation profile** (`augment/<name>.yaml`, docs/spec/03-pipelines-defaults.md "Augmentation") opens as a
form above its text: the seed and four transforms — **codec** (probability and the codecs one is drawn from),
**band-limit** (probability and cut-off), **level** (probability and gain range in dB) and **speed** (probability and
factor range) — each field with "Why this default?" and a "departs from default" chip. **Reset to recommended**
puts every value back to defaults.yaml (`augment.*`); **Save** commits the file to `main` (`recipes.edit`), keeping
its comments and any keys the form does not know. **New augmentation profile** (the Files header) commits
`augment/telephony.yaml` at the recommended values and opens it.

**Edit** opens any text file on `main` as plain text (Tab indents two spaces): **Check** sends a dry run, **Commit to
main** commits it with your message (default `edit <path>`). A pipeline (`pipelines/<name>.yaml`) is planned before
it is committed — strict YAML, the name equal to the file's, kinds a worker publishes, wiring, every parameter against
its kind's schema — and a problem lists each field (`pipeline-invalid`) and commits nothing; inputs are not needed
for that. Files the agent profile renders have no Edit (Agent settings changes them).

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

Augmentation profile fields (defaults from defaults.yaml `augment.*`; each transform applies to an utterance with its
probability, ranges are drawn uniformly):

| Field | Default | Safe range |
| --- | --- | --- |
| `seed` | 1234 (recorded on the run) | 0 – 2^31−1 |
| `transforms.codec.probability`, `.codecs` | 0.5; g711-ulaw, g711-alaw, gsm-fr, amr-nb, opus | 0 – 1; those five |
| `transforms.band_limit.probability`, `.cutoff_hz` | 0.5; 3400 Hz | 0 – 1; 3000 – 8000 Hz |
| `transforms.level.probability`, `.gain_db` | 0.3; −10 to 6 dB | 0 – 1; −30 – 20 dB |
| `transforms.speed.probability`, `.factor` | 0.3; 0.9 to 1.1 | 0 – 1; 0.8 – 1.2 |

A value outside its safe range is a warning, not a block.

## Commands

- `projects.sync` — Sync templates: re-renders skills, starter pipelines, agent config and `AGENTS.md` (unless
  custom) with the current template versions; differences arrive as a draft branch `sync/<date>`
- `branches.accept` — Accept a sync branch into `main` (fast-forward when possible, otherwise a merge commit); a
  conflict answers `merge-conflict` and leaves `main` unchanged
- `branches.revert` — Discard a sync branch
- `recipes.edit` — commit a changed file to `main` (the editor's Commit to main, the augmentation form's Save). If-Match
  is the commit that last changed the file; a file changed meanwhile answers `412 precondition-failed` and the form
  offers Reload; a pipeline that would not plan answers `422 pipeline-invalid`. Files the
  agent profile renders (`AGENTS.md`, `CLAUDE.md`, `.claude/settings.json`, `opencode.json`) are refused: change them
  in Agent settings
- `recipes.new` — commit a new file (New augmentation profile); an existing path answers `409 conflict`; a new
  pipeline is planned first like an edit
- `recipes.list`, `recipes.get`, `branches.list`, `branches.get` — the reads behind this document
- `branches.compare` — a branch compared with `main` in three ways, file by file (the three-way view)

## Playbooks

- After a Cadence upgrade: Sync templates, read the diff, accept it.
- Reviewing agent work: open the session branch, read the diff, then accept or discard it from the session.
- Telephony augmentation: New augmentation profile, lower the codec probability if the calls are clean, Save. Agents
  change profiles on their session branch (the default preset forbids `recipes.new` and `recipes.edit` for agents).

## Sources

docs/spec/03-pipelines-defaults.md "Augmentation" (profile, transforms; NeMo's lossy-codec augmentation and Lhotse's
transforms); ITU-T G.712 (telephone passband 300–3400 Hz); Ko et al., Interspeech 2015 (speed perturbation 0.9–1.1);
docs/spec/05-agents.md "Worktree, drafts and merge"; docs/spec/02-domain-projects-registry.md "Registry" (templates
and skills sync as a draft commit); docs/spec/08-resolutions.md R1 (`projects.sync`), R10 (the internal repository).
