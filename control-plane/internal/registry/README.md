# internal/registry
The Cadence-wide registry: collections (`reg_<uuidv7>`, `<kind>/<name>`, tags, licence) of immutable versions
(`ver_<uuidv7>`, `YYYY-MM-DD.<first 12 hex of the content sha256>`, draft → frozen → deprecated, payload per kind) for
the kinds `base_model`, `dataset_version` and `template`. A trigger in migration 0003 keeps frozen versions unchanged.

Projects adopt versions (`projects.adopt`, bumps the project's revision) and point aliases at adopted versions
(`aliases.set`; `production` answers `reserved-alias`, `baseline` is gated — `Gated(name)`). Registry events
(`<kind>.registered` on `entity.<kind>.<ver_id>`) carry no projectId; adoption and alias events are project work.
`Seed` registers the base-model catalogue, fixture datasets and the scoring normalizers `normalizer/basic` and
`normalizer/he-il` (`fixtures/`) and every unit of the templates tree at
start, idempotently. `Resolve` turns `ver_…`, `@alias` or a collection name into a version.

Phase 4 (stream R, migration 0036): `Adopt` takes a purpose (`target` | `replay`) and runs `CheckLicence` (unusable
licence, `outputsCommercialUse: false`, non-commercial or no-derivatives on what is trained on or shipped:
`licence-forbids-adoption`) and `CheckLocale` (dataset versions, golden sets, normalizers, auxiliary models in none of
the project's languages, unless replay: `locale-mismatch`); worker-published kinds (`Published`) are never adopted.
`Resolve` turns a collection name into the project's adopted version (what `data.lock` lists; `Adopted`) before the
newest frozen one. `Archive` is the soft delete (state `archived`, terminal; `InUse` lists what blocks it:
`version-in-use`); `Filter.HideArchived` keeps archived versions out of `registry.search`.
