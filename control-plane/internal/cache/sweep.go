package cache

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/usunrise88/cadence/control-plane/internal/auth"
	"github.com/usunrise88/cadence/control-plane/internal/defaults"
	"github.com/usunrise88/cadence/control-plane/internal/events"
	"github.com/usunrise88/cadence/control-plane/internal/jobs"
	"github.com/usunrise88/cadence/control-plane/internal/storage"
)

// projects lists every project with cached dataset bytes against its quota, the largest first.
func (s *Service) projects(ctx context.Context, q storage.Querier, d *defaults.Defaults) ([]Project, error) {
	rows, err := q.Query(ctx, `SELECT p.id, p.slug, coalesce(sum(a.size), 0)::bigint FROM projects p
		JOIN artifacts a ON a.project_id = p.id AND a.type = 'dataset' AND a.evicted_at IS NULL
		WHERE EXISTS (SELECT 1 FROM registry_versions v WHERE `+datasetHashSQL+` = a.hash)
		GROUP BY p.id, p.slug ORDER BY 3 DESC, p.slug`)
	if err != nil {
		return nil, fmt.Errorf("project cache bytes: %w", err)
	}
	quota := quotaBytes(d)
	out, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (Project, error) {
		p := Project{QuotaBytes: quota}
		err := row.Scan(&p.ProjectID, &p.Slug, &p.DatasetBytes)
		p.Over = p.DatasetBytes > quota
		return p, err
	})
	if err != nil {
		return nil, fmt.Errorf("project cache bytes: %w", err)
	}
	return out, nil
}

// candidates orders the evictable datasets the way the sweep takes them: datasets of projects over their quota
// first (spec 05 "Cache fairness"), then least recently used.
func (s *Service) candidates(list []Dataset, projects []Project) []Dataset {
	over := map[string]bool{}
	for _, p := range projects {
		if p.Over {
			over[p.ProjectID] = true
		}
	}
	var out []Dataset
	for _, d := range list {
		if d.Evictable {
			out = append(out, d)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		oi, oj := over[out[i].ProjectID], over[out[j].ProjectID]
		if oi != oj {
			return oi
		}
		return out[i].LastUsedAt.Before(*out[j].LastUsedAt)
	})
	return out
}

// Sweep compares the store's use with storage.cache_high_water_pct and, above it, queues one eviction of the
// evictable dataset versions it takes (over-quota projects first, then least recently used) until the estimated use
// is at storage.cache_low_water_pct, as the system actor and without an approval: only shards that also live on a
// mount go, so datasets.materialize brings any of them back. A sweep never queues a second eviction while its
// previous one is queued or running.
func (s *Service) Sweep(ctx context.Context) error {
	if s.CAS == nil || s.Jobs == nil {
		return nil
	}
	d := s.defaults()
	total, free, err := s.disk()
	if err != nil || total <= 0 {
		return err
	}
	used := float64(total-free) / float64(total) * 100
	ctx = auth.WithActor(ctx, jobs.System)
	return pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		var jobID *string
		if used <= d.Storage.CacheHighWaterPct.Value {
			return recordSweep(ctx, tx, used, nil)
		}
		var queued bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM jobs WHERE kind = $1 AND state IN ('queued', 'running')
				AND actor->>'id' = $2)`, JobEvict, jobs.System.ID).Scan(&queued); err != nil {
			return fmt.Errorf("find a queued sweep: %w", err)
		}
		if queued {
			return recordSweep(ctx, tx, used, nil)
		}
		list, err := listDatasets(ctx, tx, "")
		if err != nil {
			return err
		}
		for i := range list {
			if _, err := fill(ctx, tx, &list[i]); err != nil {
				return err
			}
		}
		projects, err := s.projects(ctx, tx, d)
		if err != nil {
			return err
		}
		target := int64((used - d.Storage.CacheLowWaterPct.Value) / 100 * float64(total))
		var (
			ids   []string
			freed int64
		)
		for _, c := range s.candidates(list, projects) {
			if freed >= target {
				break
			}
			p, err := s.PlanEvict(ctx, tx, c.VersionID)
			if err != nil || len(p.Blocked) > 0 {
				continue
			}
			ids = append(ids, c.VersionID)
			freed += p.FreeBytes
		}
		if len(ids) == 0 {
			s.log().WarnContext(ctx, "cache above its high-water mark and nothing is evictable", "usedPct", used,
				"highWaterPct", d.Storage.CacheHighWaterPct.Value)
			return recordSweep(ctx, tx, used, nil)
		}
		j, drafts, err := s.EnqueueEvict(ctx, tx, ids, "sweep")
		if err != nil {
			return err
		}
		jobID = &j.ID
		s.log().InfoContext(ctx, "cache above its high-water mark: evicting", "usedPct", used, "versions", len(ids),
			"bytes", freed, "jobId", j.ID)
		if err := recordSweep(ctx, tx, used, jobID); err != nil {
			return err
		}
		return events.Append(ctx, tx, jobs.System, nil, drafts)
	})
}

func recordSweep(ctx context.Context, tx pgx.Tx, used float64, jobID *string) error {
	if _, err := tx.Exec(ctx, `INSERT INTO cache_sweeps (id, swept_at, used_pct, job_id) VALUES (true, $1, $2, $3)
		ON CONFLICT (id) DO UPDATE SET swept_at = excluded.swept_at, used_pct = excluded.used_pct,
			job_id = coalesce(excluded.job_id, cache_sweeps.job_id)`, time.Now().UTC(), used, jobID); err != nil {
		return fmt.Errorf("record the cache sweep: %w", err)
	}
	return nil
}
