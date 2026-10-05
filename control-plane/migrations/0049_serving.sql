-- 0049 · Phase 5 · stream D2: staging serving (docs/spec/06-platform.md "Staging serving", R30; plan
-- docs/review/2026-10-05-phase-5-plan.md). The tables name deployment targets by id without a foreign key: the targets
-- table comes with 0050 (stream D3), and a fresh database applies 0049 first.

-- The staging server's health as the control plane's periodic check found it (serving.health_check_seconds). One row
-- per staging target; since is when the state last changed (the event entity.deployment_target.{id} carries changes).
CREATE TABLE serving_health (
    target_id  text PRIMARY KEY,                     -- dtg_… (deployment_targets, 0050)
    state      text NOT NULL CHECK (state IN ('unknown', 'up', 'down')),
    detail     text NOT NULL DEFAULT '',
    latency_ms integer,
    checked_at timestamptz NOT NULL,
    since      timestamptz NOT NULL
);

-- Models the serve steps loaded on a staging target, by their versioned name on the server (cadence-<16 hex of the
-- deployable's hash>). A row is written when a lease that serves the model is granted; the idle unload sets
-- unloaded once no active lease has used it for serving.unload_idle_minutes. last_used_at is the newest grant; the
-- newest end of its leases is read from serving_leases ⋈ leases.
CREATE TABLE serving_models (
    target_id       text NOT NULL,
    model           text NOT NULL,
    deployable_hash text NOT NULL,
    memory_mb       integer NOT NULL CHECK (memory_mb >= 0),
    state           text NOT NULL DEFAULT 'loaded' CHECK (state IN ('loaded', 'unloaded')),
    loaded_at       timestamptz NOT NULL,
    last_used_at    timestamptz NOT NULL,
    unloaded_at     timestamptz,
    PRIMARY KEY (target_id, model)
);

-- The served models of a lease (one per deployable it consumes): the control plane counts a model's active leases
-- through this (06 "Staging serving": a model is unloaded only when no lease uses it), and the queue reserves a served
-- model's memory once per card however many leases use it (keyed by the least model name of a lease).
CREATE TABLE serving_leases (
    lease_id   text NOT NULL REFERENCES leases (id),
    target_id  text NOT NULL,
    model      text NOT NULL,
    memory_mb  integer NOT NULL CHECK (memory_mb >= 0),
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (lease_id, model)
);

CREATE INDEX serving_leases_model_idx ON serving_leases (target_id, model);
