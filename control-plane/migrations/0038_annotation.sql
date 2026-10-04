-- 0038 · Phase 4 · stream A: annotation batches, double annotation and adjudication, reviewer accounts, triage
-- resolutions (docs/spec/04-blocks.md "Annotation workflow", docs/review/2026-10-03-phase-4-plan.md decision 10, R27).
-- 0036 and 0037 belong to other phase-4 streams; migrations apply by number.

-- A reviewer is a person invited to one annotation batch: no password, a session that reaches that batch only
-- (docs/spec/06-platform.md "Authentication and access"). The admin stays role admin.
ALTER TABLE users ADD COLUMN role text NOT NULL DEFAULT 'admin' CHECK (role IN ('admin', 'reviewer'));

-- An annotation batch: a fixed, stratified sample of segments of one role from a frame (a dataset version's segments
-- artifact), the guidelines commit it pins, and its freeze into a golden set or a training dataset version.
CREATE TABLE annotation_batches (
    id           text PRIMARY KEY,                                   -- anb_<uuidv7>
    project_id   text NOT NULL REFERENCES projects (id),
    name         text NOT NULL,
    description  text NOT NULL DEFAULT '',
    purpose      text NOT NULL CHECK (purpose IN ('golden-set', 'training')),
    state        text NOT NULL DEFAULT 'open' CHECK (state IN ('open', 'freezing', 'frozen', 'failed')),
    role         text NOT NULL,
    stratify     text[] NOT NULL DEFAULT '{}',
    double_share double precision NOT NULL CHECK (double_share >= 0 AND double_share <= 1),
    seed         bigint NOT NULL DEFAULT 0,
    context_s    double precision NOT NULL DEFAULT 0,
    due_at       timestamptz,
    guidelines   jsonb NOT NULL,                                     -- {name, path, commit}
    frame        jsonb NOT NULL,                                     -- {datasetVersionId?, segmentsHash, source, segments}
    strata       jsonb NOT NULL DEFAULT '[]',                        -- [{key, frame, sampled}]
    golden_set   text NOT NULL,
    freeze_state jsonb,                                              -- BatchFreezeState
    rev          integer NOT NULL DEFAULT 1 CHECK (rev >= 1),
    created_by   jsonb NOT NULL,
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now(),
    UNIQUE (project_id, name)
);

CREATE INDEX annotation_batches_project_idx ON annotation_batches (project_id, created_at DESC);

-- One sampled segment. Its id is also the media id of its audio window (the segment plus context_s each side, every
-- channel of the source file), so a reviewer's signed links name the item and nothing else.
CREATE TABLE annotation_items (
    id           text PRIMARY KEY,                                   -- bit_<uuidv7>
    batch_id     text NOT NULL REFERENCES annotation_batches (id),
    position     integer NOT NULL CHECK (position >= 1),
    hash         text NOT NULL CHECK (hash ~ '^b3:[0-9a-f]{64}$'),   -- the segment's canonical audio hash
    state        text NOT NULL DEFAULT 'pending' CHECK (state IN ('pending', 'agreed', 'disputed', 'adjudicated', 'excluded')),
    segment      jsonb NOT NULL,                                     -- the segments row (cadence.segments/1) as sampled
    audio_window jsonb NOT NULL,                                     -- {file, start, end, channels, roles}
    prefill      jsonb NOT NULL,                                     -- {text, origin, confidence?}
    context      jsonb NOT NULL DEFAULT '{"turns": []}',
    strata       jsonb NOT NULL DEFAULT '{}',
    double_item  boolean NOT NULL DEFAULT false,
    eou          jsonb,                                              -- {speechEnd, nextSpeech, gapS}
    final        jsonb,                                              -- {text, tags, entities, by, at, from?}
    wer          double precision,                                   -- between the first two transcripts
    rev          integer NOT NULL DEFAULT 1 CHECK (rev >= 1),
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now(),
    UNIQUE (batch_id, position),
    UNIQUE (batch_id, hash)
);

CREATE INDEX annotation_items_batch_state_idx ON annotation_items (batch_id, state, position);

-- A person's annotation of an item: one per annotator and item (a second submission replaces the first). seq orders
-- the annotators of an item (1 is the first transcript, the reference of the inter-annotator WER).
CREATE TABLE annotations (
    id           text PRIMARY KEY,                                   -- ann_<uuidv7>
    item_id      text NOT NULL REFERENCES annotation_items (id),
    annotator_id text NOT NULL REFERENCES users (id),
    annotator    jsonb NOT NULL,                                     -- the Actor
    seq          integer NOT NULL CHECK (seq >= 1),
    status       text NOT NULL CHECK (status IN ('done', 'skipped', 'flagged')),
    text         text NOT NULL DEFAULT '',
    tags         text[] NOT NULL DEFAULT '{}',
    entities     jsonb NOT NULL DEFAULT '[]',
    note         text NOT NULL DEFAULT '',
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now(),
    UNIQUE (item_id, annotator_id),
    UNIQUE (item_id, seq)
);

CREATE INDEX annotations_annotator_idx ON annotations (annotator_id, item_id);

-- How a person resolved a triage item (triage.accept, triage.correct, triage.reject): the human transcript written,
-- the tags, or why the segment was dropped.
ALTER TABLE triage_items ADD COLUMN resolution jsonb;
