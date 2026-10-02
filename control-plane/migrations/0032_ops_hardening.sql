-- 0032 · Phase 3 audit fixes, stream F3 (ops, notifications and media hardening). 0030–0031 belong to other fix
-- streams; migrations apply by number.

-- The manual-test allowance is granted, not only checked (docs/spec/06-platform.md "Media"): transcriptions.new
-- grants a GPU session its seconds from what the project has left net of the other open sessions' grants, under a
-- per-project lock, and the relay caps the session at its grant less what its job leased. 0: granted nothing (a CPU
-- session, or one opened before this migration).
ALTER TABLE transcriptions ADD COLUMN session_seconds integer NOT NULL DEFAULT 0 CHECK (session_seconds >= 0);

-- Eviction never takes what an eval record or metric reads (internal/eviction referencesQuery): these indexes answer
-- that check, and the foreign keys' checks, without a scan.
CREATE INDEX eval_records_scores_idx ON eval_records (scores_hash);
CREATE INDEX eval_records_hypotheses_idx ON eval_records (hypotheses_hash) WHERE hypotheses_hash IS NOT NULL;
CREATE INDEX eval_metrics_scores_idx ON eval_metrics (scores_hash);
