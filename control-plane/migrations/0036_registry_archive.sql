-- 0036 · Phase 4 · stream R: the registry in full — soft delete of registry versions
-- (docs/spec/02-domain-projects-registry.md "Registry": "a registry version referenced by anything cannot be deleted;
-- otherwise admin-only soft delete"; docs/review/2026-10-03-phase-4-plan.md "Waves", stream R).
--
-- Versions are never deleted once frozen (migration 0003). versions.archive moves a version nothing uses to the new
-- terminal state 'archived': its content, fingerprint and lineage stay; adoption, aliases and Latest skip it.

ALTER TABLE registry_versions DROP CONSTRAINT registry_versions_state_check;
ALTER TABLE registry_versions ADD CONSTRAINT registry_versions_state_check
    CHECK (state IN ('draft', 'frozen', 'deprecated', 'archived'));
ALTER TABLE registry_versions ADD COLUMN archived_at timestamptz;
ALTER TABLE registry_versions ADD COLUMN archived_by jsonb;

-- The moves: draft → frozen, frozen → deprecated, and any state → archived (terminal). Content never changes once a
-- version left draft, and archiving a draft does not change it either; archived_at and archived_by are set once.
CREATE OR REPLACE FUNCTION registry_versions_immutable() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP = 'DELETE' THEN
        IF OLD.state <> 'draft' THEN
            RAISE EXCEPTION 'registry version % is %: versions are never deleted once frozen', OLD.id, OLD.state
                USING ERRCODE = 'integrity_constraint_violation';
        END IF;
        RETURN OLD;
    END IF;
    IF (OLD.state <> 'draft' OR NEW.state = 'archived') AND (NEW.id, NEW.collection_id, NEW.version, NEW.fingerprint,
            NEW.payload, NEW.created_by, NEW.created_at, NEW.frozen_at)
        IS DISTINCT FROM (OLD.id, OLD.collection_id, OLD.version, OLD.fingerprint, OLD.payload, OLD.created_by,
            OLD.created_at, OLD.frozen_at) THEN
        RAISE EXCEPTION 'registry version % is %: its content never changes; register a new version', OLD.id, OLD.state
            USING ERRCODE = 'integrity_constraint_violation';
    END IF;
    IF OLD.state = 'archived' AND (NEW.archived_at, NEW.archived_by, NEW.deprecated_at)
            IS DISTINCT FROM (OLD.archived_at, OLD.archived_by, OLD.deprecated_at) THEN
        RAISE EXCEPTION 'registry version % is archived: it never changes again', OLD.id
            USING ERRCODE = 'integrity_constraint_violation';
    END IF;
    IF NEW.state <> OLD.state AND NOT ((OLD.state = 'draft' AND NEW.state = 'frozen')
            OR (OLD.state = 'frozen' AND NEW.state = 'deprecated')
            OR (OLD.state <> 'archived' AND NEW.state = 'archived')) THEN
        RAISE EXCEPTION 'registry version % cannot go from % to %', OLD.id, OLD.state, NEW.state
            USING ERRCODE = 'integrity_constraint_violation';
    END IF;
    RETURN NEW;
END
$$;
