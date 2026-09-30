---
title: Merge conflict
summary: A branch cannot be merged into main because both changed the same lines; main was left exactly as it was.
contexts: [error:merge-conflict, panel:recipe]
---

## What this is

A `409 Conflict` problem of type `merge-conflict`: merging a branch of the project repository into `main` — a
template sync branch (`branches.accept`) or an agent session's branch (`agentSessions.accept`) — found files that
both the branch and `main` changed in the same place since the branch left `main`. Cadence merges with git's own
three-way merge, computed without touching any working tree, so nothing was written: `main`, the branch and every
worktree are as they were. The `detail` names the conflicting files.

## Place in the loop

Agent sessions and template syncs change the repository on their own branches; people edit `main` from the UI.
Their changes meet only at merge (docs/spec/05-agents.md "Worktree, drafts and merge"). A clean merge applies on its
own — a fast-forward when `main` has not moved, otherwise a merge commit. A conflict waits for a person.

## Fields and defaults

| Field | Meaning |
| --- | --- |
| `type` | `https://cadence.local/help/errors/merge-conflict` |
| `status` | `409` |
| `detail` | The branch and the files it conflicts on |

`branches.get` shows the same conflicts before you try (`conflicts`), with the branch's diff against `main`;
`branches.compare` gives, per conflicting file, the merge base, `main` and branch texts and the conflict hunks (the
three-way view in Chat's Session changes and the Recipe document).

## Commands

- `branches.get` — the branch's diff against `main` and the files a merge would conflict on.
- `branches.compare` — the three versions of every conflicting file and the hunks where both sides changed it.
- `branches.revert` — discard a sync branch; run `projects.sync` again to get a fresh one on top of today's `main`.
- `agentSessions.revert` — discard a session's changes (Agent sessions stream).
- `recipes.get` — read either side of a conflicting file (`ref=main` or `ref=<branch>`).

## Playbooks

- A sync branch conflicts when someone edited a template-owned file (a skill, a starter pipeline, the agent config)
  on `main`. Either discard the sync branch and keep your edit, or move your edit elsewhere and sync again.
- A session branch conflicts when `main` changed the same recipe during the session. Open Three-way in the Chat's
  Session changes (or the branch in the Recipe panel), compare the versions, then resume the session with the
  resolution as its next instruction or discard its changes.
- Agents: do not retry the merge; read `branches.compare` for the conflicting hunks, report them to the person and
  wait.

## Sources

- docs/spec/05-agents.md "Worktree, drafts and merge"; docs/spec/08-resolutions.md R1 (`accept` / `revert`), R10.
- git-merge-tree(1), `--write-tree`: a merge computed without a working tree (git 2.38+).
- S. Khanna, K. Kunal, B. C. Pierce, "A Formal Investigation of Diff3", FSTTCS 2007: the hunk layout of the
  three-way view (whether a file conflicts is git's decision).
- RFC 9110, HTTP Semantics, §15.5.10 409 Conflict; RFC 9457, Problem Details for HTTP APIs.
