package search

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/usunrise88/cadence/control-plane/internal/events"
	"github.com/usunrise88/cadence/control-plane/internal/help"
	"github.com/usunrise88/cadence/control-plane/internal/storage"
)

// CursorName is the indexer's row in event_cursors.
const CursorName = "search"

// Indexer keeps search_documents in step with the outbox. It reads committed events after its own cursor (a row
// in event_cursors, advanced in the same transaction as the documents it wrote), reloads each entity an event
// names through its Source and upserts or removes its documents. Because the outbox commits in seq order
// (events.Append), a cursor never skips an event; because documents are reloaded, not patched from payloads,
// replaying an event is harmless. A restart resumes where the last transaction committed.
type Indexer struct {
	pool    *pgxpool.Pool
	hub     *events.Hub
	log     *slog.Logger
	sources map[string]Source
	// PollInterval is how often the table is read without a wake-up from the hub (a dropped subscription).
	PollInterval time.Duration
	// Batch is the most events applied per transaction.
	Batch int
}

// NewIndexer returns an indexer over sources. hub may be nil: the indexer then only polls.
func NewIndexer(pool *pgxpool.Pool, hub *events.Hub, log *slog.Logger, sources []Source) *Indexer {
	m := make(map[string]Source, len(sources))
	for _, s := range sources {
		m[s.Kind] = s
	}
	return &Indexer{pool: pool, hub: hub, log: log, sources: m, PollInterval: time.Second, Batch: 500}
}

// Run indexes until ctx is cancelled: a catch-up after every event the hub publishes, and every PollInterval.
func (ix *Indexer) Run(ctx context.Context) error {
	var sub *events.Subscription
	subscribe := func() <-chan events.Record {
		if ix.hub == nil {
			return nil
		}
		sub = ix.hub.Subscribe()
		return sub.C
	}
	wake := subscribe()
	defer func() {
		if sub != nil {
			sub.Close()
		}
	}()
	tick := time.NewTicker(ix.PollInterval)
	defer tick.Stop()
	for {
		if _, err := ix.CatchUp(ctx); err != nil && ctx.Err() == nil {
			ix.log.WarnContext(ctx, "search index: catch-up failed; retrying", "err", err)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-tick.C:
		case _, ok := <-wake:
			if !ok { // dropped as a slow subscriber, or the hub closed: poll until a new subscription works
				wake = nil
				if ctx.Err() == nil {
					wake = subscribe()
				}
			}
		}
	}
}

// CatchUp applies every committed event after the cursor and returns how many it consumed.
func (ix *Indexer) CatchUp(ctx context.Context) (int, error) {
	total := 0
	for {
		n, err := ix.step(ctx)
		total += n
		if err != nil || n < ix.Batch {
			return total, err
		}
	}
}

// step applies one batch in one transaction: read the cursor (locked, so two control planes never interleave),
// load and write the documents, advance the cursor.
func (ix *Indexer) step(ctx context.Context) (int, error) {
	n := 0
	err := pgx.BeginFunc(ctx, ix.pool, func(tx pgx.Tx) error {
		var cursor int64
		err := tx.QueryRow(ctx, `SELECT seq FROM event_cursors WHERE name = $1 FOR UPDATE`, CursorName).Scan(&cursor)
		if errors.Is(err, pgx.ErrNoRows) {
			if _, err := tx.Exec(ctx, `INSERT INTO event_cursors (name) VALUES ($1) ON CONFLICT DO NOTHING`, CursorName); err != nil {
				return fmt.Errorf("create search cursor: %w", err)
			}
		} else if err != nil {
			return fmt.Errorf("read search cursor: %w", err)
		}
		batch, err := events.List(ctx, tx, events.Filter{}, cursor, ix.Batch)
		if err != nil {
			return err
		}
		n = len(batch)
		if n == 0 {
			return nil
		}
		if err := ix.apply(ctx, tx, batch); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `UPDATE event_cursors SET seq = $2, updated_at = now() WHERE name = $1`,
			CursorName, batch[n-1].Seq)
		if err != nil {
			return fmt.Errorf("advance search cursor: %w", err)
		}
		return nil
	})
	return n, err
}

// apply reloads each entity the batch names once (at its last event) and writes its documents.
func (ix *Indexer) apply(ctx context.Context, tx pgx.Tx, batch []events.Record) error {
	type key struct{ kind, id string }
	last := map[key]events.Record{}
	var order []key
	for _, r := range batch {
		if r.Entity == nil {
			continue
		}
		if _, ok := ix.sources[r.Entity.Kind]; !ok {
			continue
		}
		k := key{r.Entity.Kind, r.Entity.ID}
		if _, seen := last[k]; !seen {
			order = append(order, k)
		}
		last[k] = r
	}
	for _, k := range order {
		ev := last[k]
		docs, err := ix.sources[k.kind].Load(ctx, tx, ev)
		if err != nil {
			return fmt.Errorf("index %s %s: %w", k.kind, k.id, err)
		}
		if len(docs) == 0 {
			if err := Remove(ctx, tx, k.kind, k.id); err != nil {
				return err
			}
			continue
		}
		for _, d := range docs {
			if d.Actor == nil && d.Kind == k.kind && d.ID == k.id {
				actor := ev.Actor // the last actor that changed it, when the entity keeps none
				d.Actor = &actor
			}
			if err := Upsert(ctx, tx, d, ev.Seq); err != nil {
				return err
			}
		}
	}
	return nil
}

// Upsert writes one document, normalising its text.
func Upsert(ctx context.Context, q storage.Querier, d Document, seq int64) error {
	tags := d.Tags
	if tags == nil {
		tags = []string{}
	}
	numbers := d.Numbers
	if numbers == nil {
		numbers = map[string]float64{}
	}
	actorKind := ""
	if d.Actor != nil {
		actorKind = d.Actor.Kind
	}
	var projectID *string
	if d.ProjectID != "" {
		projectID = &d.ProjectID
	}
	_, err := q.Exec(ctx, `INSERT INTO search_documents (kind, id, scope, project_id, ref, title, body, tags, status, lang,
			actor, actor_kind, numbers, title_norm, body_norm, seq, updated_at, indexed_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, now())
		ON CONFLICT (kind, id) DO UPDATE SET scope = EXCLUDED.scope, project_id = EXCLUDED.project_id,
			ref = EXCLUDED.ref, title = EXCLUDED.title, body = EXCLUDED.body, tags = EXCLUDED.tags,
			status = EXCLUDED.status, lang = EXCLUDED.lang, actor = EXCLUDED.actor, actor_kind = EXCLUDED.actor_kind,
			numbers = EXCLUDED.numbers, title_norm = EXCLUDED.title_norm, body_norm = EXCLUDED.body_norm,
			seq = EXCLUDED.seq, updated_at = EXCLUDED.updated_at, indexed_at = now()`,
		d.Kind, d.ID, d.Scope, projectID, d.Ref, d.Title, d.Text, tags, d.Status, d.Lang, d.Actor, actorKind, numbers,
		Normalize(d.Title), Normalize(joinText(d.Text, joinText(tags...))), seq, d.UpdatedAt)
	if err != nil {
		return fmt.Errorf("index %s %s: %w", d.Kind, d.ID, err)
	}
	return nil
}

// Remove deletes the document of an entity that no longer exists.
func Remove(ctx context.Context, q storage.Querier, kind, id string) error {
	if _, err := q.Exec(ctx, `DELETE FROM search_documents WHERE kind = $1 AND id = $2`, kind, id); err != nil {
		return fmt.Errorf("remove %s %s from the index: %w", kind, id, err)
	}
	return nil
}

// IndexHelp replaces the help documents with the bundled articles; it runs at start (the bundle only changes with
// the binary).
func IndexHelp(ctx context.Context, pool *pgxpool.Pool, articles []help.Article, now time.Time) error {
	return pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		ids := make([]string, 0, len(articles))
		for _, a := range articles {
			ids = append(ids, a.ID)
			d := Document{
				Kind: KindHelp, ID: a.ID, Scope: ScopeDocHelp, Ref: ref(KindHelp, a.ID), Title: a.Title,
				Text: joinText(a.Summary, a.Body), Tags: append([]string{"section:" + a.Section}, a.Contexts...),
				UpdatedAt: now,
			}
			if err := Upsert(ctx, tx, d, 0); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx, `DELETE FROM search_documents WHERE kind = $1 AND NOT (id = ANY($2))`, KindHelp, ids); err != nil {
			return fmt.Errorf("remove stale help documents: %w", err)
		}
		return nil
	})
}
