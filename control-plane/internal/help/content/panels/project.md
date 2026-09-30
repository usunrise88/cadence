---
title: Project
summary: The project home — locales, base model, repository, agent profile, budgets, the five blocks, gates and notes.
contexts: [panel:project, command:projects.new, command:projects.note, command:projects.sync]
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
starter pipelines. The blocks fill as their phases land (training in phase 2, evaluation in 3, data in 4, deployment
and flywheel in 5).

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
- `agentProfile.edit` — Open Agent settings

## Playbooks

Playbooks ("Adapt a new language", "Fine-tune from a dataset version") appear on the Project home with their
estimate from phase 2.

## Sources

Cadence recommendation — docs/spec/02-domain-projects-registry.md "Projects" and "Project wizard";
docs/spec/11-ui-panels.md "The recommended path"; docs/spec/08-resolutions.md R1 (`projects.new` answers 202 with the
bootstrap job) and R10 (the internal repository).
