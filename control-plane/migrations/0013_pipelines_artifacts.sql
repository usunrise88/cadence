-- 0013 · Phase 2 stream P: the artifact store's index and the pipeline engine (docs/spec/08-resolutions.md R11, R15;
-- docs/review/2026-09-30-phase-2-plan.md "The step job", "Pipelines"). 0012 belongs to stream W, 0014–0015 to D and O;
-- migrations apply by number and tolerate gaps.

-- One row per artifact in the content store (internal/cas), addressed by its BLAKE3 hash. The bytes live in the
-- store; a directory artifact's hash is its manifest blob, and the manifest lists the files. The row is written once,
-- by the first producer; later producers and consumers add an artifact_projects row.
CREATE TABLE artifacts (
    hash             text PRIMARY KEY CHECK (hash ~ '^b3:[0-9a-f]{64}$'),
    type             text NOT NULL CHECK (type ~ '^[a-z][a-z0-9_-]{0,62}$'),
    size             bigint NOT NULL CHECK (size >= 0),         -- a directory: the sum of its files
    directory        boolean NOT NULL DEFAULT false,
    meta             jsonb NOT NULL DEFAULT '{}',
    project_id       text REFERENCES projects (id),             -- the first producer's project; NULL: registry
    pipeline_run_id  text,                                      -- the producing step, when a step produced it
    step_id          text,
    step             text,
    output           text,
    created_at       timestamptz NOT NULL DEFAULT now()
);

-- Projects that produced or consumed an artifact: a credential scoped to one of them may read it.
CREATE TABLE artifact_projects (
    hash        text NOT NULL REFERENCES artifacts (hash),
    project_id  text NOT NULL REFERENCES projects (id),
    created_at  timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (hash, project_id)
);

CREATE INDEX artifact_projects_project_idx ON artifact_projects (project_id, created_at DESC);

-- A pipeline run: one execution of a pipeline file read at a commit (a pipeline's version is its commit SHA). The
-- parsed definition is kept so retries never re-read the repository.
CREATE TABLE pipeline_runs (
    id           text PRIMARY KEY,                              -- plr_<uuidv7>
    project_id   text NOT NULL REFERENCES projects (id),
    pipeline     text NOT NULL,
    source       text NOT NULL CHECK (source IN ('repository', 'template', 'inline')),
    ref          text NOT NULL DEFAULT '',
    commit_sha   text NOT NULL DEFAULT '',
    version      text NOT NULL DEFAULT '',
    definition   jsonb NOT NULL,
    inputs       jsonb NOT NULL DEFAULT '{}',                   -- pipeline input → ArtifactRef
    state        text NOT NULL DEFAULT 'running' CHECK (state IN ('running', 'done', 'failed', 'cancelled')),
    run_id       text,                                          -- the training run a facade started it for
    fresh        boolean NOT NULL DEFAULT false,
    priority     integer NOT NULL DEFAULT 0,
    error        text,
    actor        jsonb NOT NULL,
    rev          integer NOT NULL DEFAULT 1 CHECK (rev >= 1),
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now(),
    finished_at  timestamptz
);

CREATE INDEX pipeline_runs_project_idx ON pipeline_runs (project_id, created_at DESC);
CREATE INDEX pipeline_runs_run_idx ON pipeline_runs (run_id) WHERE run_id IS NOT NULL;

-- A step of a pipeline run. Each attempt is its own `step` job (attempt_log keeps them); job_id is the current one.
CREATE TABLE pipeline_steps (
    id                    text PRIMARY KEY,                     -- pls_<uuidv7>
    pipeline_run_id       text NOT NULL REFERENCES pipeline_runs (id),
    project_id            text NOT NULL REFERENCES projects (id),
    step                  text NOT NULL,                        -- the step id in the pipeline file
    position              integer NOT NULL,                     -- topological order
    kind                  text NOT NULL,
    kind_version          text NOT NULL,
    step_kind_version_id  text NOT NULL DEFAULT '',             -- ver_ of the step kind in the registry
    state                 text NOT NULL DEFAULT 'waiting'
                          CHECK (state IN ('waiting', 'queued', 'running', 'done', 'reused', 'failed', 'skipped', 'cancelled')),
    params                jsonb NOT NULL DEFAULT '{}',          -- resolved
    departures            jsonb NOT NULL DEFAULT '[]',          -- [{param, value, default}]
    wiring                jsonb NOT NULL DEFAULT '{}',          -- input → $inputs.x | step.output
    inputs                jsonb,                                -- input → ArtifactRef once ready
    produces              jsonb NOT NULL DEFAULT '{}',          -- output → artifact type
    outputs               jsonb,                                -- output → ArtifactRef once done
    resources             jsonb NOT NULL DEFAULT '{}',
    secret_names          text[] NOT NULL DEFAULT '{}',
    estimate_seconds      double precision,
    input_hash            text,
    reused_from           text,
    attempts              integer NOT NULL DEFAULT 0,
    attempt_log           jsonb NOT NULL DEFAULT '[]',
    job_id                text,
    error                 jsonb,
    metrics               jsonb,
    rev                   integer NOT NULL DEFAULT 1 CHECK (rev >= 1),
    created_at            timestamptz NOT NULL DEFAULT now(),
    updated_at            timestamptz NOT NULL DEFAULT now(),
    started_at            timestamptz,
    finished_at           timestamptz,
    UNIQUE (pipeline_run_id, step)
);

CREATE INDEX pipeline_steps_run_idx ON pipeline_steps (pipeline_run_id, position);
CREATE INDEX pipeline_steps_job_idx ON pipeline_steps (job_id) WHERE job_id IS NOT NULL;
-- Input-hash reuse: a finished step of the same project with the same hash.
CREATE INDEX pipeline_steps_reuse_idx ON pipeline_steps (project_id, input_hash, finished_at DESC) WHERE state = 'done';
