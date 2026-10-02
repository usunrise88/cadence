---
title: Language pack
summary: One locale of the project (lang/<locale>/) — its files with a checked YAML editor, the scoring normalizer it references, and its boost lists with weights.
contexts: [panel:language-pack, command:langpacks.get, command:langpacks.edit, command:boost.edit]
---

## What this is

A document for one **language pack**, the project repository's `lang/<locale>/` directory on main
(`langpacks.get`). Bootstrap copies Cadence's starter packs (he-IL, sr); the pack is the project's to change.

- **Scoring normalizer**: `normalizer.yaml` names a registry normalizer (`normalizer/<name>` or a pinned `ver_…`);
  the line shows the version it resolves to now. Scoring rules live in the registry so WER stays comparable (R21);
  the pack's own files hold the training text style, inverse normalisation, transliteration and the like.
- **Files**: every file of the pack; select one to read it, **Edit** to change it. **Check** runs the server's dry run
  of `langpacks.edit` (the pack's shape, the referenced normalizer); **Commit to main** is offered for exactly the
  text that passed the check. The commit carries the pack's commit as If-Match: if the pack changed meanwhile, nothing
  is committed and the editor asks you to reload.
- **Boost lists** (`boost/<domain>.txt`): terms the decoder favours, with a weight. **Edit** or **New list…** opens
  the list editor — one term per line (blank lines, `#` comments and duplicates are dropped), the weight (empty keeps
  the list's own, else `langpacks.boost_weight`). Check, then Commit, through `boost.edit`. Each list's short hash is
  the identity an eval's decoding variant names.
- An agent's edit under the draft policy lands on a branch; the document says which, and it is accepted like any
  session branch.

**"Test a phrase" is not built.** The spec's box (one phrase decoded with and without the list) does not exist in this
document yet. Meanwhile: in the Transcription panel, open two targets that differ only in their boost list and speak
or play the phrase; or evaluate the list as a decoding variant (Run eval… or `evals.new` with `decoding`) and compare
the boosted and unboosted cells of the matrix.

## Place in the loop

Data and Evaluation · Prepare: the text rules and hot words both training and evaluation read.

## Fields and defaults

| Field | Default | Meaning |
| --- | --- | --- |
| Boost weight | the list's own, else `langpacks.boost_weight` | Editor accepts 0 < weight ≤ 10 |
| List name | — | Lower-case letters, digits and hyphens: `boost/<name>.txt` |

## Commands

| Command | API | Notes |
| --- | --- | --- |
| Edit language pack | `langpacks.edit` | Check (dry run) first; If-Match is the pack's commit |
| Edit boost list | `boost.edit` | Same; creates the list when it is new |

## Playbooks

- **Boost product names.** New list…, name it `names`, paste one name per line, Check, Commit; then evaluate the
  checkpoint with `decoding: [{boost: none}, {boost: lang/<locale>/boost/names.txt@<commit>}]` and compare the two
  matrices in the Eval report.

## Sources

- docs/spec/11-ui-panels.md "Panel catalogue" (Language pack); docs/spec/03-pipelines-defaults.md "Language packs and
  hot words"; R21, R24 in docs/spec/08-resolutions.md.
