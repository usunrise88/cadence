// Package audit is the audit log: one row per committed command, written in the command's own transaction, plus
// one per denied, gated or failed attempt. It is kept one year (docs/spec/06-platform.md, Operations).
package audit

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/usunrise88/cadence/control-plane/internal/auth"
	"github.com/usunrise88/cadence/control-plane/internal/problems"
	"github.com/usunrise88/cadence/control-plane/internal/storage"
)

// Retention is how long entries are kept.
const Retention = 365 * 24 * time.Hour

// Outcomes besides a problem slug (for failed attempts).
const (
	OutcomeOK       = "ok"
	OutcomeApproval = "approval" // stored as an approval, answered 202
	OutcomeDenied   = "denied"   // refused by the policy engine
	OutcomeExpired  = "expired"  // an approval nobody decided
)

// Entry is one audit row. Its JSON form is the contract's AuditEntry.
type Entry struct {
	ID         string     `json:"id"`
	CommandID  string     `json:"commandId,omitempty"`
	Operation  string     `json:"operation"`
	Actor      auth.Actor `json:"actor"`
	Preset     string     `json:"preset,omitempty"`
	ProjectID  string     `json:"projectId,omitempty"`
	Outcome    string     `json:"outcome"`
	Status     int        `json:"status"`
	Rule       string     `json:"rule,omitempty"`
	ToolCallID string     `json:"toolCallId,omitempty"`
	ApprovalID string     `json:"approvalId,omitempty"`
	At         time.Time  `json:"at"`
}

// Write inserts e; ID and At are filled in when empty.
func Write(ctx context.Context, q storage.Querier, e Entry) error {
	if e.ID == "" {
		e.ID = "aud_" + uuid.Must(uuid.NewV7()).String()
	}
	if e.At.IsZero() {
		e.At = time.Now()
	}
	if _, err := q.Exec(ctx, `INSERT INTO audit_log (id, command_id, operation, actor, actor_id, preset, project_id,
		outcome, status, rule, tool_call_id, approval_id, at)
		VALUES ($1, NULLIF($2, ''), $3, $4, $5, NULLIF($6, ''), NULLIF($7, ''), $8, $9, NULLIF($10, ''),
		        NULLIF($11, ''), NULLIF($12, ''), $13)`,
		e.ID, e.CommandID, e.Operation, e.Actor, e.Actor.ID, e.Preset, e.ProjectID, e.Outcome, e.Status, e.Rule,
		e.ToolCallID, e.ApprovalID, e.At); err != nil {
		return fmt.Errorf("write audit entry for %s: %w", e.Operation, err)
	}
	return nil
}

// RetagToolCall gives the entries of an agent session's commands that carry the synthetic tool-call id from (an MCP
// call without a tool-use id) the agent's own id to.
func RetagToolCall(ctx context.Context, q storage.Querier, sessionID, from, to string) error {
	if _, err := q.Exec(ctx, `UPDATE audit_log SET tool_call_id = $3 WHERE actor->>'sessionId' = $1 AND tool_call_id = $2
		AND tool_call_id LIKE 'mcp:%'`, sessionID, from, to); err != nil {
		return fmt.Errorf("retag tool call %s in the audit log: %w", from, err)
	}
	return nil
}

// Filter selects entries; empty fields match everything. Before is a cursor (an entry id): only older entries.
type Filter struct {
	ActorID, ProjectID, Operation, Before string
	Limit                                 int
}

// List returns entries newest first and the cursor of the next page ("" on the last one).
func List(ctx context.Context, q storage.Querier, f Filter) ([]Entry, string, error) {
	if f.Limit <= 0 {
		f.Limit = 100
	}
	if f.Before != "" && !strings.HasPrefix(f.Before, "aud_") {
		return nil, "", problems.BadRequest.New("before %q is not a cursor from a previous page", f.Before)
	}
	var (
		where []string
		args  []any
	)
	add := func(cond string, v any) {
		args = append(args, v)
		where = append(where, fmt.Sprintf(cond, len(args)))
	}
	if f.ActorID != "" {
		add("actor_id = $%d", f.ActorID)
	}
	if f.ProjectID != "" {
		add("project_id = $%d", f.ProjectID)
	}
	if f.Operation != "" {
		add("operation = $%d", f.Operation)
	}
	if f.Before != "" {
		add("id < $%d", f.Before)
	}
	sql := `SELECT id, coalesce(command_id, ''), operation, actor, coalesce(preset, ''), coalesce(project_id, ''),
		outcome, status, coalesce(rule, ''), coalesce(tool_call_id, ''), coalesce(approval_id, ''), at FROM audit_log`
	if len(where) > 0 {
		sql += " WHERE " + strings.Join(where, " AND ")
	}
	args = append(args, f.Limit+1)
	sql += fmt.Sprintf(" ORDER BY id DESC LIMIT $%d", len(args))
	rows, err := q.Query(ctx, sql, args...)
	if err != nil {
		return nil, "", fmt.Errorf("list audit log: %w", err)
	}
	out, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (Entry, error) {
		var e Entry
		err := row.Scan(&e.ID, &e.CommandID, &e.Operation, &e.Actor, &e.Preset, &e.ProjectID, &e.Outcome, &e.Status,
			&e.Rule, &e.ToolCallID, &e.ApprovalID, &e.At)
		return e, err
	})
	if err != nil {
		return nil, "", fmt.Errorf("read audit log: %w", err)
	}
	next := ""
	if len(out) > f.Limit {
		out = out[:f.Limit]
		next = out[len(out)-1].ID
	}
	return out, next, nil
}

// Prune deletes entries older than Retention before now; it returns how many.
func Prune(ctx context.Context, q storage.Querier, now time.Time) (int64, error) {
	tag, err := q.Exec(ctx, "DELETE FROM audit_log WHERE at < $1", now.Add(-Retention))
	if err != nil {
		return 0, fmt.Errorf("prune audit log: %w", err)
	}
	return tag.RowsAffected(), nil
}
