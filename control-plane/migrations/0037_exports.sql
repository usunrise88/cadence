-- 0037 · Phase 4 · stream I: dataset exports (docs/review/2026-10-03-phase-4-plan.md "Waves", stream I;
-- docs/spec/03-pipelines-defaults.md "Interoperability").
--
-- An export is work: a pipeline run in a project (shar_export, dataset_export or hf_push) that reads a frozen dataset
-- version and writes Lhotse Shar, a NeMo manifest, a Cadence bundle or an audiofolder pushed to the Hugging Face Hub
-- — to a directory on a writable mount, or into the content store. datasets.export inserts the row with the run; the
-- `export` output hook completes it (files, bytes, Hub commit) and records the blobs it placed on a mount unchanged in
-- blob_copies (migration 0033), so the cache may evict the version. The state is read from the pipeline run, so a
-- failed or cancelled run needs no write here. An export step a project pipeline runs on its own gets its row from the
-- hook (version_id NULL when no version registers its input).
CREATE TABLE dataset_exports (
    id              text PRIMARY KEY,                     -- dex_<uuidv7>
    version_id      text REFERENCES registry_versions (id),
    project_id      text NOT NULL REFERENCES projects (id),
    format          text NOT NULL CHECK (format IN ('lhotse-shar', 'nemo-manifest', 'cadence-bundle', 'hf-hub')),
    target          text NOT NULL CHECK (target <> ''),   -- cas | mount://<name>/<dir> | hf://datasets/<org>/<name>
    pipeline_run_id text REFERENCES pipeline_runs (id),
    step_id         text NOT NULL DEFAULT '',             -- the export step in the run (export)
    step_kind       text NOT NULL DEFAULT '',             -- kind@version
    artifact        text,                                 -- the export artifact once the step is done
    files           integer NOT NULL DEFAULT 0 CHECK (files >= 0),
    bytes           bigint NOT NULL DEFAULT 0 CHECK (bytes >= 0),
    copies          integer NOT NULL DEFAULT 0 CHECK (copies >= 0),
    sample          jsonb NOT NULL DEFAULT '[]',          -- the first files written: [{path, hash, bytes}]
    hub             jsonb,                                -- {repo, commit, url, private}
    created_by      jsonb NOT NULL,
    approval_id     text,
    rev             integer NOT NULL DEFAULT 1 CHECK (rev >= 1),
    created_at      timestamptz NOT NULL DEFAULT now(),
    finished_at     timestamptz,
    UNIQUE (pipeline_run_id, step_id)
);

CREATE INDEX dataset_exports_project_idx ON dataset_exports (project_id, created_at DESC);
CREATE INDEX dataset_exports_version_idx ON dataset_exports (version_id) WHERE version_id IS NOT NULL;
