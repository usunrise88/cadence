-- 0044 · Owner decisions on the phase-4 gate (2026-10-04; docs/spec/00-overview.md decision log).
--
-- Retry approvals: the approval a pipeline run was started (or last continued) under, so a gpu-spend retry of the run
-- on the same UTC day inherits it while the spend stays within the estimate the person approved
-- (internal/approvals.Inherit).
ALTER TABLE pipeline_runs ADD COLUMN approval_id text REFERENCES approvals (id);
CREATE INDEX pipeline_runs_approval_idx ON pipeline_runs (approval_id) WHERE approval_id IS NOT NULL;

-- Mount content in step hashes: the fingerprint of what a step that reads a mount would read (a listing: relative
-- path, size and mtime / ETag / blob id of each file), folded into its input hash (internal/mounts.Fingerprint).
ALTER TABLE pipeline_steps ADD COLUMN mount_fingerprint text;
