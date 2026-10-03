-- 0027 · Phase 3 wave 2 stream X: experiments and sweeps (docs/spec/04-blocks.md "Experiments and sweeps";
-- docs/review/2026-10-02-phase-3-plan.md). An experiment groups the runs that answer one question on a fixed mix
-- revision and base model; a sweep generates those runs from a grid or a random draw over recipe parameters and
-- runs them one after another under a GPU-hour cap.

CREATE TABLE experiments (
    id               text PRIMARY KEY,                          -- exp_<uuidv7>
    project_id       text NOT NULL REFERENCES projects (id),
    name             text NOT NULL,
    question         text NOT NULL,
    tag              text NOT NULL DEFAULT '',
    mix_id           text NOT NULL REFERENCES mixes (id),
    mix_rev          integer NOT NULL CHECK (mix_rev >= 1),      -- the fixed mix revision every run trains on
    base_version_id  text NOT NULL REFERENCES registry_versions (id),
    actor            jsonb NOT NULL,
    rev              integer NOT NULL DEFAULT 1 CHECK (rev >= 1),
    created_at       timestamptz NOT NULL DEFAULT now(),
    updated_at       timestamptz NOT NULL DEFAULT now(),
    UNIQUE (project_id, name)
);

CREATE INDEX experiments_project_idx ON experiments (project_id, created_at DESC);

-- A sweep: its points (parameter values, the estimate each had when planned, and the run once started) and its
-- cursor. The recipe is read at one commit for every run (recipe_ref).
CREATE TABLE sweeps (
    id                  text PRIMARY KEY,                       -- swp_<uuidv7>
    experiment_id       text NOT NULL REFERENCES experiments (id),
    project_id          text NOT NULL REFERENCES projects (id),
    mode                text NOT NULL CHECK (mode IN ('grid', 'random')),
    parameters          jsonb NOT NULL,                         -- the request's SweepParameter list
    seed                integer,
    steps               integer,
    priority            integer NOT NULL DEFAULT 0,
    gpu_hour_cap        double precision NOT NULL CHECK (gpu_hour_cap > 0),
    recipe_ref          text NOT NULL DEFAULT '',
    points              jsonb NOT NULL,                         -- [{index, values, estimateGpuHours, runId?}]
    current_run_id      text,
    estimate_gpu_hours  double precision NOT NULL DEFAULT 0,
    state               text NOT NULL DEFAULT 'running'
                        CHECK (state IN ('running', 'done', 'stopped', 'cancelled', 'failed')),
    stop_reason         text,
    actor               jsonb NOT NULL,
    rev                 integer NOT NULL DEFAULT 1 CHECK (rev >= 1),
    created_at          timestamptz NOT NULL DEFAULT now(),
    updated_at          timestamptz NOT NULL DEFAULT now(),
    finished_at         timestamptz
);

CREATE INDEX sweeps_experiment_idx ON sweeps (experiment_id, created_at DESC);
-- Runs of sweeps queue one after another on the project's training slot: one running sweep per project.
CREATE UNIQUE INDEX sweeps_running_idx ON sweeps (project_id) WHERE state = 'running';

-- A run created for an experiment (runs.new with experiment, or a sweep) carries it.
ALTER TABLE runs ADD COLUMN experiment_id text REFERENCES experiments (id);
ALTER TABLE runs ADD COLUMN sweep_id text REFERENCES sweeps (id);
CREATE INDEX runs_experiment_idx ON runs (experiment_id, created_at) WHERE experiment_id IS NOT NULL;
