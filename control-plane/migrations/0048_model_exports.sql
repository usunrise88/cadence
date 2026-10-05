-- 0048 · Phase 5 · stream D1: model exports and their parity and benchmark checks (docs/review/2026-10-05-phase-5-plan.md;
-- docs/spec/02-domain-projects-registry.md "Deployment entities"; 03 "Export, parity and benchmark (phase 5)"; spike E1).

-- A model version never changes: an export attaches a deployable to it, one per (version, latency profile, format).
-- The export runs in a project (its GPU budget and queue priority); the row is shared by every project that uses the
-- version, so asking again for an exported (or exporting) profile answers the row and runs nothing. A failed export is
-- run again on the same row (rev + 1). deployable is what deployable.json says about serving it (server, engine,
-- memory, manifestSha256), copied when the deployable hook records the artifact.
CREATE TABLE model_exports (
    id               text PRIMARY KEY,                       -- mex_<uuidv7>
    model_version_id text NOT NULL REFERENCES registry_versions (id),
    profile          text NOT NULL CHECK (profile ~ '^[A-Za-z0-9][A-Za-z0-9._-]{0,49}$'),
    format           text NOT NULL CHECK (format ~ '^[a-z0-9][a-z0-9._-]{0,99}$'),
    state            text NOT NULL CHECK (state IN ('exporting', 'exported', 'failed')),
    deployable_hash  text CHECK (deployable_hash IS NULL OR deployable_hash ~ '^b3:[0-9a-f]{64}$'),
    deployable       jsonb,
    project_id       text NOT NULL REFERENCES projects (id),
    pipeline_run_id  text REFERENCES pipeline_runs (id),
    step             text NOT NULL DEFAULT '',               -- the step of the pipeline run that exports this profile
    error            text,
    rev              integer NOT NULL DEFAULT 1 CHECK (rev >= 1),
    created_by       jsonb NOT NULL,
    created_at       timestamptz NOT NULL DEFAULT now(),
    updated_at       timestamptz NOT NULL DEFAULT now(),
    UNIQUE (model_version_id, profile, format),
    CHECK (state <> 'exported' OR deployable_hash IS NOT NULL)
);

CREATE INDEX model_exports_run_idx ON model_exports (pipeline_run_id) WHERE pipeline_run_id IS NOT NULL;

-- A parity check or a benchmark of an export: one row per pipeline run (R31). request is what was asked (the sample,
-- the thresholds, the levels, the budget); result is the report's summary once its hook ran (the contract's
-- ModelExportParity / ModelExportBenchmark fields); the export's parity is its newest parity row, its benchmarks every
-- benchmark row, newest first. target_id is the delivery target whose concurrency a benchmark's verdict is taken at;
-- staging_target_id the server the streams ran through.
CREATE TABLE model_export_checks (
    id                text PRIMARY KEY,                      -- mxc_<uuidv7>
    export_id         text NOT NULL REFERENCES model_exports (id),
    kind              text NOT NULL CHECK (kind IN ('parity', 'benchmark')),
    project_id        text NOT NULL REFERENCES projects (id),
    pipeline_run_id   text NOT NULL UNIQUE REFERENCES pipeline_runs (id),
    state             text NOT NULL DEFAULT 'running' CHECK (state IN ('running', 'done', 'failed')),
    target_id         text,                                  -- dtg_ (deployment_targets comes in 0050: no foreign key)
    staging_target_id text,
    request           jsonb NOT NULL DEFAULT '{}',
    result            jsonb,
    report_hash       text CHECK (report_hash IS NULL OR report_hash ~ '^b3:[0-9a-f]{64}$'),
    error             text,
    created_by        jsonb NOT NULL,
    created_at        timestamptz NOT NULL DEFAULT now(),
    finished_at       timestamptz
);

CREATE INDEX model_export_checks_export_idx ON model_export_checks (export_id, kind, created_at DESC);
