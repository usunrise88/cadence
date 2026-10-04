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

Reference alignments (phase 4 · stream L, `alignments.go`, migration 0039): an aligning step (`align_reference`, omni
runtime) writes an artifact of type `alignment` (`cadence.alignment/1`); `AlignmentHook` records it in
`reference_alignments` against the step's one dataset input, from the output meta only (format, aligner, counts,
reasons), and announces `golden_set.aligned` on every golden set built on that dataset artifact. A golden set version
stays immutable: `Alignments`/`LatestAlignment` give the newest alignment of a dataset artifact, which
`goldenSets.get|list` show as `alignment` and `internal/evals` feeds to the latency scorer (emission delay) for
unaugmented cells, its artifact hash part of the metric's configuration.
