-- 0014 · Phase 2 · stream D: the minimal data entities imports need (docs/spec/08-resolutions.md R18,
-- docs/spec/02-domain-projects-registry.md "Data entities as built"). All of it is registry data: no projectId.
--
-- A source is a corpus with its licence and kind. Imported sources start eval-only (training_cleared false) until a
-- person clears them for training (sources.edit; agents need an approval). Archiving is soft and reversible only by
-- the admin in the database; an archived source takes no new imports.
CREATE TABLE sources (
    id               text PRIMARY KEY,                -- src_<uuidv7>
    name             text NOT NULL UNIQUE CHECK (name ~ '^[a-z0-9][a-z0-9._-]{0,98}[a-z0-9]$'),
    description      text NOT NULL DEFAULT '',
    licence          text NOT NULL CHECK (licence <> ''),
    kind             text NOT NULL CHECK (kind IN ('public', 'production', 'synthetic')),
    languages        text[] NOT NULL DEFAULT '{}',
    url              text NOT NULL DEFAULT '',
    training_cleared boolean NOT NULL DEFAULT false,
    cleared_by       jsonb,                           -- the actor who last set training_cleared to true
    cleared_at       timestamptz,
    archived         boolean NOT NULL DEFAULT false,
    rev              integer NOT NULL DEFAULT 1,
    created_by       jsonb NOT NULL,
    created_at       timestamptz NOT NULL DEFAULT now(),
    updated_at       timestamptz NOT NULL DEFAULT now()
);

-- An utterance is one audio segment in the content store; its identity is the BLAKE3 hash of the audio bytes (the
-- CAS blob of its file in the dataset artifact), so the same audio imported twice is one row. The first source that
-- imported it owns it.
CREATE TABLE utterances (
    id              text PRIMARY KEY,                 -- utt_<uuidv7>
    content_hash    text NOT NULL UNIQUE CHECK (content_hash ~ '^b3:[0-9a-f]{64}$'),
    source_id       text NOT NULL REFERENCES sources (id),
    duration_s      double precision NOT NULL CHECK (duration_s > 0),
    language        text NOT NULL,
    speaker         text NOT NULL DEFAULT '',
    sample_rate     integer NOT NULL CHECK (sample_rate > 0),
    channels        integer NOT NULL DEFAULT 1 CHECK (channels > 0),
    bytes           bigint NOT NULL DEFAULT 0,
    created_at      timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX utterances_source_idx ON utterances (source_id, id);
CREATE INDEX utterances_language_idx ON utterances (language, id);

-- A transcript is text for an utterance with its origin: human, pseudo-label, or a model id (model:<name>).
CREATE TABLE transcripts (
    id           text PRIMARY KEY,                    -- trn_<uuidv7>
    utterance_id text NOT NULL REFERENCES utterances (id),
    text         text NOT NULL,
    origin       text NOT NULL CHECK (origin IN ('human', 'pseudo-label') OR origin LIKE 'model:%'),
    confidence   double precision CHECK (confidence IS NULL OR (confidence >= 0 AND confidence <= 1)),
    created_at   timestamptz NOT NULL DEFAULT now(),
    UNIQUE (utterance_id, origin, text)
);

-- Which utterances a dataset version holds, in which split, with the transcript it uses. Written once when the
-- version is registered; frozen versions never change, so neither do their rows.
CREATE TABLE dataset_utterances (
    version_id    text NOT NULL REFERENCES registry_versions (id),
    utterance_id  text NOT NULL REFERENCES utterances (id),
    split         text NOT NULL CHECK (split IN ('train', 'validation', 'test')),
    transcript_id text NOT NULL REFERENCES transcripts (id),
    PRIMARY KEY (version_id, utterance_id)
);

CREATE INDEX dataset_utterances_utterance_idx ON dataset_utterances (utterance_id);
CREATE INDEX dataset_utterances_split_idx ON dataset_utterances (version_id, split, utterance_id);

-- Per-utterance fingerprints for leakage checks (golden set vs training data): kind audio-b3 (exact content) is
-- written by every import; steps may add others (an acoustic fingerprint in phase 4).
CREATE TABLE utterance_fingerprints (
    utterance_id text NOT NULL REFERENCES utterances (id),
    kind         text NOT NULL CHECK (kind ~ '^[a-z0-9][a-z0-9-]{0,39}$'),
    value        text NOT NULL,
    PRIMARY KEY (utterance_id, kind)
);

CREATE INDEX utterance_fingerprints_value_idx ON utterance_fingerprints (kind, value);
