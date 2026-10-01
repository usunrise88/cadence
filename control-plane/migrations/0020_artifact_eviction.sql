-- 0020 · Phase 2 stream E: content-store retention (docs/spec/06-platform.md "Artifacts, metrics and logs",
-- Retention). artifacts.evict deletes the blobs of superseded training states after a person approves it; the index
-- row stays, marked evicted, so lineage still resolves and artifacts.get says where the bytes went.

-- An evicted artifact: its blobs are gone from the store (the backup mirror keeps them), its row and metadata stay.
ALTER TABLE artifacts
    ADD COLUMN evicted_at       timestamptz,
    ADD COLUMN evicted_by       jsonb,
    ADD COLUMN eviction_job_id  text;

CREATE INDEX artifacts_type_live_idx ON artifacts (type, created_at) WHERE evicted_at IS NULL;

-- The files of each directory artifact (its manifest, as rows): a file blob may be listed by several artifacts, so
-- a blob is deleted only when no artifact that stays lists it. artifacts.Record fills it; directory artifacts
-- recorded before this migration are backfilled from their manifests at start (internal/eviction.Backfill), since
-- SQL cannot read the store. Rows stay after an eviction: they are the evicted directory's file list.
CREATE TABLE artifact_files (
    hash       text NOT NULL REFERENCES artifacts (hash),
    path       text NOT NULL,
    file_hash  text NOT NULL CHECK (file_hash ~ '^b3:[0-9a-f]{64}$'),
    size       bigint NOT NULL CHECK (size >= 0),
    PRIMARY KEY (hash, path)
);

CREATE INDEX artifact_files_file_idx ON artifact_files (file_hash);

-- What a job did for the command an audit row records (artifacts.evict: the artifacts, bytes freed, blobs deleted).
ALTER TABLE audit_log ADD COLUMN detail jsonb;
