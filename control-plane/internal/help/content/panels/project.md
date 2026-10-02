---
title: Project
summary: The project home — locales, base model, repository, agent profile, budgets, the five blocks, gates and notes.
contexts: [panel:project, command:projects.new, command:projects.note, command:projects.sync, command:gates.edit]
---

## What this is

The Project document is the home of a project: a unit of work with its own repository, budgets and gates. Its
header shows the project's name, state, locales, base model and repository; the Overview lists the project facts the
wizard set, the repository (clone URL, remote, head of `main`), the agent profile, budgets, the five blocks of the
Cadence cycle — data, training, evaluation, deployment, flywheel — and the gates. The Notes tab shows `NOTES.md`,
the learnings every agent session reads.

A project is created by the **New project** wizard: three fields in Recommended mode (name, language, where the call
recordings are — skippable until the Data block arrives), everything else filled from `defaults.yaml` and shown as a
summary with **Customise**. Creating it answers at once with a bootstrap job; the wizard shows its progress and opens
the project when it ends.

## Place in the loop

Every loop starts here: the next-step bar names what to prepare next. After the bootstrap the repository holds
`project.yaml`, `AGENTS.md` (rendered from the instructions template), `CLAUDE.md` (`@AGENTS.md`), `NOTES.md`,
`data.lock`, the agent config rendered from the permission preset, Cadence's skills under `.claude/skills` and the
starter pipelines. The five blocks show what the project holds: Training counts mixes and runs (and how many
finished); Evaluation counts the golden sets the project adopted and its evals, with the newest gate verdict and what
`@baseline` points at (the base model while it is unset). Data arrives in phase 4, deployment and the flywheel in 5.

## Gate

The **Gate** section shows the project's gate as `gates.get` reads it from `gates.yaml` on `main`: the primary
profile, the target golden sets and their rule, the replay sets and the regression they may show, the
deletions-and-insertions check and the significance settings, with every value that departs from `defaults.yaml`
highlighted. Without a `gates.yaml` the defaults apply: golden sets in the project's languages are targets, the
others replay sets.

**Edit gates.yaml** (`gates.edit`; also from the Eval report's Gate section and the palette) edits the file in place:
**Check** is a dry run that parses it, checks every golden set it names is adopted and shows the gate it would make;
**Commit to main** commits it with `If-Match` on the commit that last changed the file (`defaults` while there is
none). If someone changed it meanwhile nothing is committed and you reload. A person's edit commits; an agent's waits
for an approval and the editor shows the approval id. See the [evaluation guide](../guides/evaluation.md).

## Fields and defaults

| Field | Meaning | Default |
| --- | --- | --- |
| State | `bootstrapping` while the job writes the repository, `active`, `failed` (the reason is shown), or `archived` | — |
| Slug | The project's id in paths and links (`/p/<slug>/w/<workspace>`); lowercase letters, digits, dashes | From the name |
| Locales, Domain | What the project adapts to; written to `project.yaml` and `AGENTS.md` | `wizard.locale`, `wizard.domain` |
| Base model | The adopted registry version and its pinned Hugging Face revision | `wizard.base_model` (newest frozen version) |
| Repository | `internal` (served by Cadence at `/git/<slug>.git`), a new GitHub repository, or an existing URL; `main` is the project branch | `wizard.repository` |
| Budgets | GPU-hours per day and agent tokens per day | `budgets.gpu_hours_per_project_per_day`, `budgets.agent_tokens_per_project_per_day` |
| Revision | Edit counter used for `If-Match`; edits, notes and adoptions increment it | — |

Clone the internal repository with `git clone <clone URL>` and an API key (`cdk_…`) as the password; pushes to `main`
are accepted and appear as recipe changes.

## Commands

- `projects.new` — New project (the wizard; menu bar → project switcher or the palette)
- `projects.edit` — Edit project: name, description, locales, domain, base model, budgets (primary action)
- `projects.note` — Add a note: one dated learning appended to `NOTES.md` and committed to `main`
- `projects.sync` — Sync templates and skills: the update arrives as a draft branch `sync/<date>` to review in Recipe
- `projects.archive` — Archive project (inline confirm, reversible): worktrees go, the repository and artifacts stay read-only
- `gates.edit` — Edit gates.yaml: Check (dry run), then Commit to main; an agent's change waits for an approval
- `agentProfile.edit` — Open Agent settings

## Playbooks

The Project home's **Playbooks** section offers the playbook this phase runs — "Fine-tune from a dataset version" in
phase 2 — with its estimate from the defaults; Start playbook opens the same form as Agent sessions (inputs, the
estimate before starting). The other v1 playbooks are listed with the phase they run from ("Adapt a new language"
from phase 4). `playbooks.run` starts it.

## Sources

Cadence recommendation — docs/spec/02-domain-projects-registry.md "Projects" and "Project wizard";
docs/spec/11-ui-panels.md "The recommended path"; docs/spec/08-resolutions.md R1 (`projects.new` answers 202 with the
bootstrap job) and R10 (the internal repository).
