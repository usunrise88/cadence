package sessions

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/usunrise88/cadence/control-plane/internal/audit"
	"github.com/usunrise88/cadence/control-plane/internal/events"
	"github.com/usunrise88/cadence/control-plane/internal/mcp"
)

// AttributionWindow is how long after a command the agent's report of the tool call behind it may still give the
// command the agent's own tool-call id.
const AttributionWindow = 2 * time.Minute

// ToolCallRetagger renames a tool-call id in what other packages keep (drafts and the draftable kinds' revisions)
// and returns the events that tell open panels.
type ToolCallRetagger interface {
	RetagToolCall(ctx context.Context, tx pgx.Tx, from, to string) ([]events.Draft, error)
}

// attribute links the agent's own tool-call id to the command an MCP call caused when the MCP client sent no
// tool-use id (opencode): the command then carries a synthetic id (mcp:<session>/<rpc id>), and the attribution
// badge could not find the tool call in Chat. When the host reports a completed Cadence tool call, the oldest
// command of the same operation by the same session within AttributionWindow that still has a synthetic id gets the
// agent's id — in the audit log, the outbox events, the session's permission entries, drafts and revisions — and
// open panels are told. Claude sends its tool-use id, which already is the transcript's id: nothing to do.
func (s *Service) attribute(ctx context.Context, tx pgx.Tx, sess Session, body map[string]any) ([]events.Draft, error) {
	tc, _ := body["toolCall"].(map[string]any)
	str := func(k string) string { v, _ := tc[k].(string); return v }
	id, op := str("id"), operationOf(str("operation"))
	if tc == nil || str("class") != "mcp" || str("status") != "completed" || op == "" || id == "" ||
		strings.HasPrefix(id, mcp.SyntheticToolCallPrefix) {
		return nil, nil
	}
	since := s.now().Add(-AttributionWindow)
	var from string
	err := tx.QueryRow(ctx, `SELECT tool_call_id FROM audit_log WHERE actor->>'sessionId' = $1 AND operation = $2
		AND at >= $3 AND tool_call_id LIKE 'mcp:%' ORDER BY id LIMIT 1`, sess.ID, op, since).Scan(&from)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("find the command of tool call %s: %w", id, err)
	}
	// A tool call is attributed once: a later report of the same call must not take the next command's id.
	var taken bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM audit_log WHERE at >= $1 AND tool_call_id = $2
		AND actor->>'sessionId' = $3)`, since.Add(-AttributionWindow), id, sess.ID).Scan(&taken); err != nil {
		return nil, fmt.Errorf("check tool call %s: %w", id, err)
	}
	if taken {
		return nil, nil
	}
	return s.retag(ctx, tx, sess, from, id)
}

// retag gives the commands with the synthetic tool-call id from the agent's id to.
func (s *Service) retag(ctx context.Context, tx pgx.Tx, sess Session, from, to string) ([]events.Draft, error) {
	if err := audit.RetagToolCall(ctx, tx, sess.ID, from, to); err != nil {
		return nil, err
	}
	if err := events.RetagToolCall(ctx, tx, from, to); err != nil {
		return nil, err
	}
	rows, err := tx.Query(ctx, `UPDATE agent_messages SET body = jsonb_set(body, '{permission,toolCallId}', to_jsonb($3::text)),
		rev = rev + 1, updated_at = now()
		WHERE session_id = $1 AND kind = 'permission' AND body->'permission'->>'toolCallId' = $2
		RETURNING `+msgCols, sess.ID, from, to)
	if err != nil {
		return nil, fmt.Errorf("retag the permission entries of %s: %w", sess.ID, err)
	}
	list, err := pgx.CollectRows(rows, scanMessage)
	if err != nil {
		return nil, fmt.Errorf("read the retagged permission entries: %w", err)
	}
	var drafts []events.Draft
	for _, m := range list {
		drafts = append(drafts, messageEvent(m, EventMessageUpdated))
	}
	if s.Attribution != nil {
		more, err := s.Attribution.RetagToolCall(ctx, tx, from, to)
		if err != nil {
			return nil, err
		}
		drafts = append(drafts, more...)
	}
	s.log().InfoContext(ctx, "tool call attributed", "session", sess.ID, "from", from, "toolCallId", to)
	return drafts, nil
}
