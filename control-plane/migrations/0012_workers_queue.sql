-- 0012 · Phase 2 stream W: the worker protocol, the step queue and telemetry (docs/review/2026-09-30-phase-2-plan.md
-- "Worker protocol", R14, R15, R19, R40). A pipeline step is a River job of kind "step"; its handler waits in
-- steps.Leases.Await, which puts the job in step_jobs; a worker claims it as a lease on one card of its host.

-- Step jobs: priority (jobs.edit) and pause (jobs.pause | jobs.resume) live on the job mirror the API shows.
ALTER TABLE jobs ADD COLUMN priority integer NOT NULL DEFAULT 0;
ALTER TABLE jobs ADD COLUMN paused_at timestamptz;

-- One worker process: a runtime on a compute host (one per runtime and host; a restart re-registers the same row).
CREATE TABLE workers (
    id                 text PRIMARY KEY,                   -- wrk_<uuidv7>
    host_id            text NOT NULL REFERENCES compute_hosts (id),
    instance           text NOT NULL DEFAULT '',
    runtime_name       text NOT NULL,
    runtime_version_id text NOT NULL REFERENCES registry_versions (id),
    runtime            jsonb NOT NULL,                     -- the RuntimeDescriptor as published
    step_kinds         text[] NOT NULL DEFAULT '{}',       -- kind@version it may lease
    credential_id      text,
    registered_at      timestamptz NOT NULL DEFAULT now(),
    last_seen_at       timestamptz NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX workers_host_runtime_idx ON workers (host_id, runtime_name);

-- The queue: one row per step job whose handler waits for a worker. The row outlives leases: a paused or
-- window-closed job goes back to waiting with its spec's resumeFrom set, keeping its place (enqueued_at).
CREATE TABLE step_jobs (
    job_id           text PRIMARY KEY REFERENCES jobs (id),
    project_id       text,
    spec             jsonb NOT NULL,                       -- steps.Spec, overrides updated on requeue
    kind_ref         text NOT NULL,                        -- kind@version
    job_kind         text NOT NULL,
    gpu              boolean NOT NULL,
    memory_mb        integer NOT NULL DEFAULT 0,
    estimate_seconds double precision,
    state            text NOT NULL DEFAULT 'waiting' CHECK (state IN ('waiting', 'leased', 'ended')),
    outcome          jsonb,                                -- steps.Outcome once ended
    enqueued_at      timestamptz NOT NULL DEFAULT now(),
    updated_at       timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX step_jobs_waiting_idx ON step_jobs (kind_ref, enqueued_at) WHERE state = 'waiting';

-- A lease: one step job on one card (or none, for a step without a GPU) of one worker. Card slots are the control
-- plane's per host and card (R40), so two runtimes on one host never double-book a card.
CREATE TABLE leases (
    id           text PRIMARY KEY,                         -- lse_<uuidv7>
    job_id       text NOT NULL REFERENCES jobs (id),
    worker_id    text NOT NULL REFERENCES workers (id),
    host_id      text NOT NULL REFERENCES compute_hosts (id),
    card_index   integer,                                  -- null: the step needs no card
    job_kind     text NOT NULL,
    memory_mb    integer NOT NULL DEFAULT 0,               -- reserved on the card and handed to the step as its cap
    state        text NOT NULL DEFAULT 'active' CHECK (state IN ('active', 'released', 'reaped')),
    stop_reason  text CHECK (stop_reason IN ('cancelled', 'paused', 'window-closed')),
    progress     double precision,
    message      text,
    outcome      jsonb,
    created_at   timestamptz NOT NULL DEFAULT now(),
    heartbeat_at timestamptz NOT NULL DEFAULT now(),
    ended_at     timestamptz
);

CREATE UNIQUE INDEX leases_active_job_idx ON leases (job_id) WHERE state = 'active';
CREATE INDEX leases_active_card_idx ON leases (host_id, card_index) WHERE state = 'active';
CREATE INDEX leases_job_idx ON leases (job_id, created_at DESC);

-- Card slots: the lock a claim takes per host and card, and the card's last telemetry (gpu topic, Queue & GPU).
CREATE TABLE card_slots (
    host_id      text NOT NULL REFERENCES compute_hosts (id),
    card_index   integer NOT NULL,
    telemetry    jsonb,                                    -- the last CardTelemetry a worker reported
    reported_at  timestamptz,
    PRIMARY KEY (host_id, card_index)
);

-- When the gpu topic last carried a host's telemetry (at most one event per 5 s per host).
CREATE TABLE gpu_events (
    host_id text PRIMARY KEY REFERENCES compute_hosts (id),
    sent_at timestamptz NOT NULL
);

-- Metric points (R15): thousands per run need no TSDB.
CREATE TABLE metric_points (
    id          bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    job_id      text NOT NULL,
    run_id      text,
    step_id     text,
    project_id  text,
    name        text NOT NULL,
    step        bigint,                                    -- optimiser step
    epoch       double precision,
    value       double precision NOT NULL,
    wall_time   timestamptz NOT NULL
);

CREATE INDEX metric_points_run_idx ON metric_points (run_id, name, step) WHERE run_id IS NOT NULL;
CREATE INDEX metric_points_job_idx ON metric_points (job_id, name, step);
