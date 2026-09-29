-- 0003 · Phase 1 registry core: collections and immutable versions, per-project adoptions and aliases, compute
-- hosts, secret metadata and instance-wide policies. Bundled registry versions and the compute hosts of
-- defaults.yaml are seeded at start by the control plane (internal/registry, internal/compute), not here.

-- A collection is a named series of versions of one kind: dataset/fleurs-he-smoke, base-model/…, template/….
CREATE TABLE registry_collections (
    id          text PRIMARY KEY,                 -- reg_<uuidv7>
    kind        text NOT NULL,                    -- base_model | dataset_version | template (checked in Go)
    name        text NOT NULL UNIQUE,
    description text NOT NULL DEFAULT '',
    tags        text[] NOT NULL DEFAULT '{}',
    licence     text NOT NULL DEFAULT '',
    created_by  jsonb NOT NULL,
    created_at  timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX registry_collections_kind_idx ON registry_collections (kind, name);

-- Versions are immutable once frozen (trigger below); only aliases move. The version string is
-- YYYY-MM-DD.<first 12 hex digits of the fingerprint>, assigned by the control plane.
CREATE TABLE registry_versions (
    id            text PRIMARY KEY,               -- ver_<uuidv7>
    collection_id text NOT NULL REFERENCES registry_collections (id),
    version       text NOT NULL CHECK (version ~ '^[0-9]{4}-[0-9]{2}-[0-9]{2}\.[0-9a-f]{12}$'),
    fingerprint   text NOT NULL CHECK (fingerprint ~ '^[0-9a-f]{64}$'),
    state         text NOT NULL DEFAULT 'draft' CHECK (state IN ('draft', 'frozen', 'deprecated')),
    payload       jsonb NOT NULL,
    created_by    jsonb NOT NULL,
    created_at    timestamptz NOT NULL DEFAULT now(),
    frozen_at     timestamptz,
    deprecated_at timestamptz,
    UNIQUE (collection_id, version),
    UNIQUE (collection_id, fingerprint)
);

CREATE INDEX registry_versions_collection_idx ON registry_versions (collection_id, created_at DESC);

-- Frozen and deprecated versions never change and are never deleted; the only moves are draft → frozen and
-- frozen → deprecated.
CREATE FUNCTION registry_versions_immutable() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP = 'DELETE' THEN
        IF OLD.state <> 'draft' THEN
            RAISE EXCEPTION 'registry version % is %: versions are never deleted once frozen', OLD.id, OLD.state
                USING ERRCODE = 'integrity_constraint_violation';
        END IF;
        RETURN OLD;
    END IF;
    IF OLD.state <> 'draft' AND (NEW.id, NEW.collection_id, NEW.version, NEW.fingerprint, NEW.payload, NEW.created_by,
            NEW.created_at, NEW.frozen_at)
        IS DISTINCT FROM (OLD.id, OLD.collection_id, OLD.version, OLD.fingerprint, OLD.payload, OLD.created_by,
            OLD.created_at, OLD.frozen_at) THEN
        RAISE EXCEPTION 'registry version % is %: its content never changes; register a new version', OLD.id, OLD.state
            USING ERRCODE = 'integrity_constraint_violation';
    END IF;
    IF NEW.state <> OLD.state AND NOT ((OLD.state = 'draft' AND NEW.state = 'frozen')
            OR (OLD.state = 'frozen' AND NEW.state = 'deprecated')) THEN
        RAISE EXCEPTION 'registry version % cannot go from % to %', OLD.id, OLD.state, NEW.state
            USING ERRCODE = 'integrity_constraint_violation';
    END IF;
    RETURN NEW;
END
$$;

CREATE TRIGGER registry_versions_immutable BEFORE UPDATE OR DELETE ON registry_versions
    FOR EACH ROW EXECUTE FUNCTION registry_versions_immutable();

-- A project adopts registry versions by reference; "used by" reads this table.
CREATE TABLE adoptions (
    project_id text NOT NULL REFERENCES projects (id),
    version_id text NOT NULL REFERENCES registry_versions (id),
    adopted_by jsonb NOT NULL,
    adopted_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (project_id, version_id)
);

CREATE INDEX adoptions_version_idx ON adoptions (version_id);

-- Per-project pointers (@train-current, @baseline, @production, …) at adopted versions.
CREATE TABLE aliases (
    id         text PRIMARY KEY,                  -- als_<uuidv7>
    project_id text NOT NULL REFERENCES projects (id),
    name       text NOT NULL,
    version_id text NOT NULL,
    rev        integer NOT NULL DEFAULT 1 CHECK (rev >= 1),
    set_by     jsonb NOT NULL,
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (project_id, name),
    FOREIGN KEY (project_id, version_id) REFERENCES adoptions (project_id, version_id)
);

-- Hosts and their cards; the queue schedules against them from phase 2.
CREATE TABLE compute_hosts (
    id          text PRIMARY KEY,                 -- cmp_<uuidv7>
    name        text NOT NULL UNIQUE,
    description text NOT NULL DEFAULT '',
    cards       jsonb NOT NULL,
    health      jsonb NOT NULL DEFAULT '{"state": "unknown"}',
    rev         integer NOT NULL DEFAULT 1 CHECK (rev >= 1),
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now()
);

-- Secret metadata only (R9): the value lives encrypted in the control plane's data directory, never here.
CREATE TABLE secrets (
    id           text PRIMARY KEY,                -- sec_<uuidv7>
    name         text NOT NULL UNIQUE,
    kind         text NOT NULL,
    scope        text NOT NULL DEFAULT 'instance',
    rev          integer NOT NULL DEFAULT 1 CHECK (rev >= 1),
    created_by   jsonb NOT NULL,
    created_at   timestamptz NOT NULL DEFAULT now(),
    last_used_at timestamptz
);

-- Instance-wide policies: one row holding only the values that depart from defaults.yaml.
CREATE TABLE policies (
    id         text PRIMARY KEY CHECK (id = 'instance'),
    budgets    jsonb NOT NULL DEFAULT '{}',
    rev        integer NOT NULL DEFAULT 1 CHECK (rev >= 1),
    updated_at timestamptz NOT NULL DEFAULT now()
);

INSERT INTO policies (id) VALUES ('instance');
