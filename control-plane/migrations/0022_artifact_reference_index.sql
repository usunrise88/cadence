-- 0022 · Indexed reference checks for content-store eviction (docs/spec/06-platform.md "Artifacts, metrics and
-- logs", Retention). Eviction asks, for every candidate, whether a waiting or leased step job, a running pipeline
-- run, an unfinished pipeline step or a registry version names its hash anywhere in a JSON document, and whether a
-- checkpoint row holds it. Those were full scans of the documents' text; they become index lookups.

-- Every artifact hash (b3:<64 hex>) that occurs anywhere in a document's text, as strpos on the text found them:
-- a hash cannot start inside another one (':' is not a hex digit), so the non-overlapping matches are all of them.
CREATE FUNCTION artifact_hashes_in(doc jsonb) RETURNS text[]
    LANGUAGE sql IMMUTABLE STRICT PARALLEL SAFE
    RETURN ARRAY(SELECT DISTINCT m[1] FROM regexp_matches(doc::text, '(b3:[0-9a-f]{64})', 'g') AS m);

-- The partial predicates are exactly the states internal/eviction checks, so the indexes stay small.
CREATE INDEX step_jobs_artifact_hashes_idx ON step_jobs USING gin (artifact_hashes_in(spec))
    WHERE state IN ('waiting', 'leased');
CREATE INDEX pipeline_runs_artifact_hashes_idx ON pipeline_runs USING gin (artifact_hashes_in(inputs))
    WHERE state = 'running';
CREATE INDEX pipeline_steps_artifact_hashes_idx ON pipeline_steps USING gin (artifact_hashes_in(inputs))
    WHERE state IN ('waiting', 'queued', 'running');
CREATE INDEX registry_versions_artifact_hashes_idx ON registry_versions USING gin (artifact_hashes_in(payload));

CREATE INDEX checkpoints_artifact_idx ON checkpoints (artifact_hash);
