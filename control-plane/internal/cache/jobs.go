package cache

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/jackc/pgx/v5"
	"golang.org/x/sys/unix"

	"github.com/usunrise88/cadence/control-plane/internal/artifacts"
	"github.com/usunrise88/cadence/control-plane/internal/audit"
	"github.com/usunrise88/cadence/control-plane/internal/events"
	"github.com/usunrise88/cadence/control-plane/internal/jobs"
	"github.com/usunrise88/cadence/control-plane/internal/mounts"
	"github.com/usunrise88/cadence/control-plane/internal/steps"
	"github.com/usunrise88/cadence/control-plane/internal/storage"
)

// Job kinds and events.
const (
	JobEvict       = "datasets.evict"
	JobMaterialize = "datasets.materialize"
	periodicSweep  = "storage.cacheSweep"
	// EventEvicted is the artifact eviction event (internal/eviction's), on entity.artifact.{hash}.
	EventEvicted = "artifact.evicted"
	// EventRestored goes out on entity.artifact.{hash} when a materialisation brought every blob back.
	EventRestored = "artifact.restored"
)

// Register adds the evict and materialise job kinds and the periodic cache sweep; call it before the job runner
// starts.
func (s *Service) Register(js *jobs.Service) {
	s.Jobs = js
	js.Register(JobEvict, s.runEvict, jobs.KindOptions{MaxAttempts: 3, Timeout: time.Hour})
	js.Register(JobMaterialize, s.runMaterialize, jobs.KindOptions{MaxAttempts: 3, Timeout: 24 * time.Hour})
	js.AddPeriodic(periodicSweep, time.Duration(s.defaults().Storage.CacheSweepMinutes.Value)*time.Minute, s.Sweep)
}

type evictArgs struct {
	VersionIDs []string `json:"versionIds"`
	Reason     string   `json:"reason,omitempty"` // "sweep" for the high-water sweep
}

type materializeArgs struct {
	VersionID string `json:"versionId"`
}

// EnqueueEvict queues the eviction of versions (in the command's transaction).
func (s *Service) EnqueueEvict(ctx context.Context, tx pgx.Tx, versionIDs []string, reason string) (jobs.Job, []events.Draft, error) {
	if s.Jobs == nil {
		return jobs.Job{}, nil, errors.New("cache: the job kinds are not registered")
	}
	return s.Jobs.Enqueue(ctx, tx, jobs.Spec{Kind: JobEvict, Args: evictArgs{VersionIDs: versionIDs, Reason: reason}})
}

// EnqueueMaterialize queues the materialisation of a version.
func (s *Service) EnqueueMaterialize(ctx context.Context, tx pgx.Tx, versionID string) (jobs.Job, []events.Draft, error) {
	if s.Jobs == nil {
		return jobs.Job{}, nil, errors.New("cache: the job kinds are not registered")
	}
	return s.Jobs.Enqueue(ctx, tx, jobs.Spec{Kind: JobMaterialize, Args: materializeArgs{VersionID: versionID}})
}

// evictResult is what an eviction job reports.
type evictResult struct {
	Evicted    []string          `json:"evicted"`
	Skipped    map[string]string `json:"skipped"`
	BytesFreed int64             `json:"bytesFreed"`
	Blobs      int               `json:"blobs"`
}

// runEvict evicts each version that is still evictable, in two halves like artifacts.evict (internal/eviction):
// under the store's exclusive lock it re-plans and marks the artifact evicted (artifact.evicted), then, again under
// the lock, deletes the blobs the marked artifacts alone list that have a copy on a mount. The manifest stays, so a
// materialisation knows what to copy back. A retry finds its marked rows and deletes what is left.
func (s *Service) runEvict(ctx context.Context, r *jobs.Run) (any, error) {
	var args evictArgs
	if err := json.Unmarshal(r.Args, &args); err != nil {
		return nil, fmt.Errorf("read eviction args: %w", err)
	}
	if s.CAS == nil {
		return nil, errors.New("cache: no content store")
	}
	res := evictResult{Evicted: []string{}, Skipped: map[string]string{}}
	err := pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		if err := artifacts.LockExclusive(ctx, tx); err != nil {
			return err
		}
		var drafts []events.Draft
		now := time.Now().UTC()
		for _, id := range args.VersionIDs {
			p, err := s.PlanEvict(ctx, tx, id)
			if err != nil {
				res.Skipped[id] = err.Error()
				continue
			}
			if len(p.Blocked) > 0 {
				if p.State != StateEvicted {
					res.Skipped[id] = p.Blocked[0]
				}
				continue
			}
			tag, err := tx.Exec(ctx, `UPDATE artifacts SET evicted_at = $2, evicted_by = $3, eviction_job_id = $4
				WHERE hash = $1 AND evicted_at IS NULL`, p.Artifact, now, r.Job.Actor, r.Job.ID)
			if err != nil {
				return fmt.Errorf("mark %s evicted: %w", p.Artifact, err)
			}
			if tag.RowsAffected() == 0 {
				continue
			}
			res.Evicted = append(res.Evicted, id)
			drafts = append(drafts, events.Draft{Topic: events.EntityTopic(artifacts.Kind, p.Artifact), Type: EventEvicted,
				Entity: &events.EntityRef{Kind: artifacts.Kind, ID: p.Artifact},
				Payload: map[string]any{"hash": p.Artifact, "type": "dataset", "size": p.Bytes, "datasetVersionId": id,
					"jobId": r.Job.ID, "at": now, "by": r.Job.Actor, "permanent": false, "reason": args.Reason}})
		}
		return events.Append(ctx, tx, r.Job.Actor, nil, drafts)
	})
	if err != nil {
		return nil, err
	}
	err = pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		if err := artifacts.LockExclusive(ctx, tx); err != nil {
			return err
		}
		// Every file of an artifact this job marked, that has a copy on a mount and that no live artifact lists.
		rows, err := tx.Query(ctx, `SELECT DISTINCT f.file_hash FROM artifact_files f JOIN artifacts a ON a.hash = f.hash
			WHERE a.eviction_job_id = $1 AND a.evicted_at IS NOT NULL
			AND EXISTS (SELECT 1 FROM blob_copies b WHERE b.hash = f.file_hash)
			AND NOT EXISTS (SELECT 1 FROM artifact_files g JOIN artifacts o ON o.hash = g.hash
				WHERE g.file_hash = f.file_hash AND o.evicted_at IS NULL)
			AND NOT EXISTS (SELECT 1 FROM artifacts o WHERE o.hash = f.file_hash AND o.evicted_at IS NULL)`, r.Job.ID)
		if err != nil {
			return fmt.Errorf("find blobs to delete: %w", err)
		}
		blobs, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			return fmt.Errorf("find blobs to delete: %w", err)
		}
		for i, b := range blobs {
			n, err := s.CAS.Delete(b)
			if err != nil {
				return err
			}
			if n > 0 {
				res.BytesFreed += n
				res.Blobs++
			}
			if i%100 == 99 {
				_ = r.Progress(ctx, float64(i+1)/float64(len(blobs)), fmt.Sprintf("%d of %d shards deleted", i+1, len(blobs)))
			}
		}
		return audit.Write(ctx, tx, audit.Entry{Operation: JobEvict, Actor: r.Job.Actor, Outcome: audit.OutcomeOK, Status: 200,
			Detail: map[string]any{"jobId": r.Job.ID, "datasetVersions": res.Evicted, "skipped": res.Skipped,
				"bytesFreed": res.BytesFreed, "blobs": res.Blobs, "reason": args.Reason}})
	})
	if err != nil {
		return nil, err
	}
	s.log().InfoContext(ctx, "evicted dataset shards", "jobId", r.Job.ID, "versions", len(res.Evicted),
		"blobs", res.Blobs, "bytesFreed", res.BytesFreed, "reason", args.Reason)
	return res, nil
}

// materializeResult is what a materialisation reports.
type materializeResult struct {
	Copied   int   `json:"copied"`
	Bytes    int64 `json:"bytes"`
	Restored bool  `json:"restored"`
	Missing  int   `json:"missing"`
}

// runMaterialize copies the version's missing shards back from their mount copies, shard by shard (each verified by
// hash on the way in; a retry skips what is back already), then clears the artifact's eviction once every blob is
// in the store (artifact.restored) and records the use.
func (s *Service) runMaterialize(ctx context.Context, r *jobs.Run) (any, error) {
	var args materializeArgs
	if err := json.Unmarshal(r.Args, &args); err != nil {
		return nil, fmt.Errorf("read materialisation args: %w", err)
	}
	p, err := s.PlanMaterialize(ctx, s.Pool, args.VersionID)
	if err != nil {
		return nil, err
	}
	res := materializeResult{Missing: p.Missing}
	readers := map[string]mounts.Reader{}
	for i, c := range p.copies {
		if err := s.copyBack(ctx, c, readers); err != nil {
			return nil, err
		}
		res.Copied++
		res.Bytes += c.size
		_ = r.Progress(ctx, float64(i+1)/float64(len(p.copies)), fmt.Sprintf("%d of %d shards copied", i+1, len(p.copies)))
	}
	err = pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		if err := artifacts.LockShared(ctx, tx); err != nil {
			return err
		}
		a, err := artifacts.Get(ctx, tx, p.Artifact)
		if err != nil {
			return err
		}
		if _, err := artifacts.Verify(s.CAS, steps.ArtifactRef{Hash: a.Hash, Type: a.Type, Size: a.Size}); err != nil {
			return nil // shards are still missing: the artifact stays evicted
		}
		if _, err := tx.Exec(ctx, `UPDATE artifacts SET evicted_at = NULL, evicted_by = NULL, eviction_job_id = NULL,
			last_used_at = now() WHERE hash = $1`, a.Hash); err != nil {
			return fmt.Errorf("restore %s: %w", a.Hash, err)
		}
		res.Restored = true
		if a.Evicted == nil {
			return nil
		}
		return events.Append(ctx, tx, r.Job.Actor, nil, []events.Draft{{Topic: events.EntityTopic(artifacts.Kind, a.Hash),
			Type: EventRestored, Entity: &events.EntityRef{Kind: artifacts.Kind, ID: a.Hash},
			Payload: map[string]any{"hash": a.Hash, "type": a.Type, "datasetVersionId": args.VersionID, "jobId": r.Job.ID}}})
	})
	if err != nil {
		return nil, err
	}
	if !res.Restored {
		return res, fmt.Errorf("dataset %s: %d shards are in no cache and on no mount; the version stays evicted", args.VersionID, p.Missing)
	}
	return res, nil
}

// copyBack copies one blob from the first mount that holds it, verifying the hash.
func (s *Service) copyBack(ctx context.Context, c copyJob, readers map[string]mounts.Reader) error {
	rows, err := s.Pool.Query(ctx, `SELECT b.mount_id, b.path FROM blob_copies b JOIN mounts m ON m.id = b.mount_id
		WHERE b.hash = $1 ORDER BY (m.health->>'state' = 'unhealthy'), m.name`, c.hash)
	if err != nil {
		return fmt.Errorf("find copies of %s: %w", c.hash, err)
	}
	type loc struct{ mount, path string }
	locs, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (loc, error) {
		var l loc
		err := row.Scan(&l.mount, &l.path)
		return l, err
	})
	if err != nil {
		return fmt.Errorf("find copies of %s: %w", c.hash, err)
	}
	var errs []error
	for _, l := range locs {
		rd := readers[l.mount]
		if rd == nil {
			m, err := mounts.Get(ctx, s.Pool, l.mount)
			if err == nil {
				rd, err = mounts.Open(ctx, m, s.Secrets, s.HTTP)
			}
			if err != nil {
				errs = append(errs, err)
				continue
			}
			readers[l.mount] = rd
		}
		f, err := rd.Open(ctx, l.path)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		_, _, err = s.CAS.Put(f, c.hash)
		_ = f.Close()
		if err == nil {
			return nil
		}
		errs = append(errs, fmt.Errorf("%s: %w", l.path, err))
	}
	if len(errs) == 0 {
		return fmt.Errorf("blob %s: no copy on any mount", c.hash)
	}
	return fmt.Errorf("blob %s: %w", c.hash, errors.Join(errs...))
}

// ---------------------------------------------------------------- use and the sweep

// Use is the cache now (the contract's StorageUse).
type Use struct {
	TotalBytes     int64      `json:"totalBytes"`
	FreeBytes      int64      `json:"freeBytes"`
	UsedPct        float64    `json:"usedPct"`
	HighWaterPct   float64    `json:"highWaterPct"`
	LowWaterPct    float64    `json:"lowWaterPct"`
	ArtifactBytes  int64      `json:"artifactBytes"`
	DatasetBytes   int64      `json:"datasetBytes"`
	EvictableBytes int64      `json:"evictableBytes"`
	LastSweepAt    *time.Time `json:"lastSweepAt,omitempty"`
	Projects       []Project  `json:"projects"`
	Datasets       []Dataset  `json:"datasets"`
}

// Project is one project's cached datasets against its quota (the contract's StorageProject).
type Project struct {
	ProjectID    string `json:"projectId"`
	Slug         string `json:"slug"`
	DatasetBytes int64  `json:"datasetBytes"`
	QuotaBytes   int64  `json:"quotaBytes"`
	Over         bool   `json:"over"`
}

func (s *Service) disk() (int64, int64, error) {
	if s.CAS == nil {
		return 0, 0, errors.New("cache: no content store")
	}
	if s.Disk != nil {
		return s.Disk(s.CAS.Root())
	}
	var st unix.Statfs_t
	if err := unix.Statfs(s.CAS.Root(), &st); err != nil {
		return 0, 0, fmt.Errorf("statfs %s: %w", s.CAS.Root(), err)
	}
	bsize := uint64(st.Bsize) //nolint:unconvert,gosec // platform-dependent field type; a block size is positive
	clamp := func(blocks uint64) int64 {
		if bsize != 0 && blocks > math.MaxInt64/bsize {
			return math.MaxInt64
		}
		return int64(blocks * bsize) //nolint:gosec // bounded above
	}
	return clamp(st.Blocks), clamp(st.Bavail), nil
}

// Use reads the cache: the store's filesystem, the marks, projects against their quota and every dataset version
// with an artifact (least recently used first, with pins and evictability).
func (s *Service) Use(ctx context.Context, q storage.Querier) (Use, error) {
	d := s.defaults()
	u := Use{HighWaterPct: d.Storage.CacheHighWaterPct.Value, LowWaterPct: d.Storage.CacheLowWaterPct.Value,
		Projects: []Project{}, Datasets: []Dataset{}}
	total, free, err := s.disk()
	if err != nil {
		return Use{}, err
	}
	u.TotalBytes, u.FreeBytes = total, free
	if total > 0 {
		u.UsedPct = math.Round(float64(total-free)/float64(total)*1000) / 10
	}
	if err := q.QueryRow(ctx, `SELECT coalesce(sum(size), 0)::bigint FROM artifacts WHERE evicted_at IS NULL`).Scan(&u.ArtifactBytes); err != nil {
		return Use{}, fmt.Errorf("artifact bytes: %w", err)
	}
	var swept time.Time
	if err := q.QueryRow(ctx, `SELECT swept_at FROM cache_sweeps`).Scan(&swept); err == nil {
		u.LastSweepAt = &swept
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return Use{}, fmt.Errorf("last sweep: %w", err)
	}
	list, err := listDatasets(ctx, q, "")
	if err != nil {
		return Use{}, err
	}
	for i := range list {
		if _, err := fill(ctx, q, &list[i]); err != nil {
			return Use{}, err
		}
		if list[i].State == StateCached {
			u.DatasetBytes += list[i].Bytes
		}
	}
	u.Datasets = list
	if u.Projects, err = s.projects(ctx, q, d); err != nil {
		return Use{}, err
	}
	for _, c := range s.candidates(list, u.Projects) {
		u.EvictableBytes += c.Bytes
	}
	return u, nil
}
