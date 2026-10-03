package workers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/usunrise88/cadence/control-plane/internal/events"
	"github.com/usunrise88/cadence/control-plane/internal/jobs"
	"github.com/usunrise88/cadence/control-plane/internal/obs"
	"github.com/usunrise88/cadence/control-plane/internal/queue"
	"github.com/usunrise88/cadence/control-plane/internal/steps"
)

// Await implements steps.Leases: it puts the step job jobID (a River job of kind "step" whose args are a
// steps.Spec) in the queue and blocks until the step ends — a worker released it, the reaper found its worker
// gone (Outcome with error type lost) or the job was cancelled (Outcome cancelled). A pause or a window close does
// not end it: the job waits in the queue and resumes. When ctx ends first (the control plane stops) Await returns
// ctx's error and leaves the queue as it is, so calling Await again for the same job picks up where it was; an
// ended job answers its stored outcome at once.
func (s *Service) Await(ctx context.Context, jobID string) (steps.Outcome, error) {
	if err := s.enqueue(ctx, jobID); err != nil {
		return steps.Outcome{}, err
	}
	for {
		o, done, err := s.check(ctx, jobID)
		if err != nil || done {
			return o, err
		}
		if !s.sleep(ctx, s.poll) {
			o, done, err := s.check(context.WithoutCancel(ctx), jobID)
			if err == nil && done {
				return o, nil
			}
			return steps.Outcome{}, ctx.Err()
		}
	}
}

// enqueue adds the job to step_jobs once; its priority starts at the spec's.
func (s *Service) enqueue(ctx context.Context, jobID string) error {
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var (
			kind string
			args []byte
			has  bool
		)
		err := tx.QueryRow(ctx, `SELECT j.kind, r.args->'args', EXISTS (SELECT 1 FROM step_jobs WHERE job_id = j.id)
			FROM jobs j JOIN river_job r ON r.id = j.river_id WHERE j.id = $1`, jobID).Scan(&kind, &args, &has)
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("await %s: no such job", jobID)
		}
		if err != nil {
			return fmt.Errorf("await %s: read job: %w", jobID, err)
		}
		if has {
			return nil
		}
		if kind != steps.JobKind && kind != steps.LiveJobKind {
			return fmt.Errorf("await %s: a %s job, not a %s or %s job", jobID, kind, steps.JobKind, steps.LiveJobKind)
		}
		var spec steps.Spec
		if err := json.Unmarshal(args, &spec); err != nil {
			return fmt.Errorf("await %s: args are not a step spec: %w", jobID, err)
		}
		if err := spec.Validate(); err != nil {
			return fmt.Errorf("await %s: %w", jobID, err)
		}
		need := queue.NeedOf(spec)
		now := s.now()
		if _, err := tx.Exec(ctx, `INSERT INTO step_jobs (job_id, project_id, spec, kind_ref, job_kind, gpu, memory_mb, traceparent,
				estimate_seconds, enqueued_at, updated_at)
			VALUES ($1, NULLIF($2, ''), $3, $4, $5, $6, $7, NULLIF($10, ''), $8, $9, $9)`,
			jobID, spec.ProjectID, spec, spec.KindRef(), need.JobKind, spec.Resources.GPU, need.MemoryMB, spec.EstimateSeconds, now,
			obs.Traceparent(ctx)); err != nil {
			return fmt.Errorf("await %s: enqueue: %w", jobID, err)
		}
		if _, err := tx.Exec(ctx, `UPDATE jobs SET priority = $2 WHERE id = $1`, jobID, spec.Priority); err != nil {
			return fmt.Errorf("await %s: priority: %w", jobID, err)
		}
		drafts, err := jobs.Progress(ctx, tx, jobID, 0, "waiting for a worker")
		if err != nil {
			return err
		}
		drafts = append(drafts, queueEvent(jobID, spec.ProjectID, "enqueued", map[string]any{"kind": spec.KindRef()}))
		return events.Append(ctx, tx, jobs.System, nil, drafts)
	})
	if err == nil {
		s.wake.broadcast()
	}
	return err
}

// check reads the step job: its outcome once ended; a cancelled job ends here as cancelled (a running step hears
// it at its next heartbeat and its card frees on release or reaping).
func (s *Service) check(ctx context.Context, jobID string) (steps.Outcome, bool, error) {
	var (
		o    steps.Outcome
		done bool
	)
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var (
			state     string
			outcome   []byte
			cancelled *time.Time
			projectID string
		)
		if err := tx.QueryRow(ctx, `SELECT s.state, s.outcome, j.cancel_requested_at, coalesce(s.project_id, '')
			FROM step_jobs s JOIN jobs j ON j.id = s.job_id WHERE s.job_id = $1 FOR UPDATE OF s`, jobID).
			Scan(&state, &outcome, &cancelled, &projectID); err != nil {
			return fmt.Errorf("read step job %s: %w", jobID, err)
		}
		switch {
		case state == "ended":
			done = true
			return json.Unmarshal(outcome, &o)
		case cancelled != nil:
			done = true
			o = steps.Outcome{State: steps.StateCancelled, Error: &steps.StepError{Type: steps.ErrCancelled, Message: "the job was cancelled"}}
			drafts, err := s.endStep(ctx, tx, jobID, projectID, o, s.now())
			if err != nil {
				return err
			}
			return events.Append(ctx, tx, jobs.System, nil, drafts)
		}
		return nil
	})
	return o, done, err
}
