# internal/goldensets
Golden sets (phase 3 · stream G; docs/spec/04-blocks.md Block 3, spec 02 "Evaluation entities"). A golden set is a
registry version of kind `golden_set` (`golden-set/<name>`) tying a frozen dataset version registered eval-only to one
scoring normalizer version (kind `normalizer`, seeded by `internal/registry`). `Prepare` resolves and checks a
`goldenSets.freeze` (`golden-set-not-eval-only`, `normalizer-unknown`, `validation-failed` for locale, groups and
name, `golden-set-leakage` against every dataset version not registered eval-only and every one any project trained
on); `Freeze` registers it and the `golden_sets` row in the approved command's transaction (event
`golden_set.frozen` on `entity.golden_set.<ver_id>`); the same content again answers the existing version.
`CheckAdoption` re-runs the leakage check when a project adopts a golden set, against `TrainedDatasets` (its runs' mix
revisions and its pipelines' training-step inputs). The policy preset gates the freeze for everyone (rule
`golden-set-freeze`): an approval at registry scope that only the admin decides.
