-- 0004 · Phase 1 stream C: approvals (gated commands waiting for a person), the audit log and the jobs mirror.
-- River's own tables (river_job, river_leader, …) come from River's migrator, run at start next to these files
-- under the same advisory lock (internal/storage.Migrate, internal/jobs.MigrateRiver).

-- A gated command stored for a person to decide. Approving replays the stored request as the original actor.
CREATE TABLE approvals (
    id            text PRIMARY KEY,                          -- apr_<uuidv7>
    state         text NOT NULL DEFAULT 'pending' CHECK (state IN ('pending', 'approved', 'denied')),
    operation     text NOT NULL,
    project_id    text REFERENCES projects (id),             -- empty scope = registry approval
    actor         jsonb NOT NULL,                            -- who asked (the original actor of the request)
    actor_id      text NOT NULL,
    session_id    text,                                      -- the agent session, for session-scoped grants
    scope         jsonb NOT NULL DEFAULT '{}',               -- the actor's scope when it asked (policy.Scope)
    rule          text NOT NULL,
    reason        text NOT NULL,
    estimate      jsonb,
    method        text NOT NULL,
    path          text NOT NULL,
    query         text NOT NULL DEFAULT '',
    headers       jsonb NOT NULL,                            -- without credentials and cookies
    body          bytea NOT NULL DEFAULT '',
    rev           integer NOT NULL DEFAULT 1 CHECK (rev >= 1),
    created_at    timestamptz NOT NULL DEFAULT now(),
    expires_at    timestamptz NOT NULL,
    decided_at    timestamptz,
    decided_by    jsonb,
    grant_scope   text CHECK (grant_scope IN ('once', 'session')),
    note          text,
    expired       boolean NOT NULL DEFAULT false,
    result_status integer,
    result_body   bytea,
    result_command_id text
);

CREATE INDEX approvals_pending_idx ON approvals (expires_at) WHERE state = 'pending';
CREATE INDEX approvals_project_idx ON approvals (project_id, created_at DESC);
-- Session-scoped grants: an approved request lets the same session repeat the operation on the same path.
CREATE INDEX approvals_session_grant_idx ON approvals (session_id, operation, path)
    WHERE state = 'approved' AND grant_scope = 'session';

-- Every committed command, denial, approval request and failed attempt. Kept one year (daily sweep).
CREATE TABLE audit_log (
    id           text PRIMARY KEY,                           -- aud_<uuidv7>, so id order is time order
    command_id   text,
    operation    text NOT NULL,
    actor        jsonb NOT NULL,
    actor_id     text NOT NULL,
    preset       text,
    project_id   text,
    outcome      text NOT NULL,
    status       integer NOT NULL,
    rule         text,
    tool_call_id text,
    approval_id  text,
    at           timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX audit_log_at_idx ON audit_log (at);
CREATE INDEX audit_log_actor_idx ON audit_log (actor_id, id DESC);
CREATE INDEX audit_log_project_idx ON audit_log (project_id, id DESC);
CREATE INDEX audit_log_operation_idx ON audit_log (operation, id DESC);

-- The Cadence view of a job; River's river_job row does the scheduling, this row carries what the API shows.
CREATE TABLE jobs (
    id                  text PRIMARY KEY,                    -- job_<uuidv7>
    river_id            bigint NOT NULL UNIQUE,
    kind                text NOT NULL,
    project_id          text REFERENCES projects (id),
    state               text NOT NULL DEFAULT 'queued'
                        CHECK (state IN ('queued', 'running', 'done', 'failed', 'cancelled')),
    progress            double precision NOT NULL DEFAULT 0 CHECK (progress >= 0 AND progress <= 1),
    message             text,
    result              jsonb,
    error               text,
    attempt             integer NOT NULL DEFAULT 0,
    actor               jsonb NOT NULL,
    rev                 integer NOT NULL DEFAULT 1 CHECK (rev >= 1),
    created_at          timestamptz NOT NULL DEFAULT now(),
    updated_at          timestamptz NOT NULL DEFAULT now(),
    started_at          timestamptz,
    finished_at         timestamptz,
    cancel_requested_at timestamptz
);

CREATE INDEX jobs_project_idx ON jobs (project_id, created_at DESC);
