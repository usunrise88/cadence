package workers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/usunrise88/cadence/control-plane/internal/cas"
	"github.com/usunrise88/cadence/control-plane/internal/events"
	"github.com/usunrise88/cadence/control-plane/internal/jobs"
	"github.com/usunrise88/cadence/control-plane/internal/problems"
	"github.com/usunrise88/cadence/control-plane/internal/steps"
	"github.com/usunrise88/cadence/control-plane/internal/storage"
)

// stepJob locks the job at rev and checks that it runs on a worker.
func stepJob(ctx context.Context, tx pgx.Tx, id string, rev int) (jobs.Job, error) {
	j, err := jobs.Lock(ctx, tx, id, rev)
	if err != nil {
		return jobs.Job{}, err
	}
	if j.Kind != steps.JobKind {
		return jobs.Job{}, problems.Conflict.New("job %s is a %s job; only step jobs (run on a worker) can be paused or reordered", id, j.Kind)
	}
	return j, nil
}

// Pause holds a step job (jobs.pause): a waiting one is not leased until Resume; a running one is told to stop at
// its next heartbeat and returns to the queue with its saved training state. Call Wake after the commit.
func (s *Service) Pause(ctx context.Context, tx pgx.Tx, id string, rev int) (jobs.Job, []events.Draft, error) {
	if _, err := stepJob(ctx, tx, id, rev); err != nil {
		return jobs.Job{}, nil, err
	}
	j, drafts, err := jobs.SetPaused(ctx, tx, id, rev, true)
	if err != nil {
		return jobs.Job{}, nil, err
	}
	return j, append(drafts, queueEvent(j.ID, j.ProjectID, "paused", nil)), nil
}

// Resume releases a paused step job back into the queue (jobs.resume). Call Wake after the commit.
func (s *Service) Resume(ctx context.Context, tx pgx.Tx, id string, rev int) (jobs.Job, []events.Draft, error) {
	if _, err := stepJob(ctx, tx, id, rev); err != nil {
		return jobs.Job{}, nil, err
	}
	j, drafts, err := jobs.SetPaused(ctx, tx, id, rev, false)
	if err != nil {
		return jobs.Job{}, nil, err
	}
	return j, append(drafts, queueEvent(j.ID, j.ProjectID, "resumed", nil)), nil
}

// Prioritize changes a step job's priority (jobs.edit, the Queue's reorder). Call Wake after the commit.
func (s *Service) Prioritize(ctx context.Context, tx pgx.Tx, id string, rev, priority int) (jobs.Job, []events.Draft, error) {
	if _, err := stepJob(ctx, tx, id, rev); err != nil {
		return jobs.Job{}, nil, err
	}
	j, drafts, err := jobs.SetPriority(ctx, tx, id, rev, priority)
	if err != nil {
		return jobs.Job{}, nil, err
	}
	return j, append(drafts, queueEvent(j.ID, j.ProjectID, "reprioritized", map[string]any{"priority": priority})), nil
}

// QueueLease is the lease of a running queue entry (the contract's QueueLease).
type QueueLease struct {
	ID          string    `json:"id"`
	WorkerID    string    `json:"workerId"`
	Runtime     string    `json:"runtime,omitempty"`
	Host        string    `json:"host"`
	Card        int       `json:"card"`
	MemoryCapMB int       `json:"memoryCapMb"`
	StartedAt   time.Time `json:"startedAt"`
	HeartbeatAt time.Time `json:"heartbeatAt"`
	Progress    *float64  `json:"progress,omitempty"`
	Message     string    `json:"message,omitempty"`
	StopReason  string    `json:"stopReason,omitempty"`
}

// Entry is one step job in the queue (the contract's QueueEntry).
type Entry struct {
	JobID           string      `json:"jobId"`
	ProjectID       string      `json:"projectId,omitempty"`
	PipelineRunID   string      `json:"pipelineRunId,omitempty"`
	StepID          string      `json:"stepId,omitempty"`
	RunID           string      `json:"runId,omitempty"`
	Kind            string      `json:"kind"`
	KindVersion     string      `json:"kindVersion"`
	JobKind         string      `json:"jobKind"`
	GPU             bool        `json:"gpu"`
	MemoryGB        float64     `json:"memoryGb,omitempty"`
	State           string      `json:"state"`
	Priority        int         `json:"priority"`
	ProjectPriority int         `json:"projectPriority"`
	EnqueuedAt      time.Time   `json:"enqueuedAt"`
	EstimateSeconds *float64    `json:"estimateSeconds,omitempty"`
	Attempt         int         `json:"attempt"`
	ResumeFrom      string      `json:"resumeFrom,omitempty"`
	Lease           *QueueLease `json:"lease,omitempty"`
}

// Queue lists the step jobs not yet ended: running ones first, then in start order (project queue priority, job
// priority, first come). projectID filters when set.
func Queue(ctx context.Context, q storage.Querier, projectID string) ([]Entry, error) {
	rows, err := q.Query(ctx, `SELECT s.job_id, s.spec, s.state, s.job_kind, s.enqueued_at, j.priority,
			`+projectPriority("$2")+`, j.paused_at IS NOT NULL,
			l.id, l.worker_id, w.runtime_name, h.name, l.card_index, l.memory_mb, l.created_at, l.heartbeat_at, l.progress,
			l.message, l.stop_reason
		FROM step_jobs s JOIN jobs j ON j.id = s.job_id
		LEFT JOIN projects p ON p.id = s.project_id
		LEFT JOIN leases l ON l.job_id = s.job_id AND l.state = 'active'
		LEFT JOIN workers w ON w.id = l.worker_id
		LEFT JOIN compute_hosts h ON h.id = l.host_id
		WHERE s.state <> 'ended' AND ($1 = '' OR s.project_id = $1)
		ORDER BY (s.state = 'leased') DESC, s.job_kind = 'interactive' DESC, `+projectPriority("$2")+` DESC, j.priority DESC,
			s.enqueued_at, s.job_id`,
		projectID, defaultProjectPriority())
	if err != nil {
		return nil, fmt.Errorf("read the queue: %w", err)
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (Entry, error) {
		var (
			e                                     Entry
			raw                                   []byte
			state                                 string
			paused                                bool
			leaseID, workerID, runtime, host, msg *string
			stop                                  *string
			card, memMB                           *int
			started, beat                         *time.Time
			progress                              *float64
		)
		if err := row.Scan(&e.JobID, &raw, &state, &e.JobKind, &e.EnqueuedAt, &e.Priority, &e.ProjectPriority, &paused, &leaseID, &workerID,
			&runtime, &host, &card, &memMB, &started, &beat, &progress, &msg, &stop); err != nil {
			return e, err
		}
		var spec steps.Spec
		if err := json.Unmarshal(raw, &spec); err != nil {
			return e, err
		}
		e.ProjectID, e.PipelineRunID, e.StepID, e.RunID = spec.ProjectID, spec.PipelineRunID, spec.StepID, spec.RunID
		e.Kind, e.KindVersion, e.GPU, e.MemoryGB = spec.Kind, spec.KindVersion, spec.Resources.GPU, spec.Resources.MemoryGB
		e.EstimateSeconds, e.Attempt, e.ResumeFrom = spec.EstimateSeconds, spec.Attempt, spec.Overrides.ResumeFrom
		switch {
		case leaseID != nil:
			e.State = "running"
			e.Lease = &QueueLease{ID: *leaseID, WorkerID: deref(workerID), Runtime: deref(runtime), Host: deref(host), Card: -1,
				MemoryCapMB: derefInt(memMB), StartedAt: derefTime(started), HeartbeatAt: derefTime(beat), Progress: progress,
				Message: deref(msg), StopReason: deref(stop)}
			if card != nil {
				e.Lease.Card = *card
			}
			if stop != nil {
				e.State = "stopping"
			}
		case paused:
			e.State = "paused"
		default:
			e.State = "waiting"
		}
		return e, nil
	})
}

func deref(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func derefInt(p *int) int {
	if p == nil {
		return 0
	}
	return *p
}

func derefTime(p *time.Time) time.Time {
	if p == nil {
		return time.Time{}
	}
	return *p
}

// PutArtifact stores an uploaded blob under hash (workerArtifacts.set): the content must hash to it.
func (s *Service) PutArtifact(hash string, body io.Reader) error {
	if s.cas == nil {
		return problems.NotImplemented.New("this control plane has no content store")
	}
	if !steps.ValidHash(hash) {
		return problems.Validation([]problems.FieldError{{Path: "/hash", Message: "an artifact hash is b3:<64 hex>"}})
	}
	if ok, _, err := s.cas.Has(hash); err == nil && ok {
		_, _ = io.Copy(io.Discard, body)
		return nil
	}
	if _, _, err := s.cas.Put(body, hash); err != nil {
		if errors.Is(err, cas.ErrHashMismatch) {
			return problems.ArtifactHashMismatch.New("the uploaded content does not hash to %s", hash)
		}
		return fmt.Errorf("store artifact: %w", err)
	}
	return nil
}

// ResumeEstimate is the GPU time resuming step job id may spend: its recorded estimate when it needs a card (a
// resume continues from its training state, so this is an upper bound); unknown when it needs a card and has none.
// A job that is not a step job spends nothing on a card. pipelineRunID is the pipeline run the step job belongs to
// ("" outside one): jobs.resume continues it and may inherit its approval (approvals.Inherit).
func ResumeEstimate(ctx context.Context, q storage.Querier, id string) (gpuHours float64, unknown bool, pipelineRunID string, err error) {
	var (
		raw []byte
		est *float64
	)
	err = q.QueryRow(ctx, `SELECT spec, estimate_seconds FROM step_jobs WHERE job_id = $1`, id).Scan(&raw, &est)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, false, "", nil
	}
	if err != nil {
		return 0, false, "", fmt.Errorf("read step job %s: %w", id, err)
	}
	var spec steps.Spec
	if err := json.Unmarshal(raw, &spec); err != nil {
		return 0, false, "", fmt.Errorf("step job %s spec: %w", id, err)
	}
	switch {
	case !spec.Resources.GPU:
		return 0, false, spec.PipelineRunID, nil
	case est == nil:
		return 0, true, spec.PipelineRunID, nil
	}
	return *est / 3600 * float64(max(1, spec.Resources.GPUs)), false, spec.PipelineRunID, nil
}
