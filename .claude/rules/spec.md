---
paths:
  - "docs/spec/**"
  - "docs/help/**"
---
# Spec and help rules

- `docs/spec/*.md` is an export of the Claude Doc "Cadence — spec v0.2". When you change behaviour, update the relevant file here and say so in the PR; the Doc is re-synced from these files.
- Decisions go in the decision log table in `docs/spec/00-overview.md`: date, decision, why. No decision without a why.
- Keep the consistency matrix in `docs/spec/04-blocks.md` true: a new step needs its window, command, API operation, tool and event in the same row.
- Help articles (`docs/help/`) follow one shape: what this is · place in the loop · fields and defaults (generated) · commands · playbooks · sources. Every panel, step kind and error type has one; CI fails otherwise.
- Cite sources for claims that come from papers, standards or vendor docs; mark everything else "Cadence recommendation".
