package workers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/usunrise88/cadence/control-plane/internal/compute"
	"github.com/usunrise88/cadence/control-plane/internal/events"
	"github.com/usunrise88/cadence/control-plane/internal/jobs"
	"github.com/usunrise88/cadence/control-plane/internal/problems"
	"github.com/usunrise88/cadence/control-plane/internal/queue"
	"github.com/usunrise88/cadence/control-plane/internal/steps"
)

// Stop reasons a heartbeat can answer (the contract's WorkerReportAck.reason).
const (
	StopCancelled    = "cancelled"
	StopPaused       = "paused"
	StopWindowClosed = "window-closed"
)

// Lease states.
const (
	LeaseActive   = "active"
	LeaseReleased = "released"
	LeaseReaped   = "reaped"
)

type leaseRow struct {
	ID, JobID, WorkerID, HostID, Host string
	CardIndex                         *int
	JobKind                           string
	State                             string
	StopReason                        *string
	ProjectID                         string
}

// lockLease reads lease id for update and checks that c may speak for its host.
func lockLease(ctx context.Context, tx pgx.Tx, c Caller, id string) (leaseRow, error) {
	var l leaseRow
	err := tx.QueryRow(ctx, `SELECT l.id, l.job_id, l.worker_id, l.host_id, h.name, l.card_index, l.job_kind, l.state, l.stop_reason,
			coalesce(j.project_id, '')
		FROM leases l JOIN compute_hosts h ON h.id = l.host_id JOIN jobs j ON j.id = l.job_id WHERE l.id = $1 FOR UPDATE OF l`, id).
		Scan(&l.ID, &l.JobID, &l.WorkerID, &l.HostID, &l.Host, &l.CardIndex, &l.JobKind, &l.State, &l.StopReason, &l.ProjectID)
	if errors.Is(err, pgx.ErrNoRows) {
		return leaseRow{}, problems.NotFound.New("no lease %q", id)
	}
	if err != nil {
		return leaseRow{}, fmt.Errorf("read lease: %w", err)
	}
	if !c.allows(l.Host) {
		return leaseRow{}, problems.Forbidden.New("lease %s runs on host %q; this token belongs to %q", id, l.Host, c.Host)
	}
	return l, nil
}

func ended(l leaseRow) error {
	return problems.LeaseEnded.New("lease %s was %s; stop the step and claim again", l.ID, l.State)
}

// Progress is a heartbeat's progress.
type Progress struct {
	Fraction *float64 `json:"fraction,omitempty"`
	Message  string   `json:"message,omitempty"`
}

// Report is a heartbeat (the contract's WorkerReport).
type Report struct {
	Progress *Progress       `json:"progress,omitempty"`
	Cards    []CardTelemetry `json:"cards,omitempty"`
}

// Ack answers a heartbeat (the contract's WorkerReportAck).
type Ack struct {
	Stop   bool   `json:"stop"`
	Reason string `json:"reason,omitempty"`
}

// Report records a heartbeat: progress (job.progress), card telemetry (gpu, compute health) and the lease's life.
// It answers stop when the job was cancelled or paused or, for training, when its card's availability window
// closed; the worker then stops the step (saving its training state when it can) and releases the lease.
func (s *Service) Report(ctx context.Context, c Caller, leaseID string, in Report) (Ack, error) {
	var ack Ack
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		l, err := lockLease(ctx, tx, c, leaseID)
		if err != nil {
			return err
		}
		if l.State != LeaseActive {
			return ended(l)
		}
		now := s.now()
		var fraction *float64
		msg := ""
		if in.Progress != nil {
			fraction, msg = in.Progress.Fraction, in.Progress.Message
		}
		if _, err := tx.Exec(ctx, `UPDATE leases SET heartbeat_at = $2, progress = coalesce($3, progress),
			message = coalesce(NULLIF($4, ''), message) WHERE id = $1`, l.ID, now, fraction, msg); err != nil {
			return fmt.Errorf("record heartbeat: %w", err)
		}
		if _, err := tx.Exec(ctx, "UPDATE workers SET last_seen_at = $2 WHERE id = $1", l.WorkerID, now); err != nil {
			return fmt.Errorf("touch worker: %w", err)
		}
		var drafts []events.Draft
		if fraction != nil || msg != "" {
			var cur float64
			if err := tx.QueryRow(ctx, "SELECT progress FROM jobs WHERE id = $1", l.JobID).Scan(&cur); err != nil {
				return fmt.Errorf("read job progress: %w", err)
			}
			if fraction != nil {
				cur = *fraction
			}
			if drafts, err = jobs.Progress(ctx, tx, l.JobID, cur, msg); err != nil {
				return err
			}
		}
		w := Worker{ID: l.WorkerID, HostID: l.HostID, Host: l.Host}
		if len(in.Cards) > 0 {
			d, err := s.recordTelemetry(ctx, tx, w, in.Cards, now)
			if err != nil {
				return err
			}
			drafts = append(drafts, d...)
		}
		reason, err := s.stopReason(ctx, tx, l, now)
		if err != nil {
			return err
		}
		if reason != "" {
			ack = Ack{Stop: true, Reason: reason}
			if l.StopReason == nil || *l.StopReason != reason {
				if _, err := tx.Exec(ctx, "UPDATE leases SET stop_reason = $2 WHERE id = $1", l.ID, reason); err != nil {
					return fmt.Errorf("record stop: %w", err)
				}
				drafts = append(drafts, queueEvent(l.JobID, l.ProjectID, "stopping", map[string]any{"leaseId": l.ID, "reason": reason}))
			}
		}
		return events.Append(ctx, tx, jobs.System, nil, drafts)
	})
	return ack, err
}

// stopReason decides whether the lease's step must stop: a cancel wins over a pause, a pause over a window close;
// once told, a worker keeps hearing the same reason (a later cancel replaces it).
func (s *Service) stopReason(ctx context.Context, tx pgx.Tx, l leaseRow, now time.Time) (string, error) {
	var cancelled, paused *time.Time
	var stepState string
	if err := tx.QueryRow(ctx, `SELECT j.cancel_requested_at, j.paused_at, coalesce(s.state, '') FROM jobs j
		LEFT JOIN step_jobs s ON s.job_id = j.id WHERE j.id = $1`, l.JobID).Scan(&cancelled, &paused, &stepState); err != nil {
		return "", fmt.Errorf("read job: %w", err)
	}
	switch {
	case cancelled != nil || stepState == "ended":
		return StopCancelled, nil
	case l.StopReason != nil:
		return *l.StopReason, nil
	case paused != nil:
		return StopPaused, nil
	case l.CardIndex != nil:
		h, err := compute.Get(ctx, tx, l.HostID)
		if err != nil {
			return "", err
		}
		at := slices.IndexFunc(h.Cards, func(c compute.Card) bool { return c.Index == *l.CardIndex })
		if at < 0 || len(h.Cards[at].Windows) == 0 {
			return "", nil
		}
		tz, err := instanceZone(ctx, tx)
		if err != nil {
			return "", err
		}
		if queue.WindowClosed(h.Cards[at].Windows.InZone(tz), l.JobKind, now) {
			return StopWindowClosed, nil
		}
	}
	return "", nil
}

// Release completes a lease with the step's outcome. Outputs must be in the content store. A step stopped for a
// pause or a window close that released as cancelled goes back to the queue in its place, resuming from the
// training-state output it saved; everything else ends the step job and wakes its Await. Releasing a released lease
// again is a no-op (a retried request).
func (s *Service) Release(ctx context.Context, c Caller, leaseID string, o steps.Outcome) error {
	if err := s.checkOutcome(o); err != nil {
		return err
	}
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		l, err := lockLease(ctx, tx, c, leaseID)
		if err != nil {
			return err
		}
		switch l.State {
		case LeaseReleased:
			return nil
		case LeaseReaped:
			return ended(l)
		}
		now := s.now()
		if _, err := tx.Exec(ctx, `UPDATE leases SET state = 'released', outcome = $2, ended_at = $3 WHERE id = $1`,
			l.ID, o, now); err != nil {
			return fmt.Errorf("release lease: %w", err)
		}
		var (
			state     string
			raw       []byte
			cancelled *time.Time
		)
		if err := tx.QueryRow(ctx, `SELECT s.state, s.spec, j.cancel_requested_at FROM step_jobs s JOIN jobs j ON j.id = s.job_id
			WHERE s.job_id = $1 FOR UPDATE OF s`, l.JobID).Scan(&state, &raw, &cancelled); err != nil {
			return fmt.Errorf("read step job: %w", err)
		}
		var drafts []events.Draft
		switch {
		case state != "leased":
			// The job already ended (cancelled while running): the release only frees the card.
			drafts = append(drafts, queueEvent(l.JobID, l.ProjectID, "released", map[string]any{"leaseId": l.ID}))
		case l.StopReason != nil && (*l.StopReason == StopPaused || *l.StopReason == StopWindowClosed) &&
			o.State != steps.StateDone && cancelled == nil:
			d, err := s.requeue(ctx, tx, l, raw, o, now)
			if err != nil {
				return err
			}
			drafts = d
		default:
			d, err := s.endStep(ctx, tx, l.JobID, l.ProjectID, o, now)
			if err != nil {
				return err
			}
			drafts = d
		}
		return events.Append(ctx, tx, jobs.System, nil, drafts)
	})
	if err == nil {
		s.wake.broadcast()
	}
	return err
}

func (s *Service) checkOutcome(o steps.Outcome) error {
	switch o.State {
	case steps.StateDone, steps.StateFailed, steps.StateCancelled:
	default:
		return problems.Validation([]problems.FieldError{{Path: "/state", Message: "done, failed or cancelled"}})
	}
	for name, ref := range o.Outputs {
		if !steps.ValidHash(ref.Hash) {
			return problems.Validation([]problems.FieldError{{Path: "/outputs/" + name + "/hash", Message: "an artifact hash is b3:<64 hex>"}})
		}
		if s.cas == nil {
			continue
		}
		ok, _, err := s.cas.Has(ref.Hash)
		if err != nil {
			return fmt.Errorf("look up output %s: %w", name, err)
		}
		if !ok {
			return problems.ArtifactMissing.New("output %q (%s) is not in the content store; write it (or upload it with workerArtifacts.set) before releasing", name, ref.Hash)
		}
	}
	return nil
}

// requeue puts a stopped step back in the queue, resuming from its training-state output when it saved one.
func (s *Service) requeue(ctx context.Context, tx pgx.Tx, l leaseRow, raw []byte, o steps.Outcome, now time.Time) ([]events.Draft, error) {
	var spec steps.Spec
	if err := json.Unmarshal(raw, &spec); err != nil {
		return nil, fmt.Errorf("read step spec: %w", err)
	}
	for _, out := range o.Outputs {
		if out.Type == TrainingState {
			spec.Overrides.ResumeFrom = out.Hash
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE step_jobs SET state = 'waiting', spec = $2, updated_at = $3 WHERE job_id = $1`,
		l.JobID, spec, now); err != nil {
		return nil, fmt.Errorf("requeue step job: %w", err)
	}
	msg := "paused; waits in the queue"
	if *l.StopReason == StopWindowClosed {
		msg = "the availability window closed; waits for the next one"
	}
	drafts, err := jobs.Progress(ctx, tx, l.JobID, 0, msg)
	if err != nil {
		return nil, err
	}
	return append(drafts, queueEvent(l.JobID, l.ProjectID, "requeued", map[string]any{
		"leaseId": l.ID, "reason": *l.StopReason, "resumeFrom": spec.Overrides.ResumeFrom})), nil
}

// TrainingState is the artifact type a stopped training step saves to resume from.
const TrainingState = "training-state"

// endStep ends a step job with o; its Await returns o.
func (s *Service) endStep(ctx context.Context, tx pgx.Tx, jobID, projectID string, o steps.Outcome, now time.Time) ([]events.Draft, error) {
	tag, err := tx.Exec(ctx, `UPDATE step_jobs SET state = 'ended', outcome = $2, updated_at = $3 WHERE job_id = $1 AND state <> 'ended'`,
		jobID, o, now)
	if err != nil {
		return nil, fmt.Errorf("end step job: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return nil, nil
	}
	return []events.Draft{queueEvent(jobID, projectID, "ended", map[string]any{"state": o.State})}, nil
}

// endWaiting ends a waiting step job that cannot start (a missing secret).
func (s *Service) endWaiting(ctx context.Context, tx pgx.Tx, jobID string, o steps.Outcome) ([]events.Draft, error) {
	var projectID string
	if err := tx.QueryRow(ctx, "SELECT coalesce(project_id, '') FROM step_jobs WHERE job_id = $1", jobID).Scan(&projectID); err != nil {
		return nil, fmt.Errorf("read step job: %w", err)
	}
	return s.endStep(ctx, tx, jobID, projectID, o, s.now())
}

// Reap ends the leases whose worker missed three heartbeats: each step job ends as failed with error type lost
// (the pipeline engine decides about a retry) and its card is free again. It also marks hosts whose workers all went
// quiet as unreachable. It reports how many leases it reaped.
//
// Beats missed while the control plane itself was down are not the worker's: for the first lostAfter after this
// service started nothing is reaped (a live worker reports again within one beat, and its release of a step that
// finished meanwhile still lands), and hosts are judged quiet only once their workers had a full claim wait to
// come back.
func (s *Service) Reap(ctx context.Context) (int, error) {
	n := 0
	now := s.now()
	if now.Before(s.started.Add(s.lostAfter())) {
		return 0, nil
	}
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		drafts, err := s.reapWhere(ctx, tx, "heartbeat_at < $1", now.Add(-s.lostAfter()))
		if err != nil {
			return err
		}
		n = len(drafts)
		if now.Before(s.started.Add(MaxClaimWait + s.lostAfter())) {
			return events.Append(ctx, tx, jobs.System, nil, drafts)
		}
		rows, err := tx.Query(ctx, `SELECT w.host_id FROM workers w JOIN compute_hosts h ON h.id = w.host_id
			WHERE h.health->>'state' = 'healthy' GROUP BY w.host_id HAVING max(w.last_seen_at) < $1`,
			now.Add(-MaxClaimWait-s.lostAfter()))
		if err != nil {
			return fmt.Errorf("find quiet hosts: %w", err)
		}
		quiet, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			return fmt.Errorf("find quiet hosts: %w", err)
		}
		for _, h := range quiet {
			d, err := compute.SetHealth(ctx, tx, h, compute.Health{State: "unreachable", CheckedAt: &now,
				Detail: "no worker on this host has reported for a minute"})
			if err != nil {
				return err
			}
			drafts = append(drafts, d...)
		}
		return events.Append(ctx, tx, jobs.System, nil, drafts)
	})
	if n > 0 {
		s.log.WarnContext(ctx, "leases reaped after missed heartbeats", "count", n)
		s.wake.broadcast()
	}
	return n, err
}

// reapWhere reaps the active leases matching cond ($1 = arg); it returns one queue event per lease reaped (plus the
// step job's end).
func (s *Service) reapWhere(ctx context.Context, tx pgx.Tx, cond string, arg any) ([]events.Draft, error) {
	now := s.now()
	lost := steps.Outcome{State: steps.StateFailed, Error: &steps.StepError{Type: steps.ErrLost, Retryable: true,
		Message: fmt.Sprintf("the worker missed %d heartbeats; the lease was reaped", MissedBeats)}}
	rows, err := tx.Query(ctx, `UPDATE leases l SET state = 'reaped', ended_at = $2, outcome = $3
		FROM jobs j WHERE j.id = l.job_id AND l.state = 'active' AND l.`+cond+`
		RETURNING l.id, l.job_id, coalesce(j.project_id, '')`, arg, now, lost)
	if err != nil {
		return nil, fmt.Errorf("reap leases: %w", err)
	}
	type reaped struct{ lease, job, project string }
	list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (reaped, error) {
		var r reaped
		err := row.Scan(&r.lease, &r.job, &r.project)
		return r, err
	})
	if err != nil {
		return nil, fmt.Errorf("reap leases: %w", err)
	}
	var drafts []events.Draft
	for _, r := range list {
		if _, err := tx.Exec(ctx, `UPDATE step_jobs SET state = 'ended', outcome = $2, updated_at = $3
			WHERE job_id = $1 AND state = 'leased'`, r.job, lost, now); err != nil {
			return nil, fmt.Errorf("end reaped step job: %w", err)
		}
		drafts = append(drafts, queueEvent(r.job, r.project, "reaped", map[string]any{"leaseId": r.lease}))
	}
	return drafts, nil
}
