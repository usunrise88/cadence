-- 0006 · Phase 1 wave 2 (mix stream): mixes as project work with revisions (docs/spec/08-resolutions.md R13) and
-- drafts, the generic unaccepted change of a draftable entity (docs/spec/06-platform.md "Real-time model").
-- 0005 belongs to the Projects stream of the same wave; migrations apply by number and tolerate the gap.

-- A mix: groups over frozen dataset versions, weights, temperature and replay share. `content` is the current
-- revision's content (the contract's MixNew with defaults filled in and dataset references resolved to ver_ ids).
CREATE TABLE mixes (
    id          text PRIMARY KEY,                               -- mix_<uuidv7>
    project_id  text NOT NULL REFERENCES projects (id),
    content     jsonb NOT NULL,
    name        text GENERATED ALWAYS AS (content ->> 'name') STORED,
    rev         integer NOT NULL DEFAULT 1 CHECK (rev >= 1),
    created_by  jsonb NOT NULL,
    updated_by  jsonb NOT NULL,
    cause       jsonb,                                          -- draftId, draftAuthor, toolCallId of the revision
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX mixes_project_name_idx ON mixes (project_id, lower(name));
CREATE INDEX mixes_project_updated_idx ON mixes (project_id, updated_at DESC);

-- Every revision's content, so a run can record the revision it trained on and a draft can diff against its base.
CREATE TABLE mix_revisions (
    mix_id     text NOT NULL REFERENCES mixes (id),
    rev        integer NOT NULL CHECK (rev >= 1),
    content    jsonb NOT NULL,
    actor      jsonb NOT NULL,
    cause      jsonb,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (mix_id, rev)
);

-- A draft proposes new content for one entity of a draftable kind, based on one of its revisions. An author (an
-- agent session) has at most one open draft per entity: its later edits update the same draft (rev + 1).
CREATE TABLE drafts (
    id           text PRIMARY KEY,                              -- drf_<uuidv7>
    project_id   text NOT NULL REFERENCES projects (id),
    entity_kind  text NOT NULL,                                 -- mix (gate, note, language pack later)
    entity_id    text NOT NULL,
    base_rev     integer NOT NULL CHECK (base_rev >= 1),
    rev          integer NOT NULL DEFAULT 1 CHECK (rev >= 1),
    state        text NOT NULL DEFAULT 'open' CHECK (state IN ('open', 'accepted', 'reverted')),
    content      jsonb NOT NULL,
    author       jsonb NOT NULL,
    author_key   text NOT NULL,                                 -- actor id + session: one open draft per author
    tool_call_id text NOT NULL DEFAULT '',
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now(),
    decided_by   jsonb,
    decided_at   timestamptz,
    applied_rev  integer
);

CREATE UNIQUE INDEX drafts_open_author_idx ON drafts (entity_kind, entity_id, author_key) WHERE state = 'open';
CREATE INDEX drafts_entity_idx ON drafts (entity_kind, entity_id, created_at DESC);
