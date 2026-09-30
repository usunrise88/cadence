-- 0009 · Agent credentials: the agents' own model accounts (the Claude Code subscription token, opencode's
-- providers), configured instance-wide from Settings → Agents instead of `agent-host login`
-- (docs/spec/08-resolutions.md R3, R6). Metadata only: a submitted value waits encrypted in the secret store's
-- transit area until the agent host writes it into the agent-credentials volume and acknowledges; then it is deleted.

-- The egress proxy authenticates with its own credential kind (cep_ tokens): it reads the allowlist and nothing else.
ALTER TABLE credentials DROP CONSTRAINT credentials_kind_check;
ALTER TABLE credentials ADD CONSTRAINT credentials_kind_check
    CHECK (kind IN ('session', 'api_key', 'agent', 'agent_host', 'egress_proxy', 'invitation', 'worker'));

CREATE TABLE agent_credentials (
    id              text PRIMARY KEY,                           -- claude-code | opencode.<provider>
    agent           text NOT NULL CHECK (agent IN ('claude-code', 'opencode')),
    provider        text NOT NULL,                              -- the provider id in the agent's own config
    catalogue_id    text NOT NULL,                              -- claude-subscription, minimax, …, openai-compatible
    name            text NOT NULL,
    base_url        text,                                       -- custom (OpenAI-compatible) providers only
    has_value       boolean NOT NULL DEFAULT false,
    hint            text NOT NULL DEFAULT '',                   -- last four characters of the value, never more
    set_at          timestamptz,
    set_by          jsonb,                                      -- auth.Actor
    expires_at      timestamptz,                                -- expected expiry (Claude's setup-token: ~1 year)
    delivery_state  text NOT NULL DEFAULT 'pending'
        CHECK (delivery_state IN ('pending', 'written', 'removing', 'removed', 'failed')),
    delivery_detail text NOT NULL DEFAULT '',
    delivery_at     timestamptz,
    verify_state    text NOT NULL DEFAULT 'none' CHECK (verify_state IN ('none', 'pending', 'ok', 'failed')),
    verify_detail   text NOT NULL DEFAULT '',
    verify_model    text NOT NULL DEFAULT '',
    verified_at     timestamptz,
    models          jsonb NOT NULL DEFAULT '[]',                -- opencode: provider/model ids from the last check
    default_model   text,                                       -- opencode: the model new projects default to
    rev             integer NOT NULL DEFAULT 1 CHECK (rev >= 1),
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now(),
    archived_at     timestamptz
);

-- One default model per agent.
CREATE UNIQUE INDEX agent_credentials_default_idx ON agent_credentials (agent)
    WHERE default_model IS NOT NULL AND archived_at IS NULL;

-- What the agent host has to do: write a value (waiting in the transit store under the task id), remove a
-- credential from the volume, or verify one. A claimed task that is not acknowledged is offered again.
CREATE TABLE agent_credential_tasks (
    id            text PRIMARY KEY,                             -- act_<uuidv7>, also the transit name of its value
    credential_id text NOT NULL REFERENCES agent_credentials (id),
    action        text NOT NULL CHECK (action IN ('write', 'remove', 'verify')),
    transit       boolean NOT NULL DEFAULT false,               -- a value waits in the transit store
    created_at    timestamptz NOT NULL DEFAULT now(),
    claimed_by    text,
    claimed_at    timestamptz,
    done_at       timestamptz,
    outcome       text CHECK (outcome IN ('ok', 'failed', 'superseded'))
);

CREATE INDEX agent_credential_tasks_open_idx ON agent_credential_tasks (created_at) WHERE done_at IS NULL;
CREATE INDEX agent_credential_tasks_credential_idx ON agent_credential_tasks (credential_id, created_at);
