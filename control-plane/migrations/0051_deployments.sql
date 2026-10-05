-- 0051 · Phase 5 · stream D4: deployments, their stage history and nightly shadow replays
-- (docs/review/2026-10-05-phase-5-plan.md; docs/spec/02-domain-projects-registry.md "Deployment entities"; R30–R33).

-- A deployment is project work: a model version's export (model_exports) served first as a shadow on the staging
-- target, then promoted to a delivery target's slot as canary and production. stage moves only when a promotion
-- record is confirmed (promotions.verify); while one waits for its receipt the deployment is pending-delivery and
-- pending_record_id names it. replay is where a shadow's calls come from ({mount, path, source, language,
-- channelRoles}); against the model it is compared with ({kind, versionId, label, deploymentId?}); shadow the running
-- totals of its replays ({hours, calls, utterances, nights, divergence, lastReplayAt}).
CREATE TABLE deployments (
    id                text PRIMARY KEY,                    -- dep_<uuidv7>
    project_id        text NOT NULL REFERENCES projects (id),
    model_version_id  text NOT NULL,
    export_id         text NOT NULL REFERENCES model_exports (id),
    profile           text NOT NULL,
    format            text NOT NULL,
    target_id         text NOT NULL REFERENCES deployment_targets (id),
    slot              text,
    model_name        text,                                -- the versioned model name on the delivery target
    stage             text NOT NULL DEFAULT 'shadow' CHECK (stage IN ('shadow', 'canary', 'production', 'retired')),
    state             text NOT NULL DEFAULT 'active' CHECK (state IN ('active', 'pending-delivery', 'rolled-back', 'retired')),
    traffic_share     double precision CHECK (traffic_share IS NULL OR (traffic_share > 0 AND traffic_share <= 1)),
    decoding          jsonb NOT NULL DEFAULT '{"boostLists": []}',
    replay            jsonb,
    against           jsonb,
    shadow            jsonb NOT NULL DEFAULT '{}',
    pending_record_id text REFERENCES promotion_records (id),
    rev               integer NOT NULL DEFAULT 1 CHECK (rev >= 1),
    created_by        jsonb NOT NULL,
    created_at        timestamptz NOT NULL DEFAULT now(),
    updated_at        timestamptz NOT NULL DEFAULT now(),
    CHECK ((state = 'pending-delivery') = (pending_record_id IS NOT NULL))
);

CREATE INDEX deployments_project_idx ON deployments (project_id, created_at DESC);
CREATE INDEX deployments_slot_idx ON deployments (target_id, slot) WHERE slot IS NOT NULL;
CREATE INDEX deployments_shadow_idx ON deployments (stage) WHERE stage = 'shadow' AND replay IS NOT NULL;

-- The stage history of a deployment, oldest first: its creation, each promotion or rollback (with the record and the
-- decoding configuration it names, boost lists with their content-store hashes so the delivery bundle can ship them),
-- and what closed it (a confirmation, a withdrawal), plus the steps another deployment's confirmation caused (retired,
-- restored).
CREATE TABLE deployment_steps (
    id            text PRIMARY KEY,                        -- dst_<uuidv7>
    deployment_id text NOT NULL REFERENCES deployments (id),
    kind          text NOT NULL CHECK (kind IN ('created', 'promotion', 'rollback', 'confirmation', 'withdrawal', 'retired', 'restored')),
    from_stage    text,
    to_stage      text,
    record_id     text REFERENCES promotion_records (id),
    target_id     text REFERENCES deployment_targets (id),
    slot          text,
    traffic_share double precision,
    decoding      jsonb,
    -- A rollback's restored deployment, or a promotion's previous production deployment on the slot.
    other_id      text REFERENCES deployments (id),
    reason        text NOT NULL DEFAULT '',
    approval_id   text,
    actor         jsonb NOT NULL,
    created_at    timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX deployment_steps_deployment_idx ON deployment_steps (deployment_id, created_at, id);
CREATE UNIQUE INDEX deployment_steps_record_idx ON deployment_steps (record_id, kind) WHERE record_id IS NOT NULL;

-- One night of a shadow deployment's replay (srp_…, also the pipeline run's id): the calls it took, then what
-- shadow_score@1 reported. worst holds the night's most divergent segments with both texts (production-derived:
-- cleared with the night's artifacts after deploy.shadow_artifact_retention_days, texts_evicted_at; the summary stays).
CREATE TABLE shadow_replays (
    id              text PRIMARY KEY,                      -- srp_<uuidv7>
    deployment_id   text NOT NULL REFERENCES deployments (id),
    project_id      text NOT NULL REFERENCES projects (id),
    night           date NOT NULL,
    trigger         text NOT NULL CHECK (trigger IN ('nightly', 'manual')),
    state           text NOT NULL CHECK (state IN ('planned', 'running', 'done', 'failed', 'skipped')),
    reason          text NOT NULL DEFAULT '',
    pipeline_run_id text,
    calls           integer NOT NULL DEFAULT 0,
    hours           double precision NOT NULL DEFAULT 0,
    utterances      integer NOT NULL DEFAULT 0,
    divergence      jsonb,
    confidence      jsonb,
    against         jsonb,
    report_hash     text CHECK (report_hash IS NULL OR report_hash ~ '^b3:[0-9a-f]{64}$'),
    worst           jsonb,
    selected        jsonb NOT NULL DEFAULT '[]',           -- the calls the night took (mount URIs), in order
    texts_evicted_at timestamptz,
    created_by      jsonb NOT NULL,
    created_at      timestamptz NOT NULL DEFAULT now(),
    finished_at     timestamptz
);

CREATE INDEX shadow_replays_deployment_idx ON shadow_replays (deployment_id, created_at DESC);
CREATE UNIQUE INDEX shadow_replays_nightly_idx ON shadow_replays (deployment_id, night) WHERE trigger = 'nightly';
CREATE UNIQUE INDEX shadow_replays_running_idx ON shadow_replays (deployment_id) WHERE state IN ('planned', 'running');
CREATE INDEX shadow_replays_retention_idx ON shadow_replays (finished_at) WHERE texts_evicted_at IS NULL;

-- The calls a shadow deployment replayed, each once (a call counts once toward shadow.hours): the next night takes
-- the newest calls not listed here. A call is its mount URI.
CREATE TABLE shadow_calls (
    deployment_id text NOT NULL REFERENCES deployments (id),
    call          text NOT NULL,
    replay_id     text NOT NULL REFERENCES shadow_replays (id),
    duration_s    double precision NOT NULL DEFAULT 0,
    segments      integer NOT NULL DEFAULT 0,
    created_at    timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (deployment_id, call)
);

-- The segment audio a night's replay cut (its canonical WAV in the content store): audio.get plays a worst segment by
-- its hash while the night's texts are kept; the rows go with them.
CREATE TABLE shadow_segments (
    hash       text NOT NULL CHECK (hash ~ '^b3:[0-9a-f]{64}$'),
    replay_id  text NOT NULL REFERENCES shadow_replays (id),
    duration_s double precision NOT NULL DEFAULT 0,
    PRIMARY KEY (hash, replay_id)
);
