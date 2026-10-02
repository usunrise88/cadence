-- 0023 · Phase 3 · stream G: golden sets and the leakage checks (docs/review/2026-10-02-phase-3-plan.md;
-- docs/spec/02-domain-projects-registry.md "Adoption re-runs the leakage check"; docs/spec/04-blocks.md Block 3).
--
-- A golden set is a registry version (kind golden_set, collection golden-set/<name>, frozen by goldenSets.freeze
-- after the admin's approval). This table ties it to the eval-only dataset version whose utterances it holds and to
-- the scoring normalizer version it is scored with, so the training-side checks find every golden utterance through
-- dataset_utterances (and its fingerprints through utterance_fingerprints) with index lookups only. Rows are written
-- once, in the freeze's transaction, and never change: frozen versions are immutable.
CREATE TABLE golden_sets (
    version_id            text PRIMARY KEY REFERENCES registry_versions (id),
    dataset_version_id    text NOT NULL REFERENCES registry_versions (id),
    normalizer_version_id text NOT NULL REFERENCES registry_versions (id),
    created_at            timestamptz NOT NULL DEFAULT now()
);

-- Which golden sets a dataset version backs (the training exclusion joins memberships to it).
CREATE INDEX golden_sets_dataset_idx ON golden_sets (dataset_version_id);

-- The overlap queries go from an utterance to the dataset versions holding it and filter those versions: covering the
-- version id lets the membership lookup answer from the index alone (dataset_utterances_utterance_idx has only the
-- utterance id).
CREATE INDEX dataset_utterances_utterance_version_idx ON dataset_utterances (utterance_id, version_id);
