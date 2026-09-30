-- 0009 · Phase 1 (worktree watcher): the uncommitted changes the agent host last reported for a session's worktree
-- while a turn runs (paths, status and sizes; no content). NULL when the worktree is clean or the session ended
-- (docs/spec/05-agents.md "Worktree, drafts and merge").
ALTER TABLE agent_sessions ADD COLUMN working jsonb;
