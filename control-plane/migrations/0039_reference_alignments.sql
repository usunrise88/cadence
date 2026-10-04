-- 0039 · Phase 4 stream L: word timings of reference texts (docs/review/2026-10-03-phase-4-plan.md decision 8; R51,
-- R54). An align_reference step aligns a dataset's references and writes an alignment artifact; its output hook keeps
-- one row per (dataset artifact, alignment artifact). A golden set carries the newest alignment of its dataset
-- artifact (goldenSets.get alignment); the eval pipeline feeds it to latency_score for emission delay. The golden set
-- version itself stays immutable: an alignment is attached by content hash, and a newer one (another aligner version)
-- simply becomes the newest. Rows are never updated.
CREATE TABLE reference_alignments (
    id                 text PRIMARY KEY,                     -- aln_<uuidv7>
    dataset_hash       text NOT NULL,                        -- the dataset artifact aligned (b3:…)
    artifact_hash      text NOT NULL,                        -- the alignment artifact (b3:…, cadence.alignment/1)
    aligner_version_id text NOT NULL DEFAULT '',             -- the auxiliary version (role align) the step read
    aligner            text NOT NULL DEFAULT '',             -- its collection name, auxiliary/<name>
    method             text NOT NULL DEFAULT '',             -- torchaudio.forced_align | ctc-viterbi | '' (nothing aligned)
    utterances         integer NOT NULL,
    aligned            integer NOT NULL,
    words              integer NOT NULL,
    reasons            jsonb NOT NULL DEFAULT '[]',          -- why utterances stayed unaligned (distinct, at most five)
    project_id         text,
    pipeline_run_id    text NOT NULL,
    step_id            text NOT NULL,
    created_at         timestamptz NOT NULL DEFAULT now(),
    UNIQUE (dataset_hash, artifact_hash)
);

CREATE INDEX reference_alignments_dataset_idx ON reference_alignments (dataset_hash, created_at DESC);
