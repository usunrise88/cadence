-- 0001 · Phase 0 core: the development user, projects, per-user workspaces, the event outbox and idempotency keys.

CREATE TABLE users (
    id         text PRIMARY KEY,
    name       text NOT NULL UNIQUE,
    created_at timestamptz NOT NULL DEFAULT now()
);

-- The fixed development actor; phase 1 replaces it with real login and keeps the row as the admin account.
INSERT INTO users (id, name) VALUES ('usr_admin', 'admin');

CREATE TABLE projects (
    id          text PRIMARY KEY,                 -- prj_<uuidv7>
    slug        text NOT NULL UNIQUE,
    name        text NOT NULL,
    description text NOT NULL DEFAULT '',
    rev         integer NOT NULL DEFAULT 1 CHECK (rev >= 1),
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now(),
    archived_at timestamptz
);

CREATE TABLE workspaces (
    id             text PRIMARY KEY,              -- wsp_<uuidv7>
    user_id        text NOT NULL REFERENCES users (id),
    project_id     text NOT NULL REFERENCES projects (id),
    name           text NOT NULL,
    schema_version integer NOT NULL,
    layout         jsonb NOT NULL,
    panels         jsonb NOT NULL,
    rev            integer NOT NULL DEFAULT 1 CHECK (rev >= 1),
    updated_at     timestamptz NOT NULL DEFAULT now(),
    UNIQUE (user_id, project_id, name)
);

-- The transactional outbox. Rows are inserted in the same transaction as the change they describe, under an
-- advisory lock that makes commit order equal seq order (see internal/events.Append).
CREATE TABLE events (
    seq        bigserial PRIMARY KEY,
    topic      text NOT NULL,
    type       text NOT NULL,
    project_id text,                              -- work events only; registry events carry none
    entity     jsonb,
    actor      jsonb NOT NULL,
    caused_by  jsonb,
    payload    jsonb NOT NULL DEFAULT '{}',
    at         timestamptz NOT NULL DEFAULT now()
);

-- text_pattern_ops serves both equality and the prefix LIKE used by trailing-wildcard topic patterns.
CREATE INDEX events_topic_idx ON events (topic text_pattern_ops);
CREATE INDEX events_project_seq_idx ON events (project_id, seq);

-- Wake the dispatcher; notifications are delivered at commit, duplicates within a transaction collapse.
CREATE FUNCTION events_notify() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    PERFORM pg_notify('cadence_events', '');
    RETURN NULL;
END
$$;

CREATE TRIGGER events_notify AFTER INSERT ON events FOR EACH STATEMENT EXECUTE FUNCTION events_notify();

-- Stored results of committed commands, replayed for a repeated Idempotency-Key.
CREATE TABLE idempotency_keys (
    actor_id     text NOT NULL,
    key          text NOT NULL,
    operation    text NOT NULL,
    request_hash text NOT NULL,
    status       integer NOT NULL,
    headers      jsonb NOT NULL,
    body         bytea NOT NULL,
    created_at   timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (actor_id, key)
);
