-- 0019 · Phase 2 gate fixes (stream G2): the fixed time a training lease adds to its steps (model and state load,
-- final validation, state and checkpoint saves), measured by the calibrate step (meta leaseOverheadSeconds). Null
-- when the calibration measured none: the estimate then takes estimates.lease_overhead_seconds (R12).
ALTER TABLE calibrations ADD COLUMN lease_overhead_seconds double precision CHECK (lease_overhead_seconds >= 0);
