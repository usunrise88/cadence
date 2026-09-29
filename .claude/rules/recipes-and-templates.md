---
paths:
  - "recipes/**"
  - "control-plane/templates/**"
---
# Recipes and bootstrap templates

- `recipes/` is an example of a project repository after bootstrap; changing its layout changes what every project gets — update the wizard/bootstrap in the spec (docs/spec/02-domain-projects-registry.md) and `control-plane/templates/` together.
- `project.yaml`, `data.lock`, `lang/<locale>/`, `pipelines/*.yaml`, `augment/*.yaml`, `AGENTS.md`, `NOTES.md`: files Cadence writes are marked "written by Cadence" and must stay machine-editable (stable keys, no prose sections inside YAML).
- Templates render with Go `text/template`; every variable used must be documented in `control-plane/templates/README.md`.
- Pipeline templates reference step kinds as `name@version`; bumping a step version means bumping the template and adding a migration note.
