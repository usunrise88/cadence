-- 0033 · Phase 4 · stream M: mounts, utterance URIs and the local cache (docs/review/2026-10-03-phase-4-plan.md
-- decisions 1–3; docs/spec/02-domain-projects-registry.md "Storage and mounts"). All of it is registry data.

-- A mount is a storage location Cadence reads audio from (and, when writable, writes exports to). Its name is the
-- authority of every URI on it: mount://<name>/<path>[#t=<start>,<end>][&ch=<n>]. Registered only through an
-- approved mounts.new; the configuration never changes afterwards (a new location is a new mount), so rev stays 1
-- until an edit verb exists. Health and inventory are the last check and scan; they change without a revision.
CREATE TABLE mounts (
    id           text PRIMARY KEY,                    -- mnt_<uuidv7>
    name         text NOT NULL UNIQUE CHECK (name ~ '^[a-z0-9]([a-z0-9-]{0,38}[a-z0-9])?$'),
    kind         text NOT NULL CHECK (kind IN ('local', 'nfs', 'smb', 's3', 'hf')),
    root         text NOT NULL CHECK (root <> ''),
    endpoint     text NOT NULL DEFAULT '',            -- s3
    region       text NOT NULL DEFAULT '',            -- s3
    revision     text NOT NULL DEFAULT '',            -- hf: the commit every read is pinned to
    credentials  text NOT NULL DEFAULT '',            -- a secret's name, never its value
    read_only    boolean NOT NULL DEFAULT true,
    licence_hint text NOT NULL DEFAULT '',
    description  text NOT NULL DEFAULT '',
    health       jsonb NOT NULL DEFAULT '{"state": "unknown"}',
    inventory    jsonb,
    rev          integer NOT NULL DEFAULT 1 CHECK (rev >= 1),
    created_by   jsonb NOT NULL,
    approval_id  text,
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now(),
    CHECK (kind <> 'hf' OR revision ~ '^[0-9a-f]{40}$'),
    CHECK (kind <> 's3' OR (endpoint <> '' AND credentials <> ''))
);

-- Where an utterance's audio also lives (decision 3: index in place). The utterance keeps its b3: identity; a URI
-- names a whole file or a segment of one (#t=start,end in seconds, &ch=n for one channel of a multi-channel file).
CREATE TABLE utterance_uris (
    utterance_id text NOT NULL REFERENCES utterances (id),
    uri          text NOT NULL CHECK (uri LIKE 'mount://%'),
    mount_id     text NOT NULL REFERENCES mounts (id),
    created_at   timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (utterance_id, uri)
);

CREATE INDEX utterance_uris_mount_idx ON utterance_uris (mount_id);

-- Copies of content-store blobs on mounts (a scan finds them under cas/b3/<ab>/<hash>; exports record theirs). A blob
-- with a copy may be evicted from the local cache, and datasets.materialize copies it back from here; a blob with no
-- copy anywhere is never evicted (decision 2).
CREATE TABLE blob_copies (
    hash       text NOT NULL CHECK (hash ~ '^b3:[0-9a-f]{64}$'),
    mount_id   text NOT NULL REFERENCES mounts (id),
    path       text NOT NULL,                         -- relative to the mount's root
    size       bigint NOT NULL CHECK (size >= 0),
    seen_at    timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (hash, mount_id)
);

-- LRU: when a lease or a materialisation last read the artifact (NULL: never since it was recorded).
ALTER TABLE artifacts ADD COLUMN last_used_at timestamptz;

CREATE INDEX artifacts_dataset_lru_idx ON artifacts (coalesce(last_used_at, created_at)) WHERE type = 'dataset' AND evicted_at IS NULL;

-- The cache sweep's last run (one row): storage.get shows it.
CREATE TABLE cache_sweeps (
    id          boolean PRIMARY KEY DEFAULT true CHECK (id),
    swept_at    timestamptz NOT NULL,
    used_pct    double precision NOT NULL,
    job_id      text
);
