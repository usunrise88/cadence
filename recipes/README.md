# recipes/ — example of a project repository after bootstrap

In production each project has its own repository (the internal bare repository served at `/git/<slug>.git`, or a
GitHub / linked repository that Cadence mirrors `main` to) created by the project wizard's bootstrap job.
`projects/hebrew/` is what that job writes for the wizard's defaults (rendered by `internal/projects/layout`; the
registry version ids in `project.yaml` and `data.lock` are examples):

- `project.yaml` — the wizard's facts; `AGENTS.md` (instructions template) and `CLAUDE.md` (`@AGENTS.md`)
- `NOTES.md` — dated learnings (`projects.note`)
- `data.lock` — every registry version the project depends on, including the templates its files came from
- `.claude/settings.json`, `opencode.json` — permissions rendered from the permission preset (no MCP section, R2)
- `.claude/skills/` — Cadence's product skills; `pipelines/` — the starter pipelines
- `lang/he-IL/` — the language pack of the project's locale (normalizer.yaml, itn.yaml, translit.yaml, lid.yaml,
  boost lists, golden-recipe.yaml, README.md), copied from Cadence's starter pack; `langpacks.edit` and `boost.edit`
  change it
- `gates.yaml` — the project's gate (target and replay golden sets, primary profile, significance). Bootstrap does not
  write it (the defaults apply until `gates.edit` commits one); the file here is an example

Data never lives here. Datasets, golden sets, models and other reusable assets are immutable versions in
the Cadence-wide registry; the project references them through adoptions and aliases, and `data.lock` records the
exact versions resolved for reproducibility.
