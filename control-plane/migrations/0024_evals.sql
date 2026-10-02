-- 0024 · Phase 3 stream E: evals, eval records and the gate verdict (docs/spec/04-blocks.md Block 3,
-- docs/spec/08-resolutions.md R20–R24, R54; docs/review/2026-10-02-phase-3-plan.md "Decisions" 4, "Gate").
-- An eval is a facade over one generated pipeline run (materialize → transcribe → score per missing cell); its cells
-- link to eval records, the global cache of scored cells that every project reuses.

-- One scored cell, shared by every project: a model (its weights hash, or base:<versionId> for a base model) on one
-- golden set version, scored after one normalizer version, decoded with one configuration, by one scorer version.
CREATE TABLE eval_records (
    id                     text PRIMARY KEY,                    -- erc_<uuidv7>
    model_key              text NOT NULL,
    golden_set_version_id  text NOT NULL REFERENCES registry_versions (id),
    normalizer_version_id  text NOT NULL REFERENCES registry_versions (id),
    decoding_hash          text NOT NULL,                       -- sha256:<hex> of the canonical decoding config
    scorer                 text NOT NULL,                       -- kind@version of the scorer step
    profile                text NOT NULL,                       -- latency profile (R43), also inside the decoding hash
    decoding               jsonb NOT NULL DEFAULT '{}',         -- the decoding config the hash covers
    scores_hash            text NOT NULL REFERENCES artifacts (hash),
    hypotheses_hash        text REFERENCES artifacts (hash),
    summary                jsonb NOT NULL,                      -- the scores artifact's summary.json
    family                 text NOT NULL DEFAULT '',
    project_id             text REFERENCES projects (id),       -- the project whose eval computed it (provenance only)
    eval_id                text,                                -- the eval that computed it
    pipeline_run_id        text,
    step_id                text,
    created_at             timestamptz NOT NULL DEFAULT now(),
    UNIQUE (model_key, golden_set_version_id, normalizer_version_id, decoding_hash, scorer)
);

CREATE INDEX eval_records_golden_set_idx ON eval_records (golden_set_version_id);

-- A project's eval: a subject against a baseline over golden sets × profiles × decoding variants.
CREATE TABLE evals (
    id                text PRIMARY KEY,                         -- evl_<uuidv7>
    project_id        text NOT NULL REFERENCES projects (id),
    status            text NOT NULL DEFAULT 'queued' CHECK (status IN ('queued', 'running', 'done', 'failed')),
    error             text,
    subject           jsonb NOT NULL,                           -- EvalModel
    subject_id        text NOT NULL,                            -- ckp_… or ver_… (filters, models.register)
    baseline          jsonb NOT NULL,                           -- EvalModel
    golden_sets       jsonb NOT NULL,                           -- [EvalGoldenSet]
    profiles          jsonb NOT NULL,                           -- [EvalProfile]
    primary_profile   text NOT NULL DEFAULT '',
    decoding          jsonb NOT NULL,                           -- [EvalDecoding]
    significance      jsonb NOT NULL,                           -- {samples, level, seed}
    estimate          jsonb NOT NULL,                           -- EvalEstimate
    pipeline_run_id   text REFERENCES pipeline_runs (id),
    gate              jsonb,                                    -- EvalGate once evals.gate ran
    gated_at          timestamptz,
    actor             jsonb NOT NULL,
    rev               integer NOT NULL DEFAULT 1 CHECK (rev >= 1),
    created_at        timestamptz NOT NULL DEFAULT now(),
    updated_at        timestamptz NOT NULL DEFAULT now(),
    finished_at       timestamptz
);

CREATE INDEX evals_project_idx ON evals (project_id, created_at DESC);
CREATE INDEX evals_subject_idx ON evals (subject_id, gated_at DESC);
CREATE UNIQUE INDEX evals_pipeline_run_idx ON evals (pipeline_run_id) WHERE pipeline_run_id IS NOT NULL;

-- One cell of an eval: a role's model on a golden set at a profile and decoding variant. Cells with the same record
-- key share one score step (a subject equal to its baseline computes once).
CREATE TABLE eval_cells (
    id                     text PRIMARY KEY,                    -- evc_<uuidv7>
    eval_id                text NOT NULL REFERENCES evals (id),
    position               integer NOT NULL,
    role                   text NOT NULL CHECK (role IN ('subject', 'baseline')),
    golden_set_version_id  text NOT NULL,
    normalizer_version_id  text NOT NULL,
    profile                text NOT NULL,
    decoding_index         integer NOT NULL,
    decoding_hash          text NOT NULL,
    model_key              text NOT NULL,
    scorer                 text NOT NULL,
    state                  text NOT NULL CHECK (state IN ('cached', 'queued', 'running', 'done', 'failed')),
    record_id              text REFERENCES eval_records (id),
    score_step             text,                                -- the pipeline step that scores it (missing cells)
    delta                  jsonb,                               -- EvalDelta (subject cells, once both sides are scored)
    UNIQUE (eval_id, position)
);

CREATE INDEX eval_cells_eval_idx ON eval_cells (eval_id, position);
CREATE INDEX eval_cells_step_idx ON eval_cells (eval_id, score_step) WHERE score_step IS NOT NULL;
