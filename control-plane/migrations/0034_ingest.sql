-- 0034 · Phase 4 · stream D: ingest from mounts, draft dataset versions, source history, utterance search
-- (docs/review/2026-10-03-phase-4-plan.md "Decisions taken for phase 4" 3–4).
--
-- A draft dataset version is a registry version in state 'draft' (migration 0003 allows its payload to change until it
-- is frozen); its utterances already have their identity — the BLAKE3 of the canonical 16 kHz PCM16 WAV of each
-- segment, computed while indexing — so memberships, fingerprints and the leakage check work before any audio is
-- copied. Nothing new is needed for that; this migration adds the history a source keeps and the search indexes.

-- Every change of a source's licence or training clearance, oldest first (sources.get "clearances"). Rows are never
-- updated or deleted.
CREATE TABLE source_clearances (
    id               bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    source_id        text NOT NULL REFERENCES sources (id),
    change           text NOT NULL CHECK (change IN ('created', 'licence', 'cleared', 'uncleared')),
    licence          text NOT NULL,
    training_cleared boolean NOT NULL,
    actor            jsonb NOT NULL,
    at               timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX source_clearances_source_idx ON source_clearances (source_id, id);

-- Sources registered before this migration: their creation, and their clearance when they are cleared.
INSERT INTO source_clearances (source_id, change, licence, training_cleared, actor, at)
SELECT id, 'created', licence, false, created_by, created_at FROM sources ORDER BY created_at, id;
INSERT INTO source_clearances (source_id, change, licence, training_cleared, actor, at)
SELECT id, 'cleared', licence, true, coalesce(cleared_by, created_by), coalesce(cleared_at, updated_at)
FROM sources WHERE training_cleared ORDER BY cleared_at, id;

-- Every dataset version an import or an ingest registered from a source (sources.get "ingests"), written by the
-- dataset output hook in the transaction that registers the version.
CREATE TABLE source_ingests (
    version_id      text NOT NULL REFERENCES registry_versions (id),
    source_id       text NOT NULL REFERENCES sources (id),
    pipeline_run_id text NOT NULL DEFAULT '',
    project_id      text NOT NULL DEFAULT '',
    step_kind       text NOT NULL DEFAULT '',
    utterances      integer NOT NULL CHECK (utterances >= 0),
    hours           double precision NOT NULL CHECK (hours >= 0),
    at              timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (version_id, source_id)
);

CREATE INDEX source_ingests_source_idx ON source_ingests (source_id, at DESC);

-- Versions imported before this migration, from their memberships and lineage.
INSERT INTO source_ingests (version_id, source_id, pipeline_run_id, project_id, step_kind, utterances, hours, at)
SELECT d.version_id, u.source_id, coalesce(v.payload->'lineage'->>'pipelineRunId', ''),
       coalesce(v.payload->'lineage'->>'projectId', ''), coalesce(v.payload->'lineage'->>'stepKind', ''),
       count(*)::int, sum(u.duration_s) / 3600.0, v.created_at
FROM dataset_utterances d JOIN utterances u ON u.id = d.utterance_id JOIN registry_versions v ON v.id = d.version_id
GROUP BY d.version_id, u.source_id, v.id;

-- utterances.search: transcript text by case-insensitive substring (pg_trgm, migration 0007), speakers within a
-- source, and durations.
CREATE INDEX transcripts_text_trgm_idx ON transcripts USING gin (lower(text) gin_trgm_ops);
CREATE INDEX utterances_speaker_idx ON utterances (speaker, id) WHERE speaker <> '';
CREATE INDEX utterances_duration_idx ON utterances (duration_s, id);
