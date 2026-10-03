package transcriptions

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/usunrise88/cadence/control-plane/internal/steps"
)

// Sweep ends sessions nothing serves any more (run it periodically, every 30 s): a ticket never used before it
// expired, a session whose job ended without the job handler seeing it (cancelled before it started), and a session
// whose socket is gone from this process (the control plane restarted, or its relay died) — their jobs are cancelled
// and their records closed. It reports how many sessions it ended.
func (s *Service) Sweep(ctx context.Context) (int, error) {
	now := s.now()
	rows, err := s.Pool.Query(ctx, `SELECT t.id, coalesce(t.job_id, ''), t.ticket_hash IS NOT NULL, t.ticket_expires_at,
			coalesce(j.state, ''), coalesce(sj.outcome, 'null'::jsonb)
		FROM transcriptions t LEFT JOIN jobs j ON j.id = t.job_id LEFT JOIN step_jobs sj ON sj.job_id = t.job_id
		WHERE t.state <> 'ended'`)
	if err != nil {
		return 0, fmt.Errorf("read open sessions: %w", err)
	}
	type open struct {
		id, jobID, jobState string
		unused              bool
		expires             *time.Time
		outcome             []byte
	}
	list, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (open, error) {
		var o open
		err := r.Scan(&o.id, &o.jobID, &o.unused, &o.expires, &o.jobState, &o.outcome)
		return o, err
	})
	if err != nil {
		return 0, fmt.Errorf("read open sessions: %w", err)
	}
	n := 0
	for _, o := range list {
		if s.hub.has(o.id) {
			continue // a socket is on it
		}
		reason := ""
		switch {
		case o.jobState == "done" || o.jobState == "failed" || o.jobState == "cancelled":
			reason = "the job ended (" + o.jobState + ")"
		case o.unused && o.expires != nil && now.After(o.expires.Add(5*time.Second)):
			reason = "the ticket expired unused"
		case !o.unused:
			reason = "the live channel closed"
		default:
			continue // the ticket is still valid: the page may still open the socket
		}
		s.endRecord(ctx, o.id, reason)
		s.cancelJob(ctx, o.jobID)
		if o.jobID != "" { // a job cancelled before its handler ran ends at once: no handler will close the record
			if err := s.Pool.QueryRow(ctx, "SELECT state FROM jobs WHERE id = $1", o.jobID).Scan(&o.jobState); err != nil {
				return n, fmt.Errorf("read job %s: %w", o.jobID, err)
			}
		}
		out := steps.Outcome{State: steps.StateCancelled}
		if len(o.outcome) > 0 && string(o.outcome) != "null" {
			_ = json.Unmarshal(o.outcome, &out)
		}
		if o.jobState == "done" || o.jobState == "failed" || o.jobState == "cancelled" || o.jobID == "" {
			if err := s.finish(ctx, o.jobID, out); err != nil {
				return n, err
			}
			if o.jobID == "" {
				if _, err := s.Pool.Exec(ctx, `UPDATE transcriptions SET state = 'ended', ended_at = now(), ticket_hash = NULL,
					live_token_hash = NULL WHERE id = $1 AND state <> 'ended'`, o.id); err != nil {
					return n, fmt.Errorf("end session %s: %w", o.id, err)
				}
			}
		}
		n++
	}
	if n > 0 {
		s.log().InfoContext(ctx, "transcription sessions swept", "count", n)
	}
	return n, nil
}
