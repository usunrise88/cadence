// Package workers is the worker protocol (docs/review/2026-09-30-phase-2-plan.md "Worker protocol", R14): workers
// register their runtime, step kinds and model families as registry versions, long-poll for leases on the step
// queue, heartbeat with progress and card telemetry, stream logs and metric points, and release a lease with the
// step's outcome. It also implements steps.Leases for the pipeline engine: a "step" job's handler calls Await,
// which puts the job in the queue and blocks until a worker's release, the reaper or a cancel ends it.
//
// The queue lives in step_jobs (one row per awaited step job; it outlives leases, so a paused or window-closed job
// returns to the queue in its place) and leases (one per attempt on a card). Where a job may run is
// internal/queue's decision; this package loads the state, takes the card locks and records the result.
package workers

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"log/slog"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/usunrise88/cadence/control-plane/internal/auth"
	"github.com/usunrise88/cadence/control-plane/internal/cas"
	"github.com/usunrise88/cadence/control-plane/internal/events"
	"github.com/usunrise88/cadence/control-plane/internal/steps"
)

// Defaults of the protocol.
const (
	HeartbeatSeconds = 10               // a worker reports every 10 s
	MissedBeats      = 3                // three missed beats reap the lease
	MaxClaimWait     = 30 * time.Second // the longest long-poll
	GPUEventInterval = 5 * time.Second  // at most one gpu event per host this often
	LogRetention     = 14 * 24 * time.Hour
	MaxLogBytes      = 4 << 20  // read per workerLogs.new request (the command body limit); the rest is discarded
	MaxLogLineBytes  = 64 << 10 // one stored log line; a longer line is truncated, never refused
	maxMsgBytes      = 16000    // a log line's msg
	maxEventLines    = 200      // log lines carried by one job.{id}.log event
	maxEventPoints   = 1000     // metric points carried by one run.{id}.metrics event
)

// Topics and event types of the worker protocol (docs/spec/06-platform.md "Topic scheme").
const (
	TopicQueue      = "queue"
	TopicGPU        = "gpu"
	EventQueue      = "queue.changed"
	EventGPU        = "gpu.telemetry"
	EventLog        = "job.log"
	EventMetrics    = "run.metrics"
	EventRegistered = "worker.registered"
)

// Secrets reads secret values for the lease's environment (secrets.Store).
type Secrets interface {
	Read(ctx context.Context, name string) ([]byte, error)
}

// Service is the worker protocol on one database.
type Service struct {
	pool    *pgxpool.Pool
	secrets Secrets
	cas     *cas.Store
	logDir  string
	log     *slog.Logger
	now     func() time.Time
	poll    time.Duration
	beat    int
	started time.Time // when this process started; Reap grants a grace after it
	wake    notifier
	logMu   sync.Mutex
	logSeq  map[string]int // lines per job log, counted once per process
	// onLeased runs in the transaction that grants a lease (the pipeline engine marks the step running).
	onLeased func(ctx context.Context, tx pgx.Tx, jobID string) ([]events.Draft, error)
	// onPublished records an intermediate output (workerOutputs.new) in its transaction.
	onPublished PublishHook
}

// OnLeased sets fn to run in the transaction that grants a lease; its events are emitted with the grant. The
// pipeline engine uses it to move the step from queued to running (pipelines.Engine.Leased).
func (s *Service) OnLeased(fn func(ctx context.Context, tx pgx.Tx, jobID string) ([]events.Draft, error)) {
	s.onLeased = fn
}

// Options configure a Service.
type Options struct {
	Pool    *pgxpool.Pool
	Secrets Secrets    // nil: steps that declare secrets fail at lease time
	CAS     *cas.Store // nil: outputs are not checked and uploads answer 501
	LogDir  string     // $CADENCE_DATA_DIR/job-logs; empty: workerLogs.new answers 501
	Log     *slog.Logger
	Now     func() time.Time // the clock of heartbeats, reaping and windows (tests move it)
	// Poll is how often waiting loops (claim, Await) re-read the queue besides being woken in-process (500 ms).
	Poll time.Duration
	// HeartbeatSeconds is the interval told to workers (10); reaping happens after MissedBeats of them.
	HeartbeatSeconds int
}

// New returns a service.
func New(o Options) *Service {
	s := &Service{pool: o.Pool, secrets: o.Secrets, cas: o.CAS, logDir: o.LogDir, log: o.Log, now: o.Now, poll: o.Poll,
		beat: o.HeartbeatSeconds, logSeq: map[string]int{}}
	if s.now == nil {
		s.now = time.Now
	}
	s.started = s.now()
	if s.poll <= 0 {
		s.poll = 500 * time.Millisecond
	}
	if s.beat <= 0 {
		s.beat = HeartbeatSeconds
	}
	if s.log == nil {
		s.log = slog.New(slog.DiscardHandler)
	}
	return s
}

var _ steps.Leases = (*Service)(nil)

// LogDir is where job logs are kept ("" when logs are off).
func (s *Service) LogDir() string { return s.logDir }

// lostAfter is how long a lease lives without a heartbeat.
func (s *Service) lostAfter() time.Duration { return time.Duration(MissedBeats*s.beat) * time.Second }

// Caller is who speaks the protocol: the worker credential of one compute host, or (Host empty) the fixed actor of
// tests and development, which may act for any host.
type Caller struct {
	Actor        auth.Actor
	CredentialID string
	Host         string // compute host name the credential belongs to
}

func (c Caller) allows(host string) bool { return c.Host == "" || c.Host == host }

// notifier wakes every waiter at once (claims and Awaits in this process); waiters also poll, so a change made by
// another process is seen within Poll.
type notifier struct {
	mu sync.Mutex
	ch chan struct{}
}

func (n *notifier) wait() <-chan struct{} {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.ch == nil {
		n.ch = make(chan struct{})
	}
	return n.ch
}

func (n *notifier) broadcast() {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.ch != nil {
		close(n.ch)
		n.ch = nil
	}
}

// Wake tells waiting claims and Awaits that the queue changed (after a pause, resume or reorder commits).
func (s *Service) Wake() { s.wake.broadcast() }

// sleep waits for a wake-up, the poll interval or ctx, whichever comes first; it reports false when ctx ended.
func (s *Service) sleep(ctx context.Context, limit time.Duration) bool {
	d := min(s.poll, limit)
	if d <= 0 {
		d = time.Millisecond
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-s.wake.wait():
	case <-t.C:
	}
	return true
}

// leaseTrace is the trace context a lease hands the worker: the job span's (so the worker's step spans are its
// children, in the trace of the request that started the job), or, for a job queued without a span, one derived
// from the job and the lease.
func leaseTrace(c candidate, leaseID string) string {
	if c.trace != "" {
		return c.trace
	}
	return traceparent(c.jobID, leaseID)
}

// traceparent is a derived W3C trace context for a lease: the trace is the job's (derived from its id, so every
// attempt of a job shares it), the parent span the lease's.
func traceparent(jobID, leaseID string) string {
	t := sha256.Sum256([]byte(jobID))
	p := sha256.Sum256([]byte(leaseID))
	return "00-" + hex.EncodeToString(t[:16]) + "-" + hex.EncodeToString(p[:8]) + "-01"
}
