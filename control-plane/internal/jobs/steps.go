package jobs

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/usunrise88/cadence/control-plane/internal/events"
	"github.com/usunrise88/cadence/control-plane/internal/problems"
)

// QueueSteps is the River queue of step jobs (kind "step", internal/steps): each handler waits for a worker to run
// its step, so the queue has room for many waiting handlers and never starves the default queue's chores.
const QueueSteps = "steps"

// QueueStepsWorkers is how many step handlers may wait at once; a waiting handler costs one goroutine.
const QueueStepsWorkers = 500

// Progress records a running job's progress from outside its handler (the worker protocol's heartbeats) inside the
// caller's transaction; a job that is not running is left alone and nothing is emitted. Progress does not change the
// job's revision (only state, priority, pause and cancel do), so a command sent with a revision read a moment ago
// does not race the heartbeats; job.progress still carries the new progress.
func Progress(ctx context.Context, tx pgx.Tx, id string, fraction float64, message string) ([]events.Draft, error) {
	fraction = max(0, min(1, fraction))
	rows, err := tx.Query(ctx, `UPDATE jobs SET progress = $2, message = NULLIF($3, ''), updated_at = now()
		WHERE id = $1 AND state = 'running' RETURNING `+cols, id, fraction, message)
	j, found, err := one(rows, err, id)
	if err != nil || !found {
		return nil, err
	}
	return stateDraft(j, EventProgress), nil
}

// lockAt reads the job at revision rev for update, or answers not-found / a stale revision.
func lockAt(ctx context.Context, tx pgx.Tx, id string, rev int) (Job, error) {
	rows, err := tx.Query(ctx, "SELECT "+cols+" FROM jobs WHERE id = $1 FOR UPDATE", id)
	cur, err := existing(rows, err, id)
	if err != nil {
		return Job{}, err
	}
	if cur.Rev != rev {
		return Job{}, problems.Stale(cur.Rev, "the job is at revision %d, not %d; re-read it and retry", cur.Rev, rev)
	}
	if Terminal(cur.State) {
		return Job{}, problems.Conflict.New("job %s already ended (%s)", id, cur.State)
	}
	return cur, nil
}

// Lock reads the job at revision rev for update inside tx: not-found, a stale revision (412) or an ended job
// (conflict) fail.
func Lock(ctx context.Context, tx pgx.Tx, id string, rev int) (Job, error) {
	return lockAt(ctx, tx, id, rev)
}

// SetPriority changes the job's priority (jobs.edit) and emits job.state_changed.
func SetPriority(ctx context.Context, tx pgx.Tx, id string, rev, priority int) (Job, []events.Draft, error) {
	if _, err := lockAt(ctx, tx, id, rev); err != nil {
		return Job{}, nil, err
	}
	rows, err := tx.Query(ctx, `UPDATE jobs SET priority = $2, rev = rev + 1, updated_at = now() WHERE id = $1 RETURNING `+cols,
		id, priority)
	j, err := existing(rows, err, id)
	if err != nil {
		return Job{}, nil, fmt.Errorf("set job priority: %w", err)
	}
	return j, stateDraft(j, EventState), nil
}

// SetPaused pauses or resumes the job (jobs.pause | jobs.resume) and emits job.state_changed; pausing a paused job
// or resuming a running one is a conflict.
func SetPaused(ctx context.Context, tx pgx.Tx, id string, rev int, paused bool) (Job, []events.Draft, error) {
	cur, err := lockAt(ctx, tx, id, rev)
	if err != nil {
		return Job{}, nil, err
	}
	switch {
	case paused && cur.PausedAt != nil:
		return Job{}, nil, problems.Conflict.New("job %s is already paused", id)
	case !paused && cur.PausedAt == nil:
		return Job{}, nil, problems.Conflict.New("job %s is not paused", id)
	case paused && cur.CancelRequestedAt != nil:
		return Job{}, nil, problems.Conflict.New("job %s is being cancelled", id)
	}
	// The message says what happens now: the worker's last progress line ("stopped; training state written") would
	// read as current long after a resume.
	rows, err := tx.Query(ctx, `UPDATE jobs SET paused_at = CASE WHEN $2 THEN now() END, rev = rev + 1, updated_at = now(),
			message = CASE WHEN $2 THEN 'paused: held in the queue until jobs.resume' ELSE 'resumed: waiting for a card' END
		WHERE id = $1 RETURNING `+cols, id, paused)
	j, err := existing(rows, err, id)
	if err != nil {
		return Job{}, nil, fmt.Errorf("pause job: %w", err)
	}
	return j, stateDraft(j, EventState), nil
}
