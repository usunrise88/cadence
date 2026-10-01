-- 0021 · Control-plane audit: the shared calibration cache (calibrations) answers estimates for every project, so
-- only a calibration measured by a base model's calibrate step for a known key (runs.calibrate, or the calibrate
-- step of a training run that starts from a base model) may replace an entry. Every calibration output the hook
-- sees is recorded here, with whether it was shared and, when not, why; the others never touch calibrations.
CREATE TABLE calibration_observations (
    artifact_hash     text NOT NULL,
    pipeline_run_id   text NOT NULL,
    step_id           text NOT NULL,
    project_id        text,
    run_id            text,
    base_model        text NOT NULL DEFAULT '',
    card_class        text NOT NULL DEFAULT '',
    memory_cap_gb     double precision NOT NULL DEFAULT 0,
    precision         text NOT NULL DEFAULT '',
    seconds_per_step  double precision NOT NULL CHECK (seconds_per_step > 0),
    shared            boolean NOT NULL,
    reason            text NOT NULL DEFAULT '',                 -- why it was not shared
    recorded_at       timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (artifact_hash, pipeline_run_id, step_id)
);
