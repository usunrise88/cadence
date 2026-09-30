// Package events is the transactional outbox, its dispatcher and the event stream served over SSE.
//
// A command appends its events in the same transaction as its change (Append). The Dispatcher reads committed
// rows in seq order and publishes them to the in-memory Hub; Stream serves one SSE client, replaying from the
// table and then following the hub.
package events

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/usunrise88/cadence/control-plane/internal/auth"
	"github.com/usunrise88/cadence/control-plane/internal/storage"
)

// outboxLockKey is the pg_advisory_xact_lock key taken right before inserting events ("cadence-outbox").
//
// Why: seq comes from a sequence, so without the lock a transaction could take seq 7, another take seq 8 and
// commit first; a reader that already saw 8 would skip 7 forever. Holding one transaction-scoped lock from the
// first insert to commit serialises the tail of event-writing transactions, so commit order equals seq order and
// "every committed seq ≤ N is visible once N is" holds. The Dispatcher and Last-Event-ID resume rely on it.
// Only the short outbox tail is serialised: the command's own work runs before the lock is taken.
const outboxLockKey int64 = 0x636164656e63_02

// EntityRef names the entity revision an event is about.
type EntityRef struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
	Rev  int    `json:"rev"`
}

// CausedBy links an event to the command (and, for agents, the tool call) that produced it, to the approval a
// person granted for it, and to the draft a person accepted.
type CausedBy struct {
	CommandID  string `json:"commandId"`
	ToolCallID string `json:"toolCallId,omitempty"`
	ApprovalID string `json:"approvalId,omitempty"`
	DraftID    string `json:"draftId,omitempty"`
}

// Draft is an event a command wants to emit; Append fills in actor, cause, seq and time.
type Draft struct {
	Topic     string
	Type      string
	ProjectID string // empty for registry events
	Entity    *EntityRef
	Payload   any
}

// Record is a committed event. Its JSON form is the contract's CadenceEvent.
type Record struct {
	Seq       int64           `json:"seq"`
	Topic     string          `json:"topic"`
	Type      string          `json:"type"`
	ProjectID string          `json:"projectId,omitempty"`
	Entity    *EntityRef      `json:"entity,omitempty"`
	Actor     auth.Actor      `json:"actor"`
	CausedBy  *CausedBy       `json:"causedBy,omitempty"`
	Payload   json.RawMessage `json:"payload"`
	At        time.Time       `json:"at"`
}

// EntityTopic is the topic of revision changes of one entity: entity.{kind}.{id}.
func EntityTopic(kind, id string) string { return "entity." + kind + "." + id }

// Append inserts drafts into the outbox inside tx. Call it last in the transaction: it takes the outbox lock,
// which is held until commit.
func Append(ctx context.Context, tx pgx.Tx, actor auth.Actor, cause *CausedBy, drafts []Draft) error {
	if len(drafts) == 0 {
		return nil
	}
	if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock($1)", outboxLockKey); err != nil {
		return fmt.Errorf("take outbox lock: %w", err)
	}
	actorJSON, err := json.Marshal(actor)
	if err != nil {
		return fmt.Errorf("marshal actor: %w", err)
	}
	var causeJSON []byte
	if cause != nil {
		if causeJSON, err = json.Marshal(cause); err != nil {
			return fmt.Errorf("marshal cause: %w", err)
		}
	}
	for _, d := range drafts {
		payload := d.Payload
		if payload == nil {
			payload = struct{}{}
		}
		payloadJSON, err := json.Marshal(payload)
		if err != nil {
			return fmt.Errorf("marshal %s payload: %w", d.Type, err)
		}
		var entityJSON []byte
		if d.Entity != nil {
			if entityJSON, err = json.Marshal(d.Entity); err != nil {
				return fmt.Errorf("marshal %s entity: %w", d.Type, err)
			}
		}
		if _, err := tx.Exec(ctx, `INSERT INTO events (topic, type, project_id, entity, actor, caused_by, payload)
			VALUES ($1, $2, NULLIF($3, ''), $4, $5, $6, $7)`,
			d.Topic, d.Type, d.ProjectID, entityJSON, actorJSON, causeJSON, payloadJSON); err != nil {
			return fmt.Errorf("insert event %s: %w", d.Type, err)
		}
	}
	return nil
}

// List returns up to limit events with seq > after that pass f, oldest first.
func List(ctx context.Context, q storage.Querier, f Filter, after int64, limit int) ([]Record, error) {
	where, args := f.sql(after)
	args = append(args, limit)
	rows, err := q.Query(ctx, `SELECT seq, topic, type, coalesce(project_id, ''), entity, actor, caused_by, payload, at
		FROM events WHERE `+where+fmt.Sprintf(" ORDER BY seq LIMIT $%d", len(args)), args...)
	if err != nil {
		return nil, fmt.Errorf("query events: %w", err)
	}
	out, err := pgx.CollectRows(rows, scanRecord)
	if err != nil {
		return nil, fmt.Errorf("read events: %w", err)
	}
	return out, nil
}

func scanRecord(row pgx.CollectableRow) (Record, error) {
	var r Record
	err := row.Scan(&r.Seq, &r.Topic, &r.Type, &r.ProjectID, &r.Entity, &r.Actor, &r.CausedBy, &r.Payload, &r.At)
	return r, err
}
