-- 0010 · Agent host restarts and tool-call attribution (phase 1 punch list): a host that shuts down releases its
-- sessions (hostSessions.release) instead of leaving them assigned until its lapse; the Chat shows the session as
-- reconnecting meanwhile; opencode's MCP calls get the agent's own tool-call id after the fact.

-- When the session's host released it (host_id is then NULL) or was found silent past the lapse (host_id stays);
-- cleared when a host takes the session.
ALTER TABLE agent_sessions ADD COLUMN host_left_at timestamptz;
-- What the next host tells the agent before its next prompt (a turn interrupted by the restart, the permission
-- requests withdrawn with it); cleared when a host takes the session.
ALTER TABLE agent_sessions ADD COLUMN resume_note text;

-- Commands an MCP client sent without a tool-use id carry a synthetic one (mcp:<session>/<rpc id>) until the agent
-- host reports the agent's own tool call; these indexes find them.
CREATE INDEX audit_log_mcp_tool_call_idx ON audit_log ((actor->>'sessionId'), operation, at)
    WHERE tool_call_id LIKE 'mcp:%';
CREATE INDEX events_mcp_tool_call_idx ON events ((caused_by->>'toolCallId'))
    WHERE caused_by->>'toolCallId' LIKE 'mcp:%';
