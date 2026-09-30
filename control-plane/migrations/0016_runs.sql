-- 0016 · Phase 2 wave 2 stream R: training runs, checkpoints and measured estimates (docs/spec/04-blocks.md Block 2,
-- docs/spec/08-resolutions.md R12, R13, R44). A run is a facade over one pipeline run of its recipe (the train-stage
-- pipeline at a commit); its status mirrors that pipeline run. Checkpoints come from the `checkpoint` output hook,
-- measured seconds per step from the `calibration` hook.

-- One optimisation stage: a pinned start (base model version, or a checkpoint for init = checkpoint), a mix revision
-- with the content hash of its rendered input_cfg, a recipe at a commit, a step budget and a seed.
CREATE TABLE runs (
    id                  text PRIMARY KEY,                       -- run_<uuidv7>
    project_id          text NOT NULL REFERENCES projects (id),
    init                text NOT NULL CHECK (init IN ('base', 'checkpoint')),
    base_version_id     text NOT NULL REFERENCES registry_versions (id),
    checkpoint_id       text,                                   -- init checkpoint: the start (ckp_…)
    parent_run_id       text REFERENCES runs (id),              -- the run the start checkpoint belongs to
    family              text NOT NULL,                          -- the base model's model family (R41)
    family_version_id   text NOT NULL REFERENCES registry_versions (id),
    mix_id              text NOT NULL REFERENCES mixes (id),
    mix_rev             integer NOT NULL,
    mix_name            text NOT NULL,
    mix_hash            text NOT NULL,                          -- the rendered mix artifact (b3:…)
    recipe              jsonb NOT NULL,                         -- {pipeline, source, ref, commit, version}
    pipeline_run_id     text NOT NULL REFERENCES pipeline_runs (id),
    train_step          text NOT NULL,                          -- the step that fills the family's train role
    steps               integer NOT NULL CHECK (steps >= 1),
    seed                integer,
    gpus                integer NOT NULL DEFAULT 1 CHECK (gpus >= 1),
    precision           text NOT NULL,
    params              jsonb NOT NULL DEFAULT '{}',            -- the train step's overrides as requested
    runtime             jsonb NOT NULL DEFAULT '{}',            -- {name, versionId, digest} of the train step's runtime
    card                jsonb NOT NULL DEFAULT '{}',            -- the card the estimate was made for
    estimate            jsonb,                                  -- the RunEstimate at start
    status              text NOT NULL DEFAULT 'queued'
                        CHECK (status IN ('queued', 'running', 'paused', 'done', 'failed', 'cancelled')),
    error               text,
    resumed_from        text,                                   -- the training-state the last runs.resume continued from
    actor               jsonb NOT NULL,
    rev                 integer NOT NULL DEFAULT 1 CHECK (rev >= 1),
    created_at          timestamptz NOT NULL DEFAULT now(),
    updated_at          timestamptz NOT NULL DEFAULT now(),
    finished_at         timestamptz
);

CREATE INDEX runs_project_idx ON runs (project_id, created_at DESC);
CREATE UNIQUE INDEX runs_pipeline_run_idx ON runs (pipeline_run_id);

-- A checkpoint of a run: a neutral `checkpoint` artifact with {step, valWer, family, weightsHash} (R42). The hook runs
-- for reused outputs too, so a checkpoint is unique per run and artifact.
CREATE TABLE checkpoints (
    id               text PRIMARY KEY,                          -- ckp_<uuidv7>
    run_id           text NOT NULL REFERENCES runs (id),
    project_id       text NOT NULL REFERENCES projects (id),
    artifact_hash    text NOT NULL REFERENCES artifacts (hash),
    kind             text NOT NULL CHECK (kind IN ('trained', 'averaged')),
    step             bigint,
    val_wer          double precision,
    family           text NOT NULL DEFAULT '',
    weights_hash     text NOT NULL DEFAULT '',
    averaged_from    text[] NOT NULL DEFAULT '{}',              -- ckp_ ids an averaged checkpoint was made from
    kept             boolean NOT NULL DEFAULT true,             -- in the run's top k by validation WER
    rank             integer,
    pipeline_run_id  text,
    step_id          text,
    meta             jsonb NOT NULL DEFAULT '{}',
    created_at       timestamptz NOT NULL DEFAULT now(),
    UNIQUE (run_id, artifact_hash)
);

CREATE INDEX checkpoints_project_idx ON checkpoints (project_id, val_wer NULLS LAST, created_at);

-- Measured seconds per step (R12): the newest calibration per (base model collection, card class, memory cap,
-- precision) answers estimates with basis measured. Rows are kept per bucket configuration.
CREATE TABLE calibrations (
    base_model        text NOT NULL,                            -- registry collection name
    card_class        text NOT NULL,
    memory_cap_gb     double precision NOT NULL,
    precision         text NOT NULL,
    bucket_config     text NOT NULL DEFAULT '',
    seconds_per_step  double precision NOT NULL CHECK (seconds_per_step > 0),
    plus_minus        double precision NOT NULL CHECK (plus_minus >= 0),
    batch_sizes       jsonb NOT NULL DEFAULT '{}',
    family            text NOT NULL DEFAULT '',
    artifact_hash     text NOT NULL,
    pipeline_run_id   text,
    measured_at       timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (base_model, card_class, memory_cap_gb, precision, bucket_config)
);

-- What a runs.calibrate pipeline run measures for: the calibration hook reads the key from here (the calibrate step
-- itself need not know the base model's collection or the card class).
CREATE TABLE calibration_requests (
    pipeline_run_id  text PRIMARY KEY REFERENCES pipeline_runs (id),
    project_id       text NOT NULL REFERENCES projects (id),
    base_model       text NOT NULL,
    card_class       text NOT NULL,
    memory_cap_gb    double precision NOT NULL,
    precision        text NOT NULL,
    created_at       timestamptz NOT NULL DEFAULT now()
);

-- The spend meter reads GPU-hours from leases on cards per project and day and per agent session.
CREATE INDEX leases_card_time_idx ON leases (created_at) WHERE card_index IS NOT NULL;
