# internal/registry
The Cadence-wide registry: collections (`reg_<uuidv7>`, `<kind>/<name>`, tags, licence) of immutable versions
(`ver_<uuidv7>`, `YYYY-MM-DD.<first 12 hex of the content sha256>`, draft → frozen → deprecated, payload per kind) for
the kinds `base_model`, `dataset_version` and `template`. A trigger in migration 0003 keeps frozen versions unchanged.

Projects adopt versions (`projects.adopt`, bumps the project's revision) and point aliases at adopted versions
(`aliases.set`; `production` answers `reserved-alias`, `baseline` is gated — `Gated(name)`). Registry events
(`<kind>.registered` on `entity.<kind>.<ver_id>`) carry no projectId; adoption and alias events are project work.
`Seed` registers the base-model catalogue and fixture datasets (`fixtures/`) and every unit of the templates tree at
start, idempotently. `Resolve` turns `ver_…`, `@alias` or a collection name into a version.
