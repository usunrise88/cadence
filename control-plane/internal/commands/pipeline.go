// Package commands runs every mutation the same way: one transaction with the actor, the Idempotency-Key, the
// dry-run switch and the outbox events it emits.
package commands

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/usunrise88/cadence/control-plane/internal/auth"
	"github.com/usunrise88/cadence/control-plane/internal/events"
	"github.com/usunrise88/cadence/control-plane/internal/problems"
)

// Response headers set by the pipeline.
const (
	HeaderCommandID = "Cadence-Command-Id"
	HeaderReplayed  = "Idempotent-Replayed"
)

// idempotencyLockClass is the first key of the two-key pg_advisory_xact_lock that serialises commands sharing an
// actor and Idempotency-Key, so a concurrent repeat waits for the first and then replays it. Two-key advisory
// locks do not collide with the single-key outbox and migration locks.
const idempotencyLockClass int32 = 0x63646e01

// Command describes one invocation of a mutating operation.
type Command struct {
	Operation      string // <entity>.<verb>, e.g. projects.new
	Actor          auth.Actor
	IdempotencyKey string
	DryRun         bool
	RequestHash    string // see HashRequest
}

// Result is what a command answers on success.
type Result struct {
	Status int // HTTP status of a real run; a dry run always answers 200 because it creates nothing
	Body   any // marshalled as JSON
	ETag   string
}

// Response is the HTTP response of a command, exactly as stored for idempotent replay.
type Response struct {
	Status int
	Header http.Header
	Body   []byte
}

// Func does the work of a command inside tx and returns the result and the events it emits.
type Func func(ctx context.Context, tx pgx.Tx) (Result, []events.Draft, error)

// Pipeline runs commands against the database.
type Pipeline struct {
	pool     *pgxpool.Pool
	log      *slog.Logger
	commands *prometheus.CounterVec // labels: operation, outcome
}

// NewPipeline returns a pipeline; commands counts runs by operation and outcome.
func NewPipeline(pool *pgxpool.Pool, log *slog.Logger, commands *prometheus.CounterVec) *Pipeline {
	return &Pipeline{pool: pool, log: log, commands: commands}
}

// Run executes fn as cmd in one transaction:
//
//  1. With an Idempotency-Key (and not a dry run), take a lock on (actor, key) and look the key up: the same
//     operation and request hash replays the stored response; anything else is idempotency-key-reused.
//  2. Run fn.
//  3. A dry run rolls back here and answers 200 with the would-be result; nothing is written, no event emitted.
//  4. Append fn's events to the outbox with causedBy (commandId; toolCallId for agent calls), store the response under the key, commit.
//
// Failed commands roll back entirely and store nothing, so a retry with the same key runs again.
func (p *Pipeline) Run(ctx context.Context, cmd Command, fn Func) (Response, error) {
	start := time.Now()
	id := "cmd_" + uuid.Must(uuid.NewV7()).String()
	resp, outcome, err := p.run(ctx, cmd, id, fn)
	p.commands.WithLabelValues(cmd.Operation, outcome).Inc()
	attrs := []any{
		"actor", cmd.Actor.ID, "actor_kind", cmd.Actor.Kind, "operation", cmd.Operation, "commandId", id,
		"dryRun", cmd.DryRun, "outcome", outcome, "duration_ms", time.Since(start).Milliseconds(),
	}
	if tc := ToolCallID(ctx); tc != "" {
		attrs = append(attrs, "toolCallId", tc)
	}
	level := slog.LevelInfo
	if outcome == problems.Internal.Slug {
		level = slog.LevelError
		attrs = append(attrs, "err", err)
	}
	p.log.Log(ctx, level, "command", attrs...)
	return resp, err
}

func (p *Pipeline) run(ctx context.Context, cmd Command, id string, fn Func) (Response, string, error) {
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return Response{}, problems.Internal.Slug, fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }() // no-op after commit

	useKey := cmd.IdempotencyKey != "" && !cmd.DryRun
	if useKey {
		stored, found, err := lookupKey(ctx, tx, cmd)
		if err != nil {
			return Response{}, outcomeOf(err), err
		}
		if found {
			return stored, "replayed", nil
		}
	}

	res, drafts, err := fn(ctx, tx)
	if err != nil {
		return Response{}, outcomeOf(err), err
	}
	resp, err := render(res, id, cmd.DryRun)
	if err != nil {
		return Response{}, problems.Internal.Slug, err
	}
	if cmd.DryRun {
		return resp, "dry_run", nil
	}

	if err := events.Append(ctx, tx, cmd.Actor, &events.CausedBy{CommandID: id, ToolCallID: ToolCallID(ctx)}, drafts); err != nil {
		return Response{}, problems.Internal.Slug, err
	}
	if useKey {
		if err := storeKey(ctx, tx, cmd, resp); err != nil {
			return Response{}, problems.Internal.Slug, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return Response{}, problems.Internal.Slug, fmt.Errorf("commit %s: %w", cmd.Operation, err)
	}
	return resp, "ok", nil
}

func render(res Result, id string, dryRun bool) (Response, error) {
	body, err := json.Marshal(res.Body)
	if err != nil {
		return Response{}, fmt.Errorf("marshal result: %w", err)
	}
	status := res.Status
	if dryRun || status == 0 {
		status = http.StatusOK
	}
	h := http.Header{}
	h.Set("Content-Type", "application/json")
	h.Set(HeaderCommandID, id)
	if res.ETag != "" {
		h.Set("ETag", res.ETag)
	}
	return Response{Status: status, Header: h, Body: append(body, '\n')}, nil
}

func lookupKey(ctx context.Context, tx pgx.Tx, cmd Command) (Response, bool, error) {
	if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock($1, hashtext($2))",
		idempotencyLockClass, cmd.Actor.ID+"\n"+cmd.IdempotencyKey); err != nil {
		return Response{}, false, fmt.Errorf("lock idempotency key: %w", err)
	}
	var (
		op, hash string
		resp     Response
	)
	err := tx.QueryRow(ctx, `SELECT operation, request_hash, status, headers, body FROM idempotency_keys
		WHERE actor_id = $1 AND key = $2`, cmd.Actor.ID, cmd.IdempotencyKey).
		Scan(&op, &hash, &resp.Status, &resp.Header, &resp.Body)
	if errors.Is(err, pgx.ErrNoRows) {
		return Response{}, false, nil
	}
	if err != nil {
		return Response{}, false, fmt.Errorf("look up idempotency key: %w", err)
	}
	if op != cmd.Operation || hash != cmd.RequestHash {
		return Response{}, false, problems.IdempotencyKeyReused.New(
			"this Idempotency-Key was already used for a different request (%s); use a new key for a new request", op)
	}
	resp.Header.Set(HeaderReplayed, "true")
	return resp, true, nil
}

func storeKey(ctx context.Context, tx pgx.Tx, cmd Command, resp Response) error {
	if _, err := tx.Exec(ctx, `INSERT INTO idempotency_keys (actor_id, key, operation, request_hash, status, headers, body)
		VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		cmd.Actor.ID, cmd.IdempotencyKey, cmd.Operation, cmd.RequestHash, resp.Status, resp.Header, resp.Body); err != nil {
		return fmt.Errorf("store idempotency key: %w", err)
	}
	return nil
}

func outcomeOf(err error) string {
	pe, _ := problems.As(err)
	return pe.Type.Slug
}
