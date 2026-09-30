-- 0008 · Phase 1 wave 2 (agent sessions stream): agent sessions, their transcripts, the requests people make of
-- the agent host (cancel, pause, resume, end), agent-permission approvals and the agent host credential
-- (docs/spec/05-agents.md "Session lifecycle"; docs/spec/08-resolutions.md R2–R6).

-- The agent host authenticates with its own credential kind (cah_ tokens): it claims work and reports transcripts,
-- and reaches nothing else.
ALTER TABLE credentials DROP CONSTRAINT credentials_kind_check;
ALTER TABLE credentials ADD CONSTRAINT credentials_kind_check
    CHECK (kind IN ('session', 'api_key', 'agent', 'agent_host', 'invitation', 'worker'));

-- One agent conversation: driver, model, branch session/<id>, state, budget and use. `host_id` is the host process
-- that runs it; a host that stops claiming loses its sessions to the next one (resume).
CREATE TABLE agent_sessions (
    id              text PRIMARY KEY,                           -- ses_<uuidv7>
    project_id      text NOT NULL REFERENCES projects (id),
    number          integer NOT NULL CHECK (number >= 1),       -- ordinal in the project
    kind            text NOT NULL CHECK (kind IN ('interactive', 'read-only')),
    driver          text NOT NULL CHECK (driver IN ('claude-code', 'opencode')),
    model           text NOT NULL,
    preset          text NOT NULL,
    state           text NOT NULL DEFAULT 'created'
                    CHECK (state IN ('created', 'running', 'waiting_approval', 'paused', 'done', 'failed', 'cancelled')),
    busy            boolean NOT NULL DEFAULT false,             -- a turn is in progress
    turn            integer NOT NULL DEFAULT 0,
    pause_reason    jsonb,                                      -- {code, message}
    error           text,
    branch          text NOT NULL DEFAULT '',                   -- session/<id>; empty for read-only sessions
    merge           jsonb NOT NULL DEFAULT '{"state":"none"}',  -- {state, commit, head, conflicts, fastForward, at, by}
    auto_merge      text NOT NULL CHECK (auto_merge IN ('when-clean', 'never')),
    budget          jsonb NOT NULL,                             -- {turns, tokens, tokensPerTurn}
    use             jsonb NOT NULL DEFAULT '{"turns":0,"inputTokens":0,"outputTokens":0}',
    prompt          text,
    refs            jsonb NOT NULL DEFAULT '[]',
    started_by      jsonb NOT NULL,
    credential_id   text,                                       -- the current cst_ token (revoked at the end)
    acp_session_id  text,                                       -- the agent's own session id, for ACP resume
    host_id         text,
    rev             integer NOT NULL DEFAULT 1 CHECK (rev >= 1),
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now(),
    started_at      timestamptz,
    ended_at        timestamptz,
    last_message_at timestamptz,                                -- the idle clock (R5) runs from here
    UNIQUE (project_id, number)
);

CREATE INDEX agent_sessions_project_idx ON agent_sessions (project_id, updated_at DESC);
CREATE INDEX agent_sessions_live_idx ON agent_sessions (state) WHERE state IN ('created', 'running', 'waiting_approval', 'paused');

-- The transcript: user messages, notices, coalesced agent messages and thoughts, plans, tool calls (one row per
-- call, updated as its status changes), permission requests, turns and commits. `key` is the host's key for the
-- entries it reports (upserts) or the server's for its own.
CREATE TABLE agent_messages (
    id          text PRIMARY KEY,                               -- msg_<uuidv7>
    session_id  text NOT NULL REFERENCES agent_sessions (id),
    project_id  text NOT NULL REFERENCES projects (id),
    seq         bigint NOT NULL,                                -- order in the session
    key         text NOT NULL,
    kind        text NOT NULL CHECK (kind IN ('user_message', 'notice', 'agent_message', 'thought', 'plan',
                                              'tool_call', 'permission', 'turn', 'commit')),
    turn        integer NOT NULL DEFAULT 0,
    actor       jsonb NOT NULL,
    body        jsonb NOT NULL,                                 -- the kind's fields (text, toolCall, permission, …)
    delivery    text CHECK (delivery IN ('pending', 'delivered')),
    rev         integer NOT NULL DEFAULT 1 CHECK (rev >= 1),
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now(),
    UNIQUE (session_id, seq),
    UNIQUE (session_id, key)
);

CREATE INDEX agent_messages_pending_idx ON agent_messages (session_id, seq) WHERE delivery = 'pending';
CREATE INDEX agent_messages_turns_idx ON agent_messages (project_id, created_at) WHERE kind = 'turn';

-- What people asked the agent host to do; the owning host takes each once (claim).
CREATE TABLE agent_session_controls (
    id           text PRIMARY KEY,                              -- ctl_<uuidv7>
    session_id   text NOT NULL REFERENCES agent_sessions (id),
    action       text NOT NULL CHECK (action IN ('cancel', 'pause', 'resume', 'end')),
    reason       jsonb,
    budget       jsonb,
    actor        jsonb NOT NULL,
    created_at   timestamptz NOT NULL DEFAULT now(),
    delivered_at timestamptz
);

CREATE INDEX agent_session_controls_pending_idx ON agent_session_controls (session_id, created_at) WHERE delivered_at IS NULL;

-- Approvals learn their kind: a gated command (replayed when approved) or an ACP permission request of an agent
-- session (answered to the agent, never replayed).
ALTER TABLE approvals ADD COLUMN kind text NOT NULL DEFAULT 'command' CHECK (kind IN ('command', 'agent_permission'));
ALTER TABLE approvals ADD COLUMN permission jsonb;             -- {sessionId, toolCallId, title, class, options, …}
ALTER TABLE approvals ADD COLUMN delivered_at timestamptz;     -- agent_permission: the host took the decision

CREATE INDEX approvals_session_pending_idx ON approvals (session_id) WHERE state = 'pending';
CREATE INDEX approvals_undelivered_idx ON approvals (session_id)
    WHERE kind = 'agent_permission' AND state <> 'pending' AND delivered_at IS NULL;

-- Agent host processes and when they last claimed work or reported; a session whose host is silent is resumed by
-- another.
CREATE TABLE agent_hosts (
    id            text PRIMARY KEY,                             -- the host's own id (hostname and boot)
    credential_id text NOT NULL,
    version       text NOT NULL DEFAULT '',
    started_at    timestamptz NOT NULL DEFAULT now(),
    seen_at       timestamptz NOT NULL DEFAULT now()
);
