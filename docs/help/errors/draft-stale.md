---
title: Draft is stale
summary: The entity moved on after the draft was made, so accepting it would undo the newer revision. currentRev is the entity's revision now; revert the draft or have its author redo the edit.
contexts: [error:draft-stale]
---

## What this is

A `412` problem answered by `drafts.accept` when the draft's base revision (`baseRev`) is no longer the entity's
current revision: someone — a person, or another agent's accepted draft — changed the entity after the agent made
its draft. Accepting it anyway would overwrite that change, so Cadence refuses. `currentRev` is the entity's
revision now. The draft stays open and is marked `stale: true` in `drafts.list` and `drafts.get`.

This differs from `precondition-failed`, which concerns your own `If-Match`: there the draft (or the entity) you
read is out of date; here the draft itself is.

## Place in the loop

Decide: accepting drafts is a person's decision (docs/spec/06-platform.md "Real-time model", "Drafts" and
"Concurrency"). A losing change is never applied silently; it waits for someone to resolve it.

## Fields and defaults

| Field | Meaning |
| --- | --- |
| `type` | `https://cadence.local/help/errors/draft-stale` |
| `status` | `412` |
| `currentRev` | The entity's revision now (the draft is based on an older one) |
| `detail` | The entity kind and both revisions |

## Commands

- `drafts.revert` — discard the stale draft (Revert on the draft's outline).
- Ask the agent to redo the edit: its next `mixes.edit` with the current revision in `ifMatch` carries the draft over
  to that revision (the fields it changed win, the rest come from the current revision), and it can be accepted.
- `drafts.get` — read the draft's `changes` against its base to decide.

## Playbooks

- Mix panel: the draft's outline shows "stale — based on rev N, the mix is at rev M" with Revert, and Accept is
  disabled with that reason.
- Agents: after a person's edit, read the mix again (`mixes.get`) and repeat the edit with the new ETag.

## Sources

- RFC 9110, HTTP Semantics, §15.5.13 412 Precondition Failed.
- Cadence recommendation — docs/spec/08-resolutions.md R13 (mixes are draftable).
