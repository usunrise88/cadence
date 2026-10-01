// Package jobs runs long work on River (riverqueue/river) and mirrors each job into the `jobs` table the API shows.
//
// A command enqueues a job inside its own transaction (Service.Enqueue), so the job exists exactly when the command
// commits. Kinds run in-process: each registers a Handler at start (Service.Register). State changes and progress
// go out as events on job.{id} (docs/spec/06-platform.md, Topic scheme). Cancelling a queued job stops it at once;
// a running job's context is cancelled and the job ends as cancelled when its handler returns.
//
// River's own tables come from River's migrator (MigrateRiver), run at start beside Cadence's migrations under the
// same advisory lock.
package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
	"github.com/riverqueue/river/rivermigrate"
	"github.com/riverqueue/river/rivertype"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
	tracenoop "go.opentelemetry.io/otel/trace/noop"

	"github.com/usunrise88/cadence/control-plane/internal/auth"
	"github.com/usunrise88/cadence/control-plane/internal/events"
	"github.com/usunrise88/cadence/control-plane/internal/obs"
	"github.com/usunrise88/cadence/control-plane/internal/problems"
	"github.com/usunrise88/cadence/control-plane/internal/storage"
)

// Kind is the entity kind in references.
const Kind = "job"

// States.
const (
	StateQueued    = "queued"
	StateRunning   = "running"
	StateDone      = "done"
	StateFailed    = "failed"
	StateCancelled = "cancelled"
)

// Event types on job.{id}.
const (
	EventState    = "job.state_changed"
	EventProgress = "job.progress"
)

// KindNoop is the trivial kind tests use: it reports progress once and returns its args as the result.
const KindNoop = "noop"

// Topic is the topic of one job: job.{id}.
func Topic(id string) string { return "job." + id }

// Terminal reports whether state is an end state.
func Terminal(state string) bool {
	return state == StateDone || state == StateFailed || state == StateCancelled
}

// System is the actor of events the job runner emits.
var System = auth.Actor{Kind: auth.KindAutomation, ID: "cadence", Name: "Cadence"}

// Job is one row of the jobs table.
type Job struct {
	ID                string
	RiverID           int64
	Kind              string
	ProjectID         string
	State             string
	Progress          float64
	Message           string
	Result            json.RawMessage
	Error             string
	Attempt           int
	Actor             auth.Actor
	Rev               int
	CreatedAt         time.Time
	UpdatedAt         time.Time
	StartedAt         *time.Time
	FinishedAt        *time.Time
	CancelRequestedAt *time.Time
	Priority          int        // step jobs: start order in the queue, higher first
	PausedAt          *time.Time // step jobs: held in the queue (jobs.pause)
}

// View is a job's JSON form: the contract's Job.
type View struct {
	ID                string          `json:"id"`
	Kind              string          `json:"kind"`
	ProjectID         string          `json:"projectId,omitempty"`
	State             string          `json:"state"`
	Progress          float64         `json:"progress"`
	Message           string          `json:"message,omitempty"`
	Result            json.RawMessage `json:"result,omitempty"`
	Error             string          `json:"error,omitempty"`
	Attempt           int             `json:"attempt"`
	Rev               int             `json:"rev"`
	Actor             auth.Actor      `json:"actor"`
	CreatedAt         time.Time       `json:"createdAt"`
	UpdatedAt         time.Time       `json:"updatedAt"`
	StartedAt         *time.Time      `json:"startedAt,omitempty"`
	FinishedAt        *time.Time      `json:"finishedAt,omitempty"`
	CancelRequestedAt *time.Time      `json:"cancelRequestedAt,omitempty"`
	Priority          int             `json:"priority"`
	PausedAt          *time.Time      `json:"pausedAt,omitempty"`
}

// JSON renders j as the contract's Job.
func (j Job) JSON() View {
	return View{ID: j.ID, Kind: j.Kind, ProjectID: j.ProjectID, State: j.State, Progress: j.Progress, Message: j.Message,
		Result: j.Result, Error: j.Error, Attempt: j.Attempt, Rev: j.Rev, Actor: j.Actor, CreatedAt: j.CreatedAt,
		UpdatedAt: j.UpdatedAt, StartedAt: j.StartedAt, FinishedAt: j.FinishedAt, CancelRequestedAt: j.CancelRequestedAt,
		Priority: j.Priority, PausedAt: j.PausedAt}
}

const cols = `id, river_id, kind, coalesce(project_id, ''), state, progress, coalesce(message, ''), result,
	coalesce(error, ''), attempt, actor, rev, created_at, updated_at, started_at, finished_at, cancel_requested_at,
	priority, paused_at`

func scan(row pgx.CollectableRow) (Job, error) {
	var j Job
	err := row.Scan(&j.ID, &j.RiverID, &j.Kind, &j.ProjectID, &j.State, &j.Progress, &j.Message, &j.Result, &j.Error,
		&j.Attempt, &j.Actor, &j.Rev, &j.CreatedAt, &j.UpdatedAt, &j.StartedAt, &j.FinishedAt, &j.CancelRequestedAt,
		&j.Priority, &j.PausedAt)
	return j, err
}

func one(rows pgx.Rows, err error, id string) (Job, bool, error) {
	if err != nil {
		return Job{}, false, fmt.Errorf("query job: %w", err)
	}
	j, err := pgx.CollectExactlyOneRow(rows, scan)
	if errors.Is(err, pgx.ErrNoRows) {
		return Job{}, false, nil
	}
	if err != nil {
		return Job{}, false, fmt.Errorf("read job %s: %w", id, err)
	}
	return j, true, nil
}

func existing(rows pgx.Rows, err error, id string) (Job, error) {
	j, found, err := one(rows, err, id)
	if err == nil && !found {
		err = problems.NotFound.New("no job %q", id)
	}
	return j, err
}

// Get returns the job with id, or not-found.
func Get(ctx context.Context, q storage.Querier, id string) (Job, error) {
	rows, err := q.Query(ctx, "SELECT "+cols+" FROM jobs WHERE id = $1", id)
	return existing(rows, err, id)
}

// List returns a project's jobs, newest first, optionally in one state.
func List(ctx context.Context, q storage.Querier, projectID, state string, limit int) ([]Job, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := q.Query(ctx, "SELECT "+cols+` FROM jobs WHERE project_id = $1 AND ($2 = '' OR state = $2)
		ORDER BY created_at DESC, id DESC LIMIT $3`, projectID, state, limit)
	if err != nil {
		return nil, fmt.Errorf("list jobs: %w", err)
	}
	out, err := pgx.CollectRows(rows, scan)
	if err != nil {
		return nil, fmt.Errorf("read jobs: %w", err)
	}
	return out, nil
}

// Wait returns the job once it is in an end state or when timeout passes, whichever comes first; poll is the
// interval between reads.
func Wait(ctx context.Context, q storage.Querier, id string, timeout, poll time.Duration) (Job, error) {
	deadline := time.Now().Add(timeout)
	for {
		j, err := Get(ctx, q, id)
		if err != nil || Terminal(j.State) || !time.Now().Before(deadline) {
			return j, err
		}
		wait := min(poll, time.Until(deadline))
		select {
		case <-ctx.Done():
			return j, nil // the caller left; answer with what we have
		case <-time.After(wait):
		}
	}
}

func stateDraft(j Job, typ string) []events.Draft {
	return []events.Draft{{
		Topic: Topic(j.ID), Type: typ, ProjectID: j.ProjectID,
		Entity:  &events.EntityRef{Kind: Kind, ID: j.ID, Rev: j.Rev},
		Payload: map[string]any{"job": j.JSON()},
	}}
}

// Handler does the work of one kind. Its result is stored as JSON on the job. A handler must return when ctx is
// cancelled (the job was cancelled or the server is stopping).
type Handler func(ctx context.Context, run *Run) (result any, err error)

// KindOptions tune one kind.
type KindOptions struct {
	MaxAttempts int           // default 1: in-process kinds are not retried unless they ask for it
	Timeout     time.Duration // default 10 min
	// Queue is the River queue the kind runs on: river.QueueDefault when empty, QueueSteps for kinds whose handler
	// waits for a worker (they must not take the default queue's few slots from other work).
	Queue string
}

// queueWorkers is how many jobs of a named queue other than the default run at once.
const queueWorkers = 100

// Run is one execution of a job, handed to its Handler.
type Run struct {
	Job  Job
	Args json.RawMessage
	svc  *Service
}

// Progress records how far the job is (0–1) with a short message and emits job.progress.
func (r *Run) Progress(ctx context.Context, fraction float64, message string) error {
	fraction = max(0, min(1, fraction))
	return r.svc.update(ctx, r.Job.ID, EventProgress, func(ctx context.Context, tx pgx.Tx) (pgx.Rows, error) {
		return tx.Query(ctx, `UPDATE jobs SET progress = $2, message = NULLIF($3, ''), rev = rev + 1, updated_at = now()
			WHERE id = $1 AND state = 'running' RETURNING `+cols, r.Job.ID, fraction, message)
	})
}

// riverArgs is the River job of every Cadence kind: River's kind is the Cadence kind.
type riverArgs struct {
	K     string          `json:"kind"`
	JobID string          `json:"jobId"`
	Args  json.RawMessage `json:"args,omitempty"`
	// Trace is the W3C traceparent of the request that enqueued the job; each attempt's span continues it.
	Trace string `json:"trace,omitempty"`
}

func (a riverArgs) Kind() string { return a.K }

// maintenanceArgs are periodic internal chores (approval expiry, audit retention); they have no jobs row.
type maintenanceArgs struct {
	K string `json:"kind"`
}

func (a maintenanceArgs) Kind() string { return a.K }

// Service owns the River client, the registered kinds and the jobs mirror.
type Service struct {
	pool     *pgxpool.Pool
	log      *slog.Logger
	workers  *river.Workers
	kinds    map[string]kindEntry
	periodic []*river.PeriodicJob
	client   *river.Client[pgx.Tx]
	// FetchPollInterval overrides River's poll interval (tests); zero keeps River's default.
	FetchPollInterval time.Duration
	// Tracer records a span per job attempt (main sets the file exporter's provider); nil records nothing.
	Tracer trace.TracerProvider
}

type kindEntry struct {
	handler Handler
	opts    KindOptions
}

// New returns a service with the noop kind registered; register kinds, then Start.
func New(pool *pgxpool.Pool, log *slog.Logger) *Service {
	s := &Service{pool: pool, log: log, workers: river.NewWorkers(), kinds: map[string]kindEntry{}}
	s.Register(KindNoop, noop, KindOptions{})
	return s
}

func noop(ctx context.Context, run *Run) (any, error) {
	var args struct {
		SleepMS int    `json:"sleepMs"`
		Fail    string `json:"fail"`
	}
	if len(run.Args) > 0 {
		_ = json.Unmarshal(run.Args, &args)
	}
	if err := run.Progress(ctx, 0.5, "halfway"); err != nil {
		return nil, err
	}
	if args.SleepMS > 0 {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(time.Duration(args.SleepMS) * time.Millisecond):
		}
	}
	if args.Fail != "" {
		return nil, errors.New(args.Fail)
	}
	return map[string]any{"args": run.Args}, nil
}

// Register adds an in-process kind; call it before Start.
func (s *Service) Register(kind string, h Handler, opts KindOptions) {
	if opts.MaxAttempts <= 0 {
		opts.MaxAttempts = 1
	}
	if opts.Timeout <= 0 {
		opts.Timeout = 10 * time.Minute
	}
	if opts.Queue == "" {
		opts.Queue = river.QueueDefault
	}
	s.kinds[kind] = kindEntry{handler: h, opts: opts}
	river.AddWorkerArgs(s.workers, riverArgs{K: kind}, &worker{svc: s, timeout: opts.Timeout})
}

// AddPeriodic runs fn every interval (and once at start) on the elected River leader; for internal chores.
func (s *Service) AddPeriodic(kind string, every time.Duration, fn func(ctx context.Context) error) {
	river.AddWorkerArgs(s.workers, maintenanceArgs{K: kind},
		river.WorkFunc(func(ctx context.Context, _ *river.Job[maintenanceArgs]) error { return fn(ctx) }))
	s.periodic = append(s.periodic, river.NewPeriodicJob(river.PeriodicInterval(every),
		func() (river.JobArgs, *river.InsertOpts) {
			return maintenanceArgs{K: kind}, &river.InsertOpts{MaxAttempts: 1}
		}, &river.PeriodicJobOpts{RunOnStart: true, ID: kind}))
}

// Start creates the River client and starts working; it returns once River runs.
func (s *Service) Start(ctx context.Context) error {
	queues := map[string]river.QueueConfig{river.QueueDefault: {MaxWorkers: 10}}
	for _, k := range s.kinds {
		if _, ok := queues[k.opts.Queue]; !ok {
			n := queueWorkers
			if k.opts.Queue == QueueSteps {
				n = QueueStepsWorkers
			}
			queues[k.opts.Queue] = river.QueueConfig{MaxWorkers: n}
		}
	}
	client, err := river.NewClient(riverpgxv5.New(s.pool), &river.Config{
		Queues:            queues,
		Workers:           s.workers,
		PeriodicJobs:      s.periodic,
		Logger:            s.log,
		FetchPollInterval: s.FetchPollInterval,
	})
	if err != nil {
		return fmt.Errorf("create job client: %w", err)
	}
	s.client = client
	if err := client.Start(ctx); err != nil {
		return fmt.Errorf("start job client: %w", err)
	}
	return nil
}

// Stop waits for running jobs to finish, cancelling them when ctx ends.
func (s *Service) Stop(ctx context.Context) error {
	if s.client == nil {
		return nil
	}
	if err := s.client.Stop(ctx); err != nil {
		if cerr := s.client.StopAndCancel(context.WithoutCancel(ctx)); cerr != nil {
			return errors.Join(err, cerr)
		}
	}
	return nil
}

// Spec describes a job to enqueue.
type Spec struct {
	Kind      string
	ProjectID string // empty for registry work
	Args      any    // marshalled as JSON and handed to the handler
}

// Enqueue inserts a job inside the caller's transaction; it is attributed to the actor in ctx. It returns the job
// with the events the caller emits with its own (a job.state_changed on job.{id}).
func (s *Service) Enqueue(ctx context.Context, tx pgx.Tx, spec Spec) (Job, []events.Draft, error) {
	if s.client == nil {
		return Job{}, nil, errors.New("jobs: the job service is not started")
	}
	k, ok := s.kinds[spec.Kind]
	if !ok {
		return Job{}, nil, fmt.Errorf("jobs: no handler for kind %q", spec.Kind)
	}
	var args json.RawMessage
	if spec.Args != nil {
		b, err := json.Marshal(spec.Args)
		if err != nil {
			return Job{}, nil, fmt.Errorf("marshal %s args: %w", spec.Kind, err)
		}
		args = b
	}
	actor, _ := auth.FromContext(ctx)
	id := "job_" + uuid.Must(uuid.NewV7()).String()
	res, err := s.client.InsertTx(ctx, tx, riverArgs{K: spec.Kind, JobID: id, Args: args, Trace: obs.Traceparent(ctx)},
		&river.InsertOpts{MaxAttempts: k.opts.MaxAttempts, Queue: k.opts.Queue})
	if err != nil {
		return Job{}, nil, fmt.Errorf("enqueue %s: %w", spec.Kind, err)
	}
	rows, err := tx.Query(ctx, `INSERT INTO jobs (id, river_id, kind, project_id, actor) VALUES ($1, $2, $3, NULLIF($4, ''), $5)
		RETURNING `+cols, id, res.Job.ID, spec.Kind, spec.ProjectID, actor)
	j, err := existing(rows, err, id)
	if err != nil {
		return Job{}, nil, err
	}
	return j, stateDraft(j, EventState), nil
}

// Cancel cancels the job at revision rev: a queued job ends as cancelled now; a running job gets
// cancelRequestedAt and ends as cancelled when its handler returns. An ended job is a conflict.
func (s *Service) Cancel(ctx context.Context, tx pgx.Tx, id string, rev int) (Job, []events.Draft, error) {
	rows, err := tx.Query(ctx, "SELECT "+cols+" FROM jobs WHERE id = $1 FOR UPDATE", id)
	cur, err := existing(rows, err, id)
	if err != nil {
		return Job{}, nil, err
	}
	if cur.Rev != rev {
		return Job{}, nil, problems.Stale(cur.Rev, "the job is at revision %d, not %d; re-read it and retry", cur.Rev, rev)
	}
	if Terminal(cur.State) {
		return Job{}, nil, problems.Conflict.New("job %s already ended (%s)", id, cur.State)
	}
	if s.client == nil {
		return Job{}, nil, errors.New("jobs: the job service is not started")
	}
	row, err := s.client.JobCancelTx(ctx, tx, cur.RiverID)
	if err != nil && !errors.Is(err, rivertype.ErrNotFound) {
		return Job{}, nil, fmt.Errorf("cancel river job %d: %w", cur.RiverID, err)
	}
	if row == nil || row.State == rivertype.JobStateCancelled {
		rows, err = tx.Query(ctx, `UPDATE jobs SET state = 'cancelled', cancel_requested_at = now(), finished_at = now(),
			rev = rev + 1, updated_at = now() WHERE id = $1 RETURNING `+cols, id)
	} else {
		rows, err = tx.Query(ctx, `UPDATE jobs SET cancel_requested_at = now(), rev = rev + 1, updated_at = now()
			WHERE id = $1 RETURNING `+cols, id)
	}
	j, err := existing(rows, err, id)
	if err != nil {
		return Job{}, nil, err
	}
	return j, stateDraft(j, EventState), nil
}

// update runs one mirror change in its own transaction and emits its event; a change that matched no row (the
// job moved on meanwhile) is skipped.
func (s *Service) update(ctx context.Context, id, typ string, q func(context.Context, pgx.Tx) (pgx.Rows, error)) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := q(ctx, tx)
		j, found, err := one(rows, err, id)
		if err != nil || !found {
			return err
		}
		return events.Append(ctx, tx, System, nil, stateDraft(j, typ))
	})
}

// worker runs every Cadence kind: it moves the mirror through running → done | failed | cancelled.
type worker struct {
	river.WorkerDefaults[riverArgs]
	svc     *Service
	timeout time.Duration
}

func (w *worker) Timeout(*river.Job[riverArgs]) time.Duration { return w.timeout }

// Work runs one attempt in a span that continues the trace of the request that enqueued the job.
func (w *worker) Work(ctx context.Context, rj *river.Job[riverArgs]) (err error) {
	s := w.svc
	ctx, span := s.tracer().Tracer("cadence/jobs").Start(obs.WithTraceparent(ctx, rj.Args.Trace), "job "+rj.Args.K,
		trace.WithSpanKind(trace.SpanKindConsumer), trace.WithAttributes(
			attribute.String("cadence.job.id", rj.Args.JobID), attribute.Int("cadence.job.attempt", rj.Attempt)))
	defer func() {
		if err != nil {
			span.RecordError(err)
			span.SetStatus(codes.Error, err.Error())
		}
		span.End()
	}()
	return w.work(ctx, rj)
}

func (s *Service) tracer() trace.TracerProvider {
	if s.Tracer != nil {
		return s.Tracer
	}
	return tracenoop.NewTracerProvider()
}

func (w *worker) work(ctx context.Context, rj *river.Job[riverArgs]) error {
	s, id := w.svc, rj.Args.JobID
	k, ok := s.kinds[rj.Args.K]
	if !ok {
		return river.JobCancel(fmt.Errorf("no handler for kind %q", rj.Args.K))
	}
	var j Job
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `UPDATE jobs SET state = 'running', attempt = $2, started_at = coalesce(started_at, now()),
			error = NULL, rev = rev + 1, updated_at = now()
			WHERE id = $1 AND state IN ('queued', 'running') AND cancel_requested_at IS NULL RETURNING `+cols, id, rj.Attempt)
		var found bool
		j, found, err = one(rows, err, id)
		if err != nil {
			return err
		}
		if !found {
			return errCancelled
		}
		return events.Append(ctx, tx, System, nil, stateDraft(j, EventState))
	})
	if errors.Is(err, errCancelled) {
		// Cancelled after River fetched the job but before this handler marked it running: jobs.cancel saw a
		// running River job and only set cancel_requested_at, so the mirror is still queued. End it here, or it
		// stays queued forever (end skips a job that already ended).
		if err := s.end(context.WithoutCancel(ctx), id, StateCancelled, "cancelled before it started"); err != nil {
			return err
		}
		return river.JobCancel(err)
	}
	if err != nil {
		return fmt.Errorf("mark job %s running: %w", id, err)
	}

	result, herr := k.handler(ctx, &Run{Job: j, Args: rj.Args.Args, svc: s})
	// The handler's context may be cancelled; finishing the mirror must not be.
	fctx := context.WithoutCancel(ctx)
	if herr == nil {
		return w.finish(fctx, rj, result)
	}
	cancelled, err := s.cancelRequested(fctx, id)
	if err != nil {
		return err
	}
	switch {
	case !cancelled && errors.Is(herr, ErrInterrupted):
		// The control plane is stopping while the handler waits on work that goes on elsewhere (a worker's lease):
		// the mirror stays running and River hands the job to the next start without spending an attempt, where
		// the handler picks the work up again.
		return river.JobSnooze(0)
	case cancelled:
		if err := s.end(fctx, id, StateCancelled, herr.Error()); err != nil {
			return err
		}
		return river.JobCancel(herr)
	case rj.Attempt >= rj.MaxAttempts:
		if err := s.end(fctx, id, StateFailed, herr.Error()); err != nil {
			return err
		}
	default:
		if err := s.update(fctx, id, EventState, func(ctx context.Context, tx pgx.Tx) (pgx.Rows, error) {
			return tx.Query(ctx, `UPDATE jobs SET state = 'queued', error = $2, rev = rev + 1, updated_at = now()
				WHERE id = $1 RETURNING `+cols, id, herr.Error())
		}); err != nil {
			return err
		}
	}
	return herr
}

var errCancelled = errors.New("the job was cancelled before it started")

// ErrInterrupted marks a handler error as "stopped by the control plane's shutdown, not failed": a handler that waits
// on work running elsewhere returns it (wrapped) when its context is cancelled, and the job runs again on the next
// start with the same attempt (River's snooze) instead of failing or retrying.
var ErrInterrupted = errors.New("interrupted by the control plane stopping; it continues on the next start")

// finish stores the result and completes the River job in the same transaction.
func (w *worker) finish(ctx context.Context, rj *river.Job[riverArgs], result any) error {
	s, id := w.svc, rj.Args.JobID
	var body []byte
	if result != nil {
		b, err := json.Marshal(result)
		if err != nil {
			return s.end(ctx, id, StateFailed, fmt.Sprintf("the result is not JSON: %v", err))
		}
		body = b
	}
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `UPDATE jobs SET state = 'done', progress = 1, result = $2, finished_at = now(),
			rev = rev + 1, updated_at = now() WHERE id = $1 RETURNING `+cols, id, body)
		j, err := existing(rows, err, id)
		if err != nil {
			return err
		}
		if _, err := river.JobCompleteTx[*riverpgxv5.Driver](ctx, tx, rj); err != nil {
			return fmt.Errorf("complete river job: %w", err)
		}
		return events.Append(ctx, tx, System, nil, stateDraft(j, EventState))
	})
}

func (s *Service) end(ctx context.Context, id, state, msg string) error {
	return s.update(ctx, id, EventState, func(ctx context.Context, tx pgx.Tx) (pgx.Rows, error) {
		return tx.Query(ctx, `UPDATE jobs SET state = $2, error = NULLIF($3, ''), finished_at = now(), rev = rev + 1,
			updated_at = now() WHERE id = $1 AND state NOT IN ('done', 'failed', 'cancelled') RETURNING `+cols, id, state, msg)
	})
}

func (s *Service) cancelRequested(ctx context.Context, id string) (bool, error) {
	var at *time.Time
	if err := s.pool.QueryRow(ctx, "SELECT cancel_requested_at FROM jobs WHERE id = $1", id).Scan(&at); err != nil {
		return false, fmt.Errorf("read job %s: %w", id, err)
	}
	return at != nil, nil
}

// MigrateRiver applies River's own migrations, each version in its own transaction (some add enum values, which
// Postgres does not allow to be used in the transaction that added them). Pass it to storage.Migrate so it runs
// under the migration lock.
func MigrateRiver(ctx context.Context, pool *pgxpool.Pool) error {
	m, err := rivermigrate.New(riverpgxv5.New(pool), &rivermigrate.Config{Logger: slog.New(slog.DiscardHandler)})
	if err != nil {
		return fmt.Errorf("river migrator: %w", err)
	}
	if _, err := m.Migrate(ctx, rivermigrate.DirectionUp, nil); err != nil {
		return fmt.Errorf("river migrations: %w", err)
	}
	return nil
}
