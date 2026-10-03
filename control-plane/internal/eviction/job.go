package eviction

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/usunrise88/cadence/control-plane/internal/artifacts"
	"github.com/usunrise88/cadence/control-plane/internal/audit"
	"github.com/usunrise88/cadence/control-plane/internal/cas"
	"github.com/usunrise88/cadence/control-plane/internal/events"
	"github.com/usunrise88/cadence/control-plane/internal/jobs"
	"github.com/usunrise88/cadence/control-plane/internal/steps"
)

// JobKind is the job that deletes the blobs of an approved eviction.
const JobKind = "artifacts.evict"

// Operation is the command (and its audit rows).
const Operation = "artifacts.evict"

// EventEvicted goes out on entity.artifact.{hash} for every artifact the job evicts.
const EventEvicted = "artifact.evicted"

// jobArgs are what the approved replay planned; the job re-plans them, so a state that became needed meanwhile (a
// resume started) stays.
type jobArgs struct {
	Hashes     []string `json:"hashes"`
	ApprovalID string   `json:"approvalId,omitempty"`
	// RetentionDays, instead of hashes, re-plans the age retention of eval artifacts (SweepEvalArtifacts).
	RetentionDays int `json:"retentionDays,omitempty"`
}

// Register adds the eviction job kind; call it before the job runner starts.
func (s *Service) Register(js *jobs.Service) {
	s.Jobs = js
	js.Register(JobKind, s.run, jobs.KindOptions{MaxAttempts: 3, Timeout: time.Hour})
}

// Enqueue queues the job that evicts p's artifacts, in the command's transaction (the approved replay).
func (s *Service) Enqueue(ctx context.Context, tx pgx.Tx, p Plan, approvalID string) (jobs.Job, []events.Draft, error) {
	if s.Jobs == nil {
		return jobs.Job{}, nil, errors.New("eviction: the job kind is not registered")
	}
	return s.Jobs.Enqueue(ctx, tx, jobs.Spec{Kind: JobKind, Args: jobArgs{Hashes: p.Hashes(), ApprovalID: approvalID}})
}

// run marks the planned artifacts evicted (one transaction, with their events), deletes their blobs, and writes the
// audit entry with the bytes freed. A retry finds the rows this job already marked and deletes what is left, so a
// crash between the two halves never leaks blobs; a second eviction of the same artifacts finds nothing to do.
//
// Both halves hold the store lock exclusive (artifacts.LockExclusive), which every artifacts.Record and step reuse
// holds shared from its store check to its commit. The first half plans and marks with every such transaction
// either committed (its references visible to the plan) or not started. The second re-reads which rows are still
// marked by this job — a Record in between found the bytes and cleared the mark — and which blobs no live artifact
// lists, and deletes them before it lets a Record in again; a Record after it finds the bytes gone (ErrEvicted).
func (s *Service) run(ctx context.Context, r *jobs.Run) (any, error) {
	var args jobArgs
	if len(r.Args) > 0 {
		if err := json.Unmarshal(r.Args, &args); err != nil {
			return nil, fmt.Errorf("read eviction args: %w", err)
		}
	}
	reasons := map[string]string{}
	err := pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		if err := artifacts.LockExclusive(ctx, tx); err != nil {
			return err
		}
		mine, err := markedBy(ctx, tx, r.Job.ID)
		if err != nil || len(mine) > 0 || (len(args.Hashes) == 0 && args.RetentionDays == 0) {
			return err // a retry: the rows are marked already
		}
		var p Plan
		if args.RetentionDays > 0 {
			p, err = s.PlanEvalRetention(ctx, tx, args.RetentionDays, time.Now())
		} else {
			p, err = s.Plan(ctx, tx, Filter{Hashes: args.Hashes})
		}
		if err != nil {
			return err
		}
		for _, c := range p.Artifacts {
			reasons[c.Hash] = c.Reason
		}
		return s.mark(ctx, tx, r.Job, p)
	})
	if err != nil {
		return nil, err
	}
	s.hook("marked")
	// Training states are never mirrored; eval artifacts are evicted only once the mirror holds them, when there is one.
	p := Plan{Kept: []Kept{}, Permanent: args.RetentionDays == 0 || s.MirrorDir == ""}
	err = pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		if err := artifacts.LockExclusive(ctx, tx); err != nil {
			return err
		}
		mine, err := markedBy(ctx, tx, r.Job.ID)
		if err != nil {
			return err
		}
		for i, c := range mine {
			if why, ok := reasons[c.Hash]; ok {
				mine[i].Reason = why
			}
		}
		p.Artifacts = mine
		// Under the lock: no live artifact outside the set needs a blob that only the set lists, and none can
		// start to until the deletion is done.
		blobs, err := s.deletable(ctx, tx, mine, p.Hashes())
		if err != nil {
			return err
		}
		s.hook("deleting")
		for i, b := range blobs {
			n, err := s.CAS.Delete(b)
			if err != nil {
				return err // retried: the rows stay marked and the next attempt deletes the rest
			}
			if n > 0 {
				p.BytesFreed += n
				p.Blobs++
			}
			if i%50 == 49 {
				_ = r.Progress(ctx, float64(i+1)/float64(len(blobs)), fmt.Sprintf("%d of %d blobs deleted", i+1, len(blobs)))
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if p.Artifacts == nil {
		p.Artifacts = []Candidate{}
	}
	err = pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		return audit.Write(ctx, tx, audit.Entry{
			Operation: Operation, Actor: r.Job.Actor, Outcome: audit.OutcomeOK, Status: 200, ApprovalID: args.ApprovalID,
			Detail: map[string]any{"jobId": r.Job.ID, "artifacts": p.Hashes(), "bytesFreed": p.BytesFreed,
				"blobs": p.Blobs, "permanent": p.Permanent, "retentionDays": args.RetentionDays},
		})
	})
	if err != nil {
		return nil, err
	}
	s.log().InfoContext(ctx, "evicted artifacts", "jobId", r.Job.ID, "artifacts", len(p.Artifacts),
		"blobs", p.Blobs, "bytesFreed", p.BytesFreed)
	return p, nil
}

// markedBy reads the artifacts job jobID already marked evicted (a retry).
func markedBy(ctx context.Context, tx pgx.Tx, jobID string) ([]Candidate, error) {
	rows, err := tx.Query(ctx, `SELECT a.hash, a.type, a.size, a.directory, coalesce(a.project_id, ''),
			coalesce(a.pipeline_run_id, ''), coalesce(r.id, ''), a.created_at
		FROM artifacts a LEFT JOIN runs r ON r.pipeline_run_id = a.pipeline_run_id
		WHERE a.eviction_job_id = $1 ORDER BY a.created_at, a.hash`, jobID)
	if err != nil {
		return nil, fmt.Errorf("query evicted artifacts: %w", err)
	}
	out, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (Candidate, error) {
		c := Candidate{Reason: "evicted by this job"}
		err := row.Scan(&c.Hash, &c.Type, &c.Size, &c.directory, &c.ProjectID, &c.PipelineRunID, &c.RunID, &c.CreatedAt)
		return c, err
	})
	if err != nil {
		return nil, fmt.Errorf("read evicted artifacts: %w", err)
	}
	return out, nil
}

// mark records the plan's artifacts as evicted by job j and emits artifact.evicted for each.
func (s *Service) mark(ctx context.Context, tx pgx.Tx, j jobs.Job, p Plan) error {
	if len(p.Artifacts) == 0 {
		return nil
	}
	now := time.Now()
	if _, err := tx.Exec(ctx, `UPDATE artifacts SET evicted_at = $2, evicted_by = $3, eviction_job_id = $4
		WHERE hash = ANY($1) AND evicted_at IS NULL`, p.Hashes(), now, j.Actor, j.ID); err != nil {
		return fmt.Errorf("mark artifacts evicted: %w", err)
	}
	drafts := make([]events.Draft, 0, len(p.Artifacts))
	for _, c := range p.Artifacts {
		drafts = append(drafts, events.Draft{
			Topic: events.EntityTopic(artifacts.Kind, c.Hash), Type: EventEvicted, ProjectID: c.ProjectID,
			Entity: &events.EntityRef{Kind: artifacts.Kind, ID: c.Hash},
			Payload: map[string]any{"hash": c.Hash, "type": c.Type, "size": c.Size, "runId": c.RunID, "jobId": j.ID,
				"at": now, "by": j.Actor, "permanent": p.Permanent},
		})
	}
	return events.Append(ctx, tx, j.Actor, nil, drafts)
}

// Backfill fills the file index for directory artifacts recorded before it existed (migration 0020), and restores
// evicted artifacts whose bytes are back in the store (copied from the backup mirror): their eviction is cleared.
// The control plane runs it at start; it is cheap when there is nothing to do.
func Backfill(ctx context.Context, pool *pgxpool.Pool, store *cas.Store) (indexed, restored int, err error) {
	if store == nil {
		return 0, 0, nil
	}
	rows, err := pool.Query(ctx, `SELECT hash FROM artifacts a WHERE directory AND evicted_at IS NULL
		AND NOT EXISTS (SELECT 1 FROM artifact_files f WHERE f.hash = a.hash)`)
	if err != nil {
		return 0, 0, fmt.Errorf("find unindexed directories: %w", err)
	}
	dirs, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return 0, 0, fmt.Errorf("read unindexed directories: %w", err)
	}
	for _, h := range dirs {
		m, err := store.ReadManifest(h)
		if err != nil {
			continue // the manifest is missing: nothing to index, Verify reports it where it matters
		}
		if err := artifacts.IndexFiles(ctx, pool, h, m.Files); err != nil {
			return indexed, 0, err
		}
		indexed++
	}
	rows, err = pool.Query(ctx, `SELECT hash, type, size FROM artifacts WHERE evicted_at IS NOT NULL`)
	if err != nil {
		return indexed, 0, fmt.Errorf("find evicted artifacts: %w", err)
	}
	type ev struct {
		hash, typ string
		size      int64
	}
	var evicted []ev
	var e ev
	if _, err := pgx.ForEachRow(rows, []any{&e.hash, &e.typ, &e.size}, func() error {
		evicted = append(evicted, e)
		return nil
	}); err != nil {
		return indexed, 0, fmt.Errorf("read evicted artifacts: %w", err)
	}
	for _, e := range evicted {
		if ok, _, _ := store.Has(e.hash); !ok {
			continue
		}
		var back bool
		// Verified under the store lock, like Record: an eviction job still deleting cannot remove what verified.
		err := pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
			if err := artifacts.LockShared(ctx, tx); err != nil {
				return err
			}
			if _, err := artifacts.Verify(store, artifactRef(e.hash, e.typ, e.size)); err != nil {
				return nil // only some of its blobs are back
			}
			if _, err := tx.Exec(ctx, `UPDATE artifacts SET evicted_at = NULL, evicted_by = NULL, eviction_job_id = NULL
				WHERE hash = $1`, e.hash); err != nil {
				return fmt.Errorf("restore %s: %w", e.hash, err)
			}
			back = true
			return nil
		})
		if err != nil {
			return indexed, restored, err
		}
		if back {
			restored++
		}
	}
	return indexed, restored, nil
}

func artifactRef(hash, typ string, size int64) steps.ArtifactRef {
	return steps.ArtifactRef{Hash: hash, Type: typ, Size: size}
}
