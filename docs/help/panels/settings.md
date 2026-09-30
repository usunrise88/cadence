---
title: Settings
summary: Instance-wide configuration for the admin — compute, secrets, credentials, policies, catalogues, security and the audit log.
contexts: [panel:settings]
---

## What this is

A tool panel for the admin account only (it opens floating; View → Open Settings, or the user menu). Sections:

| Section | What it holds |
| --- | --- |
| Compute | Hosts and their cards: memory, memory cap per card, allowed job kinds (training, eval, shadow, export), health |
| Secrets | Named credentials (Hugging Face, NGC, GitHub, S3, judge API): name, kind, scope, who added it, last use. The value is write-only |
| Credentials | API keys (`cdk_…`) scoped to one project and/or registry read; your browser sessions; agent session tokens |
| Policies | Default budgets: GPU-hours per project per day, agent turns per session |
| Catalogues | Read-only: base models, instruction templates and permission presets in the registry |
| Security | Two-factor sign-in (TOTP) for the admin account |
| Audit log | Every command, denial and failed attempt with actor, outcome, rule and cause; filter by actor, operation and project |

## Place in the loop

Prepare. Settings sets the limits every project works inside; nothing here spends GPU time.

## Fields and defaults

Every defaulted field shows its default and a **Why this default?** popover with the description, the safe range
and the source, rendered from `defaults.yaml` (`defaults.get`). A value outside the safe range is a warning; the
server refuses values outside the ranges it enforces.

| Field | Default | Source |
| --- | --- | --- |
| Memory cap (staging card) | 24 GB of 96 GB | docs/spikes/A3 (memory fraction 0.25) |
| GPU-hours per project per day | 8 GPU-h (0–192) | Cadence recommendation |
| Agent turns per session | 200 turns (1–2000) | Cadence recommendation |

Policies show **departures from defaults** as chips; **Reset to recommended** puts every budget back to
defaults.yaml.

Secret values and API key tokens are never shown again: a secret's value leaves the page with the request; an API
key's token appears once, right after creation, with a Copy button — Cadence keeps only its hash.

## Commands

| Command | API | Notes |
| --- | --- | --- |
| Edit compute | `compute.edit` | If-Match on the host's revision; a change made meanwhile answers `412` and the form offers "Discard mine" or "Apply mine" on the new revision |
| Add secret | `secrets.new` | The value is write-only |
| Create API key | `credentials.new` | Scope: one project, registry read, or both; optional expiry |
| Revoke credential | `credentials.revoke` | Inline confirm; irreversible. Your current session cannot be revoked here — sign out instead |
| Edit policies | `policies.edit` | If-Match on the policies' revision |
| Two-factor authentication… | `totp.enroll`, `totp.confirm`, `totp.disable` | The same dialog as the user menu |

Agents cannot run any of these: the `admin-only` rule of every preset forbids secrets, credentials, compute and
policies changes.

## Playbooks

- Another service moved onto the staging card: lower its memory cap, keep `training` allowed.
- A script needs to read results: create an API key scoped to that project, copy the token into the script's secret
  store, revoke the key when the script retires.
- Something changed and nobody remembers why: filter the audit log by operation; the cause column links approvals
  and agent tool calls.

## Sources

- docs/spec/11-ui-panels.md "Panel catalogue" (Settings) and "First run and progressive disclosure"; docs/spec/06-platform.md "Authentication and access"; docs/spec/08-resolutions.md R9, R11.
