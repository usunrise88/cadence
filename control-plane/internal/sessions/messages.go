package sessions

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/usunrise88/cadence/control-plane/internal/auth"
	"github.com/usunrise88/cadence/control-plane/internal/events"
	"github.com/usunrise88/cadence/control-plane/internal/storage"
)

// MessageKind is the entity kind of a transcript entry.
const MessageKind = "agent_message"

// Transcript entry kinds (the contract's AgentMessageKind).
const (
	EntryUserMessage  = "user_message"
	EntryNotice       = "notice"
	EntryAgentMessage = "agent_message"
	EntryThought      = "thought"
	EntryPlan         = "plan"
	EntryToolCall     = "tool_call"
	EntryPermission   = "permission"
	EntryTurn         = "turn"
	EntryCommit       = "commit"
)

// Delivery states of messages for the agent (user messages and notices the agent must read).
const (
	DeliveryPending   = "pending"
	DeliveryDelivered = "delivered"
)

// Message is one transcript entry. Body holds the kind's fields (text, toolCall, permission, …) exactly as the
// contract's AgentMessage names them.
type Message struct {
	ID        string
	SessionID string
	ProjectID string
	Seq       int64
	Key       string
	Kind      string
	Turn      int
	Actor     auth.Actor
	Body      map[string]any
	Delivery  string
	Rev       int
	CreatedAt time.Time
	UpdatedAt time.Time
}

// JSON renders m as the contract's AgentMessage.
func (m Message) JSON() map[string]any {
	out := make(map[string]any, len(m.Body)+10)
	for k, v := range m.Body {
		out[k] = v
	}
	out["id"], out["sessionId"], out["seq"], out["kind"], out["turn"] = m.ID, m.SessionID, m.Seq, m.Kind, m.Turn
	out["actor"], out["rev"], out["createdAt"], out["updatedAt"] = m.Actor, m.Rev, m.CreatedAt, m.UpdatedAt
	if m.Delivery != "" {
		out["delivery"] = m.Delivery
	}
	return out
}

const msgCols = `id, session_id, project_id, seq, key, kind, turn, actor, body, coalesce(delivery, ''), rev, created_at, updated_at`

func scanMessage(row pgx.CollectableRow) (Message, error) {
	var m Message
	err := row.Scan(&m.ID, &m.SessionID, &m.ProjectID, &m.Seq, &m.Key, &m.Kind, &m.Turn, &m.Actor, &m.Body, &m.Delivery,
		&m.Rev, &m.CreatedAt, &m.UpdatedAt)
	if m.Body == nil {
		m.Body = map[string]any{}
	}
	return m, err
}

// ListMessages returns up to limit entries of a session with seq > after, oldest first.
func ListMessages(ctx context.Context, q storage.Querier, sessionID string, after int64, limit int) ([]Message, error) {
	if limit <= 0 {
		limit = 200
	}
	rows, err := q.Query(ctx, "SELECT "+msgCols+` FROM agent_messages WHERE session_id = $1 AND seq > $2
		ORDER BY seq LIMIT $3`, sessionID, after, limit)
	if err != nil {
		return nil, fmt.Errorf("list transcript: %w", err)
	}
	out, err := pgx.CollectRows(rows, scanMessage)
	if err != nil {
		return nil, fmt.Errorf("read transcript: %w", err)
	}
	return out, nil
}

// messageEvent is the event of a new or changed entry on the session's topic.
func messageEvent(m Message, typ string) events.Draft {
	return events.Draft{
		Topic: Topic(m.SessionID), Type: typ, ProjectID: m.ProjectID,
		Entity:  &events.EntityRef{Kind: MessageKind, ID: m.ID, Rev: m.Rev},
		Payload: map[string]any{"message": m.JSON()},
	}
}

// entry is an entry to write.
type entry struct {
	Key      string
	Kind     string
	Turn     int
	Actor    auth.Actor
	Body     map[string]any
	Delivery string
}

// upsert writes e into the transcript of sess (whose row the caller holds locked): a new key appends an entry at the
// next seq, a known key updates it in place when its body or delivery changed. It returns the entry and its event
// (nil when nothing changed).
func upsert(ctx context.Context, tx pgx.Tx, sess Session, e entry) (Message, *events.Draft, error) {
	body, err := json.Marshal(e.Body)
	if err != nil {
		return Message{}, nil, fmt.Errorf("marshal entry %s: %w", e.Key, err)
	}
	var seq int64
	if err := tx.QueryRow(ctx, `SELECT coalesce(max(seq), 0) + 1 FROM agent_messages WHERE session_id = $1`, sess.ID).Scan(&seq); err != nil {
		return Message{}, nil, fmt.Errorf("next transcript seq: %w", err)
	}
	var inserted bool
	rows, err := tx.Query(ctx, `INSERT INTO agent_messages (id, session_id, project_id, seq, key, kind, turn, actor, body, delivery)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, NULLIF($10, ''))
		ON CONFLICT (session_id, key) DO UPDATE SET body = excluded.body, turn = excluded.turn,
			delivery = coalesce(excluded.delivery, agent_messages.delivery), rev = agent_messages.rev + 1, updated_at = now()
		WHERE agent_messages.body IS DISTINCT FROM excluded.body
			OR (excluded.delivery IS NOT NULL AND agent_messages.delivery IS DISTINCT FROM excluded.delivery)
		RETURNING `+msgCols+`, (xmax = 0)`,
		newID("msg_"), sess.ID, sess.ProjectID, seq, e.Key, e.Kind, e.Turn, e.Actor, body, e.Delivery)
	if err != nil {
		return Message{}, nil, fmt.Errorf("write transcript entry %s: %w", e.Key, err)
	}
	var (
		m     Message
		found bool
	)
	for rows.Next() {
		found = true
		if err := rows.Scan(&m.ID, &m.SessionID, &m.ProjectID, &m.Seq, &m.Key, &m.Kind, &m.Turn, &m.Actor, &m.Body,
			&m.Delivery, &m.Rev, &m.CreatedAt, &m.UpdatedAt, &inserted); err != nil {
			rows.Close()
			return Message{}, nil, fmt.Errorf("read transcript entry %s: %w", e.Key, err)
		}
	}
	if err := rows.Err(); err != nil {
		return Message{}, nil, fmt.Errorf("write transcript entry %s: %w", e.Key, err)
	}
	if !found {
		return Message{}, nil, nil // the same content again (a retried report)
	}
	typ := EventMessageUpdated
	if inserted {
		typ = EventMessageCreated
	}
	d := messageEvent(m, typ)
	return m, &d, nil
}

// entryByKey reads one entry of a session by key; ok is false when there is none.
func entryByKey(ctx context.Context, q storage.Querier, sessionID, key string) (Message, bool, error) {
	rows, err := q.Query(ctx, "SELECT "+msgCols+" FROM agent_messages WHERE session_id = $1 AND key = $2", sessionID, key)
	if err != nil {
		return Message{}, false, fmt.Errorf("read transcript entry: %w", err)
	}
	list, err := pgx.CollectRows(rows, scanMessage)
	if err != nil || len(list) == 0 {
		return Message{}, false, err
	}
	return list[0], true, nil
}

// notice appends a notice from Cadence (level info, warning or error). forAgent makes it a message the agent
// reads as its next turn (delivery pending).
func notice(ctx context.Context, tx pgx.Tx, sess Session, key, level, text string, forAgent bool) (*events.Draft, error) {
	delivery := ""
	if forAgent {
		delivery = DeliveryPending
	}
	_, d, err := upsert(ctx, tx, sess, entry{Key: key, Kind: EntryNotice, Turn: sess.Turn, Actor: System,
		Body: map[string]any{"text": text, "level": level}, Delivery: delivery})
	return d, err
}
