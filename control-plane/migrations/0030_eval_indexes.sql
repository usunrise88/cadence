-- 0030 · Phase 3 audit fixes (eval correctness): indexes for the eval-record cache.
-- A record is found from its cells (evals.get, models.register) and from its normalizer version (the registry's
-- "used by"); eval_cells_eval_idx repeated the UNIQUE (eval_id, position) index and only cost writes.
CREATE INDEX eval_cells_record_idx ON eval_cells (record_id) WHERE record_id IS NOT NULL;
CREATE INDEX eval_records_normalizer_idx ON eval_records (normalizer_version_id);
DROP INDEX eval_cells_eval_idx;
