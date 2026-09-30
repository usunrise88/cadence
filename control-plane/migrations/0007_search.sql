-- 0007 · Phase 1 search: the search index fed from the outbox, its cursor, and saved searches
-- (docs/spec/11-ui-panels.md "Search", ROADMAP.md "Phase 1 · Projects").
--
-- The index is a projection: internal/search.Indexer reads events after its cursor, reloads the entity each event
-- names and upserts its document here. Dropping every row and resetting the cursor to 0 rebuilds it.

CREATE EXTENSION IF NOT EXISTS pg_trgm;

CREATE TABLE search_documents (
    kind        text NOT NULL,                     -- EntityKind: project, dataset_version, job, help_article, …
    id          text NOT NULL,
    scope       text NOT NULL CHECK (scope IN ('project', 'registry', 'instance', 'help')),
    project_id  text,                              -- project work only; NULL for registry, instance and help
    ref         text NOT NULL,                     -- the document reference the UI opens (<kind>:<id>)
    title       text NOT NULL,
    body        text NOT NULL DEFAULT '',          -- the entity's searchable text, as written
    tags        text[] NOT NULL DEFAULT '{}',
    status      text NOT NULL DEFAULT '',
    lang        text NOT NULL DEFAULT '',          -- BCP 47 locale when the entity has one (he-IL)
    actor       jsonb,
    actor_kind  text NOT NULL DEFAULT '',
    numbers     jsonb NOT NULL DEFAULT '{}',       -- numeric fields for comparisons (wer, progress, …)
    -- Normalised text (lower case, per-locale folding: Hebrew niqqud and spelling variants), written by the
    -- indexer; queries are normalised the same way before matching.
    title_norm  text NOT NULL,
    body_norm   text NOT NULL DEFAULT '',
    tsv         tsvector GENERATED ALWAYS AS (
                    setweight(to_tsvector('simple', title_norm), 'A') || setweight(to_tsvector('simple', body_norm), 'B')
                ) STORED,
    seq         bigint NOT NULL DEFAULT 0,         -- the event that last wrote the document (0: indexed at start)
    updated_at  timestamptz NOT NULL,
    indexed_at  timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (kind, id)
);

CREATE INDEX search_documents_tsv_idx ON search_documents USING gin (tsv);
-- Trigram matching for typos and identifiers (fleurs-he-smoke, ver_0192…) over title and text.
CREATE INDEX search_documents_title_trgm_idx ON search_documents USING gin (title_norm gin_trgm_ops);
CREATE INDEX search_documents_body_trgm_idx ON search_documents USING gin (body_norm gin_trgm_ops);
CREATE INDEX search_documents_project_idx ON search_documents (project_id, updated_at DESC);
CREATE INDEX search_documents_scope_idx ON search_documents (scope, updated_at DESC);

-- Where each outbox consumer stopped: the last events.seq it has fully applied.
CREATE TABLE event_cursors (
    name       text PRIMARY KEY,
    seq        bigint NOT NULL DEFAULT 0,
    updated_at timestamptz NOT NULL DEFAULT now()
);

INSERT INTO event_cursors (name) VALUES ('search');

-- Saved searches: a named query in the qualifier language, per user (the actor id) per project.
CREATE TABLE saved_views (
    id          text PRIMARY KEY,                  -- vew_<uuidv7>
    user_id     text NOT NULL,                     -- the owner's actor id (usr_…, or crd_… for an API key)
    project_id  text NOT NULL REFERENCES projects (id),
    name        text NOT NULL,
    query       text NOT NULL,
    description text NOT NULL DEFAULT '',
    rev         integer NOT NULL DEFAULT 1 CHECK (rev >= 1),
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now(),
    UNIQUE (user_id, project_id, name)
);
