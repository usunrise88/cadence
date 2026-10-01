package storage

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// Postgres error codes of a transaction that lost a race to a concurrent one.
const (
	codeUniqueViolation      = "23505"
	codeSerializationFailure = "40001"
	codeDeadlockDetected     = "40P01"
)

// IsRace reports whether err is a unique violation, a serialization failure or a deadlock: the transaction raced
// a concurrent one, and running it again sees that one's rows.
func IsRace(err error) bool {
	var pg *pgconn.PgError
	if !errors.As(err, &pg) {
		return false
	}
	switch pg.Code {
	case codeUniqueViolation, codeSerializationFailure, codeDeadlockDetected:
		return true
	}
	return false
}

// Beginner starts transactions (a pool or a connection).
type Beginner interface {
	Begin(ctx context.Context) (pgx.Tx, error)
}

// BeginRetry runs fn in a transaction like pgx.BeginFunc and runs it again, at most attempts times in all, while it
// fails with IsRace. Use it only for idempotent work whose rerun finds what the winner wrote (first-time upserts of
// shared rows), never to paper over a real conflict.
func BeginRetry(ctx context.Context, db Beginner, attempts int, fn func(pgx.Tx) error) error {
	var err error
	for range max(attempts, 1) {
		if err = pgx.BeginFunc(ctx, db, fn); err == nil || !IsRace(err) || ctx.Err() != nil {
			return err
		}
	}
	return err
}
