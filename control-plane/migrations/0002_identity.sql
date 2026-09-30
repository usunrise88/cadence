-- 0002 · Phase 1 identity: the admin's password and second factor, and one table for every credential
-- (docs/spec/06-platform.md "Authentication and access").

-- usr_admin (seeded by 0001) stays the admin account; first start sets its password (auth.setup).
ALTER TABLE users
    ADD COLUMN password_hash       text,                          -- Argon2id, PHC string; NULL until first start
    ADD COLUMN password_changed_at timestamptz,
    ADD COLUMN totp_secret         text,                          -- base32; pending until totp_enabled
    ADD COLUMN totp_enabled        boolean NOT NULL DEFAULT false,
    ADD COLUMN totp_last_step      bigint NOT NULL DEFAULT 0;     -- last accepted RFC 6238 step (no replay)

-- Sessions, API keys, agent session tokens, reviewer invitations and worker leases. Only the SHA-256 of a token
-- is stored; the token itself is shown once.
CREATE TABLE credentials (
    id           text PRIMARY KEY,                                -- crd_<uuidv7>
    kind         text NOT NULL CHECK (kind IN ('session', 'api_key', 'agent', 'invitation', 'worker')),
    user_id      text REFERENCES users (id),                      -- the session's user, the key's owner
    subject      text,                                            -- agent session id, invitation batch, worker id
    name         text NOT NULL DEFAULT '',
    scope        jsonb NOT NULL DEFAULT '{}',                     -- auth.Scope: all, projectId, registryRead, preset
    token_hash   text NOT NULL UNIQUE,                            -- hex SHA-256 of the token
    rev          integer NOT NULL DEFAULT 1 CHECK (rev >= 1),
    created_at   timestamptz NOT NULL DEFAULT now(),
    expires_at   timestamptz,                                     -- NULL: until revoked
    last_used_at timestamptz,                                     -- updated at most once a minute
    revoked_at   timestamptz
);

CREATE INDEX credentials_kind_idx ON credentials (kind, created_at DESC);
CREATE INDEX credentials_subject_idx ON credentials (subject) WHERE subject IS NOT NULL;
