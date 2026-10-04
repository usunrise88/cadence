-- 0035 · Phase 4 stream X: auxiliary models and the triage queue (docs/review/2026-10-03-phase-4-plan.md "Decisions
-- taken for phase 4" 5–7, R26). Auxiliary versions are ordinary registry versions (kind auxiliary, checked in Go);
-- what is new is where a step's resolved registry references live and the queue of disputed pseudo-labels.

-- The registry versions a step's parameters name (x-cadence.registryRef), resolved and checked for the project when
-- the run was planned: parameter → {versionId, name, version, payload}. The worker reads them from the step spec.
ALTER TABLE pipeline_steps ADD COLUMN auxiliaries jsonb NOT NULL DEFAULT '{}';

-- Disputed pseudo-labels (origin pseudo-label:disputed): segments whose ensemble members disagree, whose language
-- identification disagrees with the source's language, or where no member heard speech. Never trained on; a person
-- resolves them (the Triage panel and triage.accept|correct|reject arrive with annotation, wave 2). One row per
-- segment of a segments artifact, so a reused step output indexes nothing twice.
CREATE TABLE triage_items (
    id               text PRIMARY KEY,                                  -- tri_<uuidv7>
    project_id       text NOT NULL REFERENCES projects (id),
    state            text NOT NULL DEFAULT 'open' CHECK (state IN ('open', 'accepted', 'corrected', 'rejected')),
    reason           text NOT NULL CHECK (reason IN ('disagreement', 'lid-mismatch', 'lid-unknown', 'no-speech', 'too-few-members')),
    pipeline_run_id  text NOT NULL,
    step_id          text NOT NULL,
    segments_hash    text NOT NULL,                                     -- the segments artifact (b3:…)
    segment_hash     text NOT NULL,                                     -- the segment's canonical audio hash (b3:…)
    segment          jsonb NOT NULL,                                    -- uri, start, end, channel, role, language, speaker
    candidates       jsonb NOT NULL DEFAULT '[]',                       -- [{member, text, meanWer, confidence?, language?}]
    best             text NOT NULL DEFAULT '',
    lid              jsonb,                                             -- {language, confidence, agrees}
    confidence       double precision NOT NULL DEFAULT 0,
    rev              integer NOT NULL DEFAULT 1,
    created_at       timestamptz NOT NULL DEFAULT now(),
    updated_at       timestamptz NOT NULL DEFAULT now(),
    UNIQUE (project_id, segments_hash, segment_hash)
);

CREATE INDEX triage_items_project_idx ON triage_items (project_id, state, created_at DESC);
CREATE INDEX triage_items_run_idx ON triage_items (pipeline_run_id);
