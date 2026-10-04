-- 0050 · Phase 5 · stream D3: deployment targets, the instance signing key, promotion records and delivery bundles
-- (docs/review/2026-10-05-phase-5-plan.md; docs/spec/02-domain-projects-registry.md "Deployment entities"; R33, R46).

-- A deployment target is where models are served (R46), instance-wide like mounts and compute. kind staging is the
-- server Cadence reaches (it has an endpoint); kind delivery is a production server only a person's delivery script
-- reaches (it never has one). config holds what promotion records name — serves, server, repositoryPath, slots — and
-- concurrency, cardClass and boost; it changes only by an approved deploymentTargets.edit (rev + 1), which on a
-- delivery target appends a target-changed record to the target's chain.
CREATE TABLE deployment_targets (
    id          text PRIMARY KEY,                    -- dtg_<uuidv7>
    name        text NOT NULL UNIQUE CHECK (name ~ '^[a-z0-9]([a-z0-9-]{0,38}[a-z0-9])?$'),
    kind        text NOT NULL CHECK (kind IN ('staging', 'delivery')),
    description text NOT NULL DEFAULT '',
    config      jsonb NOT NULL,
    state       text NOT NULL DEFAULT 'active' CHECK (state IN ('active', 'archived')),
    rev         integer NOT NULL DEFAULT 1 CHECK (rev >= 1),
    created_by  jsonb NOT NULL,
    approval_id text,
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now(),
    archived_at timestamptz,
    CHECK (kind <> 'delivery' OR coalesce(config ->> 'endpoint', '') = '')
);

-- The instance's Ed25519 signing keys: the public half and the name of the sealed secret (kind signing) holding the
-- private seed. One key is current; key rotation (cadence admin rotate-signing-key) retires it and appends a
-- key-rotation record, signed by the old key, to every delivery target's chain.
CREATE TABLE signing_keys (
    id          text PRIMARY KEY CHECK (id ~ '^ed25519:[0-9a-f]{32}$'),
    alg         text NOT NULL DEFAULT 'Ed25519' CHECK (alg = 'Ed25519'),
    public_key  bytea NOT NULL CHECK (length(public_key) = 32),
    secret_name text NOT NULL UNIQUE,
    state       text NOT NULL CHECK (state IN ('current', 'retired')),
    created_at  timestamptz NOT NULL DEFAULT now(),
    retired_at  timestamptz
);

CREATE UNIQUE INDEX signing_keys_one_current ON signing_keys ((state)) WHERE state = 'current';

-- Promotion records (R33): canonical is the record exactly as signed (RFC 8785 JSON), hash its SHA-256 (hex), the
-- signature Ed25519 over the 32 hash bytes (base64). One chain per delivery target: seq from 1, prev_hash the previous
-- record's hash (64 zeros for genesis). The columns beside canonical are an index of what it says; reads verify both.
CREATE TABLE promotion_records (
    id            text PRIMARY KEY,                  -- prm_<uuidv7>
    target_id     text NOT NULL REFERENCES deployment_targets (id),
    seq           integer NOT NULL CHECK (seq >= 1),
    kind          text NOT NULL CHECK (kind IN ('genesis', 'promotion', 'rollback', 'confirmation', 'withdrawal',
                                                'target-changed', 'key-rotation')),
    canonical     text NOT NULL,
    hash          text NOT NULL UNIQUE CHECK (hash ~ '^[0-9a-f]{64}$'),
    prev_hash     text NOT NULL CHECK (prev_hash ~ '^[0-9a-f]{64}$'),
    signature     text NOT NULL,
    key_id        text NOT NULL REFERENCES signing_keys (id),
    slot          text,
    project_id    text,
    deployment_id text,                              -- dep_… (stream D4); not part of the signed body
    refers_to     text REFERENCES promotion_records (id),
    created_at    timestamptz NOT NULL,
    UNIQUE (target_id, seq)
);

CREATE INDEX promotion_records_refers_idx ON promotion_records (refers_to) WHERE refers_to IS NOT NULL;
CREATE INDEX promotion_records_pending_idx ON promotion_records (created_at) WHERE kind IN ('promotion', 'rollback');

-- Append-only: a record is never changed or removed. (A superuser can still disable the trigger; the chain makes
-- such an edit visible: every read verifies hashes, signatures and links.)
CREATE FUNCTION promotion_records_append_only() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'promotion records are append-only (% refused)', TG_OP USING ERRCODE = 'insufficient_privilege';
END
$$;

CREATE TRIGGER promotion_records_no_update BEFORE UPDATE OR DELETE ON promotion_records
    FOR EACH ROW EXECUTE FUNCTION promotion_records_append_only();
CREATE TRIGGER promotion_records_no_truncate BEFORE TRUNCATE ON promotion_records
    FOR EACH STATEMENT EXECUTE FUNCTION promotion_records_append_only();

-- The delivery bundle of a promotion or rollback record (cadence.delivery/1), written by the delivery.build job.
-- smoke_total and smoke_required are what promotions.verify checks a receipt's <ok>/<total> against.
CREATE TABLE promotion_deliveries (
    record_id      text PRIMARY KEY REFERENCES promotion_records (id),
    state          text NOT NULL CHECK (state IN ('building', 'ready', 'failed')),
    job_id         text,
    artifact_hash  text CHECK (artifact_hash IS NULL OR artifact_hash ~ '^b3:[0-9a-f]{64}$'),
    script         text,
    smoke_total    integer CHECK (smoke_total >= 0),
    smoke_required integer CHECK (smoke_required >= 0),
    error          text,
    created_at     timestamptz NOT NULL DEFAULT now(),
    updated_at     timestamptz NOT NULL DEFAULT now()
);
