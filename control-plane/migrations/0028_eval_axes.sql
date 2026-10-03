-- 0028 · Phase 3 stream R: the robustness axis of an eval and the metrics beside WER (docs/spec/03-pipelines-defaults.md
-- "Augmentation", "Scorers and metrics"; docs/spec/04-blocks.md "Task and streaming metrics"; R54).
--
-- The eval-record key does not change: an augmented cell's decoding hash covers the augmentation (augment_dataset's
-- kind@version, the profile's content hash and the seed), so a record computed without augmentation keeps its key and
-- stays valid as "none". The columns below only describe the axis for reading.

-- The augmentations an eval ran (EvalAugmentation[]): index 0 is always none.
ALTER TABLE evals ADD COLUMN augmentations jsonb NOT NULL DEFAULT '[{"index": 0, "profile": "none"}]';

-- A cell's augmentation, and the metric steps planned for it: {"entities": {"scorer", "config", "step"} |
-- {"unavailable": reason}, "latency": …}.
ALTER TABLE eval_cells ADD COLUMN augmentation_index integer NOT NULL DEFAULT 0;
ALTER TABLE eval_cells ADD COLUMN metrics jsonb;

-- The augmentation a record was scored under ({index, profile, name, hash, seed}); NULL is none.
ALTER TABLE eval_records ADD COLUMN augmentation jsonb;

-- A metric scored beside an eval record (entity accuracy, latency to final): shared by every project like the records,
-- keyed by the record's model, golden set and decoding hash, the scorer's kind@version and the configuration it read
-- (the ITN artifact's hash, the VAD step kind). The scoring normalizer does not enter: these scorers do not use it.
CREATE TABLE eval_metrics (
    id                     text PRIMARY KEY,                    -- erm_<uuidv7>
    model_key              text NOT NULL,
    golden_set_version_id  text NOT NULL REFERENCES registry_versions (id),
    decoding_hash          text NOT NULL,
    scorer                 text NOT NULL,                       -- kind@version of the metric scorer
    config                 text NOT NULL DEFAULT '',            -- what else it read: itn b3 hash, vad kind
    metric                 text NOT NULL,                       -- entities | latency
    scores_hash            text NOT NULL REFERENCES artifacts (hash),
    summary                jsonb NOT NULL,                      -- the metric_scores artifact's summary.json
    project_id             text REFERENCES projects (id),
    eval_id                text,
    pipeline_run_id        text,
    step_id                text,
    created_at             timestamptz NOT NULL DEFAULT now(),
    UNIQUE (model_key, golden_set_version_id, decoding_hash, scorer, config)
);

CREATE INDEX eval_metrics_golden_set_idx ON eval_metrics (golden_set_version_id);
