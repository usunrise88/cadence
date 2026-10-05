-- 0047 · Phase 4 tail · stream infra: project bundles (docs/spec/03-pipelines-defaults.md "Interoperability";
-- ROADMAP "Phase 4 notes").
--
-- projects.export writes a whole project — its repository at a commit, data.lock and every registry version it
-- references with their content — to a directory on a writable mount. It is an export like the others (exports.list
-- and exports.get show it, format cadence-project-bundle), but a control-plane job writes it, not a pipeline run: the
-- row carries its own state, error, job and commit, and version_id stays NULL. Rows of the other formats keep reading
-- their state from their pipeline run (state stays NULL).
ALTER TABLE dataset_exports DROP CONSTRAINT dataset_exports_format_check;
ALTER TABLE dataset_exports ADD CONSTRAINT dataset_exports_format_check
    CHECK (format IN ('lhotse-shar', 'nemo-manifest', 'cadence-bundle', 'hf-hub', 'cadence-project-bundle'));

ALTER TABLE dataset_exports
    ADD COLUMN job_id   text,
    ADD COLUMN state    text CHECK (state IN ('running', 'done', 'failed')),
    ADD COLUMN error    text,
    ADD COLUMN commit_sha text,
    ADD COLUMN versions integer NOT NULL DEFAULT 0 CHECK (versions >= 0);
