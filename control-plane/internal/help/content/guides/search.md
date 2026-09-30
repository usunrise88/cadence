---
title: Search and the query language
summary: One search box for projects, mixes, registry versions, jobs, approvals and help — free text plus qualifiers such as kind:, status:, tag:, updated:> and wer<10; saved searches per user per project.
contexts: [guide:search]
---

## What this is

Search finds anything Cadence has indexed with one query language, the same for people and agents: free text plus
typed qualifiers. The index is fed from the event stream, so a new project, a registered version or a finished job
is searchable within seconds. Behind it is Postgres full-text search with trigram matching, so small typos
(`fleurz` finds `fleurs`, `quoka` finds `quokka`) and identifiers (`fleurs-he-smoke`, `ver_…`) match too.

| Where | Keys | What it searches |
| --- | --- | --- |
| Palette, plain text | Ctrl/Cmd+K | Entities of the current project and the registry, grouped by kind; `>` switches to commands, `?` to help, `@` to projects |
| Library filter bar | — | The same query as a list, with saved views |
| Agent | `projects.search` tool | The same index and grammar (R1: `search.query` became `projects.search`) |

## Place in the loop

Everywhere: find the dataset version to adopt (prepare), the job that failed (run), the approval waiting for you
(decide), the help article that explains an error.

## Fields and defaults

Words without a qualifier are free text: every word must match the title, the text or the tags of a hit (a word
also matches as a prefix while you type). Put a phrase in quotes to match it as written: `"call center"`. Qualifiers
can be combined; `kind:`, `status:`, `lang:`, `actor:`, `project:` and `alias:` given twice match either value
(`kind:job kind:approval`, the same as `kind:job,approval`), while `tag:`, `updated:` and numeric comparisons must all
hold (`tag:telephony tag:fixture` needs both tags).

| Qualifier | Example | Matches |
| --- | --- | --- |
| `kind:` | `kind:dataset_version`, `kind:job,approval` | The entity kind: `project`, `base_model`, `dataset_version`, `template`, `registry_collection`, `job`, `approval`, `mix`, `help_article` (short forms `dataset`, `model`, `collection`, `help` work too) |
| `status:` / `state:` | `status:failed`, `state:frozen` | The entity's state |
| `lang:` | `lang:he` | The locale; `he` matches `he-IL` and `he-IL` matches `he` |
| `actor:` | `actor:agent`, `actor:user`, `actor:automation`, `actor:usr_admin` | Who made the last change: a kind of actor or an actor id or name |
| `updated:` | `updated:>2026-09-01`, `updated:<=2026-09-15`, `updated:2026-09-30`, `updated:2026-09-01..2026-09-10` | The last change, by date (a bare date is that whole day) |
| `project:` | `project:hebrew` | Work of that project instead of the current one (the credential must reach it) |
| `scope:` | `scope:all`, `scope:project`, `scope:registry` | `all`: every project you can reach, the registry and help; `project`: this project only; `registry`: registry assets only |
| `tag:` | `tag:telephony`, `tag:locale:he-IL` | A tag, either exactly or as the value of a `key:value` tag (`tag:telephony` matches `domain:telephony`) |
| `alias:` | `alias:@production`, `alias:baseline` | Registry versions that an alias of a project you can reach points at |
| Numbers | `wer<10`, `progress>=0.5`, `dur:2..8`, `rev=3` | Numeric fields where an entity has them: `wer`, `cer`, `dur`, `hours`, `progress`, `rev`; operators `<`, `<=`, `>`, `>=`, `=` and ranges `a..b` |

Without a scope qualifier the palette and the Library search the current project, the registry and help. An unknown
qualifier (`colour:red`) or an unknown numeric field (`foo<3`) is not treated as text: the search answers
[invalid-query](../errors/invalid-query.md) with the list of qualifiers, and the palette shows that message under the box.

What you can see follows your credential, never the query:

- A project-scoped API key or an agent session token sees its own project only; `scope:all` and `project:<other>`
  never reach another project's work (`project:<other>` answers forbidden).
- Registry hits need registry read; without it they are left out.
- Instance-wide entries (approvals and jobs without a project) are the admin's.

Text is normalised the same way when indexed and when searched: case is folded; for Hebrew the niqqud and cantillation
marks are stripped and a short list of spelling variants (ktiv male and ktiv haser, for example תוכנית / תכנית) is
folded to one form, so a phrase is found however it was written. The list is a stub that grows with the Hebrew
language pack.

## Commands

- Palette results are grouped by kind, this project's newest first. **Enter** opens the hit: a document when its kind
  has one (projects), the Help panel for articles, otherwise the Inspector. **Space** previews the highlighted hit in
  the Inspector without closing the palette — once you have moved the highlight with the arrow keys; typing resets
  that, so a space inside the query still types a space.
- **Open as list** (first palette entry while you type) turns the search into a Library list; there Enter opens and
  Space previews as in every list.
- **Save view** in the Library stores the current query under a name (`views.set`, per user per project). Saved views
  appear as chips in the Library and under "Saved searches" in the palette; choosing one opens it in the Library.
  Saving under an existing name replaces that view's query.
- Agents call `projects.search` with `q`; `views.*` are yours only and are not agent tools.

## Playbooks

- **Find what failed today:** `kind:job status:failed updated:>2026-09-30`.
- **Frozen Hebrew telephony data:** `kind:dataset_version state:frozen lang:he tag:telephony`.
- **What the agents touched:** `actor:agent updated:>2026-09-29`, then Save view as "Agent changes".
- **Everything in the registry named like a model:** `nemotron scope:registry`.

## Sources

- PostgreSQL documentation, Full Text Search and the `pg_trgm` module (trigram similarity).
- Unicode Standard, Hebrew block (U+0591–U+05C7: cantillation marks and points).
- Cadence recommendation — docs/spec/11-ui-panels.md "Search"; docs/spec/08-resolutions.md R1.
