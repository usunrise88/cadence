package eviction

// Spectrogram tile pyramids the control plane built are kept by last view (phase 4 tail; docs/spec/02 "Content
// store"): a pyramid media.spectrogram built on an audio view's first request is derived data — the next request
// builds it again from the same audio (spectrogram.get answers 202 with the build) — so it leaves the store
// media.tiles_retention_days after it was last viewed (artifacts.last_used_at, set when spectrogram.get answers its
// manifest; its creation before the first view), unless something references it. The cache sweep (internal/cache)
// evicts only what a mount copy brings back, which a pyramid never has; an age rule like the eval artifacts' fits a
// view cache instead. The eviction is permanent (the mirror is not needed: the audio rebuilds it), runs as the system
// actor without an approval, and never takes a pyramid a pipeline step wrote (spectrogram_tiles@1 outputs belong to
// their runs).

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/usunrise88/cadence/control-plane/internal/auth"
	"github.com/usunrise88/cadence/control-plane/internal/defaults"
	"github.com/usunrise88/cadence/control-plane/internal/events"
	"github.com/usunrise88/cadence/control-plane/internal/jobs"
)

// Tile pyramids the retention takes: artifacts of TypeTiles whose meta.source is TilesSource (internal/media's
// TypeSpectrogramTiles and JobSpectrogram; the media package is not imported here).
const (
	TypeTiles   = "spectrogram_tiles"
	TilesSource = "media.spectrogram"
)

// tilesArtifact is one live control-plane pyramid with its last view and what still needs it.
type tilesArtifact struct {
	Candidate
	lastView   time.Time // last_used_at, or the creation before the first view
	referenced string
}

// classifyTiles applies the view retention: a pyramid goes when nothing references it and it was last viewed more
// than retention before now.
func classifyTiles(arts []tilesArtifact, retention time.Duration, now time.Time) ([]Candidate, []Kept) {
	var (
		evict []Candidate
		kept  []Kept
	)
	days := int(retention.Hours() / 24)
	for _, a := range arts {
		switch {
		case a.referenced != "":
			kept = append(kept, Kept{Hash: a.Hash, Reason: a.referenced})
		case now.Sub(a.lastView) < retention:
			kept = append(kept, Kept{Hash: a.Hash, Reason: fmt.Sprintf("last viewed on %s, less than %d days ago",
				a.lastView.UTC().Format(time.DateOnly), days)})
		default:
			c := a.Candidate
			c.Reason = fmt.Sprintf("a spectrogram tile pyramid last viewed on %s, more than %d days ago (the next view builds it again)",
				a.lastView.UTC().Format(time.DateOnly), days)
			evict = append(evict, c)
		}
	}
	return evict, kept
}

// tilesArtifacts reads the live pyramids the control plane built, with their last view.
func tilesArtifacts(ctx context.Context, tx pgx.Tx) ([]tilesArtifact, error) {
	rows, err := tx.Query(ctx, `SELECT hash, type, size, directory, coalesce(project_id, ''), created_at,
			coalesce(last_used_at, created_at)
		FROM artifacts WHERE type = $1 AND meta->>'source' = $2 AND evicted_at IS NULL
		ORDER BY created_at, hash`, TypeTiles, TilesSource)
	if err != nil {
		return nil, fmt.Errorf("query tile pyramids: %w", err)
	}
	out, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (tilesArtifact, error) {
		var a tilesArtifact
		err := row.Scan(&a.Hash, &a.Type, &a.Size, &a.directory, &a.ProjectID, &a.CreatedAt, &a.lastView)
		return a, err
	})
	if err != nil {
		return nil, fmt.Errorf("read tile pyramids: %w", err)
	}
	return out, nil
}

// PlanTilesRetention answers what the view retention of tile pyramids evicts now, inside tx.
func (s *Service) PlanTilesRetention(ctx context.Context, tx pgx.Tx, days int, now time.Time) (Plan, error) {
	if s.CAS == nil {
		return Plan{Artifacts: []Candidate{}, Kept: []Kept{}, Permanent: true}, nil
	}
	if days < 1 {
		return Plan{}, fmt.Errorf("tile pyramid retention of %d days: at least 1", days)
	}
	if now.IsZero() {
		now = time.Now()
	}
	arts, err := tilesArtifacts(ctx, tx)
	if err != nil {
		return Plan{}, err
	}
	for i := range arts {
		if arts[i].referenced, err = referenced(ctx, tx, arts[i].Hash); err != nil {
			return Plan{}, err
		}
	}
	evict, kept := classifyTiles(arts, time.Duration(days)*24*time.Hour, now)
	p := Plan{Artifacts: evict, Kept: kept, Permanent: true}
	if err := s.sizeBlobs(ctx, tx, &p); err != nil {
		return Plan{}, err
	}
	if p.Artifacts == nil {
		p.Artifacts = []Candidate{}
	}
	if p.Kept == nil {
		p.Kept = []Kept{}
	}
	return p, nil
}

// SweepTiles is the daily view retention of tile pyramids (a periodic job): when the plan at
// media.tiles_retention_days finds anything, it queues the eviction job, which re-plans under the store lock, marks,
// deletes and audits (detail.tilesRetentionDays). No approval: the pyramids are a cache the next view rebuilds.
func (s *Service) SweepTiles(ctx context.Context, d *defaults.Defaults) error {
	if s.CAS == nil || s.Jobs == nil {
		return nil
	}
	days := d.Media.TilesRetentionDays.Value
	ctx = auth.WithActor(ctx, jobs.System)
	return pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		p, err := s.PlanTilesRetention(ctx, tx, days, time.Now())
		if err != nil || len(p.Artifacts) == 0 {
			return err
		}
		var queued bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM jobs WHERE kind = $1 AND state IN ('queued', 'running')
				AND actor->>'id' = $2)`, JobKind, jobs.System.ID).Scan(&queued); err != nil {
			return fmt.Errorf("find a queued retention: %w", err)
		}
		if queued {
			return nil // a retention job of the system is queued: the next sweep plans again
		}
		_, drafts, err := s.Jobs.Enqueue(ctx, tx, jobs.Spec{Kind: JobKind, Args: jobArgs{TilesRetentionDays: days}})
		if err != nil {
			return err
		}
		s.log().InfoContext(ctx, "tile pyramids past their retention", "artifacts", len(p.Artifacts),
			"bytes", p.BytesFreed, "days", days)
		return events.Append(ctx, tx, jobs.System, nil, drafts)
	})
}
