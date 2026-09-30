---
title: Invalid search query
summary: The search query has an unknown qualifier, a malformed value or an unclosed quote; the detail lists every qualifier.
contexts: [error:invalid-query, field:q]
---

## What this is

A `400 Bad Request` problem of type `invalid-query`: `projects.search` (or `views.set`, which checks the query it
saves) could not parse the query. Cadence never guesses: a token that looks like a qualifier — a name followed by
`:` or a comparison such as `<` — must name a known field, so a typo like `knd:run` fails here instead of quietly
searching for the text "knd:run".

Typical causes:

| You wrote | Why it fails | Write instead |
| --- | --- | --- |
| `knd:job` | unknown qualifier | `kind:job` |
| `kind:runz` | not a searchable kind (the detail lists the kinds) | `kind:job` |
| `foo<3` | unknown numeric field | `wer<3`, `progress<0.5` |
| `updated:>last week` | dates are `YYYY-MM-DD` or RFC 3339 | `updated:>2026-09-01` |
| `scope:everything` | scope is `all`, `project` or `registry` | `scope:all` |
| `"call center` | the quote is not closed | `"call center"` |
| `http://example.com` | `http:` reads as a qualifier | `"http://example.com"` (quoted text is always text) |

## Place in the loop

Search is how people and agents find what exists before acting — a frozen dataset version, a failed job, an
approval waiting. The query language is the same in the palette, the Library filter bar, saved searches and the
agent's `projects.search` tool. Nothing was searched or saved.

## Fields and defaults

| Field | Meaning |
| --- | --- |
| `type` | `https://cadence.local/help/errors/invalid-query` |
| `status` | `400` |
| `detail` | What is wrong, then `Qualifiers:` and the full list (with the numeric fields) |

## Commands

- `projects.search` — search with free text and qualifiers.
- `views.set` — save a query under a name (it is parsed first).
- The query language reference: help article `guides.search`.

## Playbooks

- Agents: read the qualifier list in `detail`, fix the one token it names and retry once; do not retry unchanged.
- To search for text that contains a colon or `<`, put it in double quotes.

## Sources

- docs/spec/11-ui-panels.md "Search" — query language.
- RFC 9110, HTTP Semantics, §15.5.1 400 Bad Request; RFC 9457, Problem Details for HTTP APIs.
