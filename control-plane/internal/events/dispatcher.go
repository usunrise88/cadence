package events

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
)

// NotifyChannel is the Postgres channel the events insert trigger notifies (migration 0001).
const NotifyChannel = "cadence_events"

const dispatchBatch = 500

// Dispatcher publishes committed outbox rows to the hub in seq order. It wakes on NOTIFY cadence_events and polls
// every PollInterval as a fallback (a lost notification, a dropped LISTEN connection).
type Dispatcher struct {
	pool         *pgxpool.Pool
	hub          *Hub
	log          *slog.Logger
	dispatched   prometheus.Counter
	PollInterval time.Duration
}

// NewDispatcher returns a dispatcher polling every second.
func NewDispatcher(pool *pgxpool.Pool, hub *Hub, log *slog.Logger, dispatched prometheus.Counter) *Dispatcher {
	return &Dispatcher{pool: pool, hub: hub, log: log, dispatched: dispatched, PollInterval: time.Second}
}

// Run dispatches until ctx is cancelled. It starts after the newest committed event: history is served to
// clients from the table, the hub carries only what commits from now on.
func (d *Dispatcher) Run(ctx context.Context) error {
	var cursor int64
	if err := d.pool.QueryRow(ctx, "SELECT coalesce(max(seq), 0) FROM events").Scan(&cursor); err != nil {
		return fmt.Errorf("read outbox head: %w", err)
	}
	for {
		err := d.listen(ctx, &cursor)
		if ctx.Err() != nil {
			return nil // cancellation is the normal way to stop
		}
		d.log.WarnContext(ctx, "event dispatcher reconnecting", "err", err, "cursor", cursor)
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(d.PollInterval):
		}
	}
}

func (d *Dispatcher) listen(ctx context.Context, cursor *int64) error {
	conn, err := d.pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("acquire listen connection: %w", err)
	}
	defer conn.Release()
	defer func() {
		// Leave no listener behind on a pooled connection; a broken one is discarded by Release anyway.
		uctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
		defer cancel()
		_, _ = conn.Exec(uctx, "UNLISTEN *")
	}()
	if _, err := conn.Exec(ctx, "LISTEN "+NotifyChannel); err != nil {
		return fmt.Errorf("listen: %w", err)
	}
	for {
		if err := d.drain(ctx, conn, cursor); err != nil {
			return err
		}
		wait, cancel := context.WithTimeout(ctx, d.PollInterval)
		_, err := conn.Conn().WaitForNotification(wait)
		cancel()
		if err != nil && !errors.Is(err, context.DeadlineExceeded) {
			return fmt.Errorf("wait for notification: %w", err)
		}
	}
}

func (d *Dispatcher) drain(ctx context.Context, conn *pgxpool.Conn, cursor *int64) error {
	for {
		batch, err := List(ctx, conn, Filter{}, *cursor, dispatchBatch)
		if err != nil {
			return err
		}
		for _, r := range batch {
			d.hub.Publish(r)
			*cursor = r.Seq
		}
		d.dispatched.Add(float64(len(batch)))
		if len(batch) < dispatchBatch {
			return nil
		}
	}
}
