-- 0026 · Phase 3 stream T (manual transcription tests and the live channel; R47–R50, docs/spec/06-platform.md
-- "Media", spike A5). A transcription session is an interactive job (R49) whose record keeps only who, when, which
-- targets and the GPU time (R47): no audio, no text, no metric. The ticket and the lease's live token are kept as
-- SHA-256 hashes while they can be used and cleared when the session ends.
CREATE TABLE transcriptions (
    id                text PRIMARY KEY,                      -- trs_<uuidv7>
    project_id        text NOT NULL REFERENCES projects (id),
    user_id           text NOT NULL,                         -- the person (actor id); one open session each
    actor             jsonb NOT NULL,
    job_id            text REFERENCES jobs (id),             -- the interactive job (River kind live)
    targets           jsonb NOT NULL,                        -- [{target, kind, id, profile, language, boost?}]
    reservation_mb    integer NOT NULL DEFAULT 0,
    state             text NOT NULL DEFAULT 'queued' CHECK (state IN ('queued', 'loading', 'live', 'ended')),
    end_reason        text,
    ticket_hash       text,                                  -- sha256 of the single-use ticket; null once used
    ticket_expires_at timestamptz,
    live_token_hash   text,                                  -- sha256 of the current lease's live token
    gpu_seconds       double precision NOT NULL DEFAULT 0,   -- lease wall time of the job (the allowance's meter)
    created_at        timestamptz NOT NULL DEFAULT now(),
    started_at        timestamptz,                           -- the worker joined the socket
    ended_at          timestamptz
);

CREATE UNIQUE INDEX transcriptions_open_user_idx ON transcriptions (user_id) WHERE state <> 'ended';
CREATE UNIQUE INDEX transcriptions_job_idx ON transcriptions (job_id);
CREATE INDEX transcriptions_project_idx ON transcriptions (project_id, created_at DESC);

-- Cards that take evaluations take interactive sessions too (the seeded staging card does from now on): a session
-- reserves the family's interactive memory beside training under the card's cap, never beside a benchmark.
UPDATE compute_hosts h
SET cards = (
        SELECT coalesce(jsonb_agg(CASE
                    WHEN c -> 'allowedJobKinds' ? 'eval' AND NOT (c -> 'allowedJobKinds' ? 'interactive')
                        THEN jsonb_set(c, '{allowedJobKinds}', (c -> 'allowedJobKinds') || '["interactive"]'::jsonb)
                    ELSE c END ORDER BY t.ord), '[]'::jsonb)
        FROM jsonb_array_elements(h.cards) WITH ORDINALITY AS t (c, ord)),
    rev = rev + 1,
    updated_at = now()
WHERE EXISTS (SELECT 1 FROM jsonb_array_elements(h.cards) AS c
              WHERE c -> 'allowedJobKinds' ? 'eval' AND NOT (c -> 'allowedJobKinds' ? 'interactive'));
