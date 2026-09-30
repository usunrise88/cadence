-- 0017 · Phase 2 wave 2 (stream K): playbook sessions (docs/spec/08-resolutions.md R16; docs/spec/05-agents.md
-- "Playbooks and schedules as sessions"). A playbook session is an agent session of kind `playbook`; its playbook —
-- name, template version, resolved inputs, the estimate, the plan with the state of each step, the spending
-- operations dry-run in the session, the stop and the summary — is one JSON document on the session row, changed
-- only by the server when the session's commands succeed (internal/playbooks).

ALTER TABLE agent_sessions DROP CONSTRAINT agent_sessions_kind_check;
ALTER TABLE agent_sessions ADD CONSTRAINT agent_sessions_kind_check
    CHECK (kind IN ('interactive', 'read-only', 'playbook'));

ALTER TABLE agent_sessions ADD COLUMN playbook jsonb;
ALTER TABLE agent_sessions ADD CONSTRAINT agent_sessions_playbook_kind_check
    CHECK ((kind = 'playbook') = (playbook IS NOT NULL));
