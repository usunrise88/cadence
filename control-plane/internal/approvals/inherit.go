package approvals

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/usunrise88/cadence/control-plane/internal/policy"
	"github.com/usunrise88/cadence/control-plane/internal/storage"
)

// Retry approvals (owner decision of 2026-10-04 on the phase-4 gate, docs/spec/05-agents.md "Guardrails"): a spending
// command that continues a pipeline run a person already approved — pipelineRuns.retry, jobs.resume, runs.resume —
// inherits that approval on the same UTC day while the spend stays within what the person approved. The approval a
// run was started under is recorded on the run (pipeline_runs.approval_id, by the replay that started it); a retry
// that needed an approval of its own records that one instead (Continue).

// inheritEpsilon absorbs rounding of metered lease time against an approved estimate.
const inheritEpsilon = 1e-6

// Inherited is an approval a continuing command inherits, with why, for the decision and the audit row.
type Inherited struct {
	ApprovalID string
	Reason     string
}

// Inherit answers whether a command that continues pipeline run runID, and that the policy would send to a person
// under rule (a spend rule), may go ahead under the approval the run carries:
//
//   - the approval was approved under the same rule on the same UTC day as now;
//   - when its estimate was known: the GPU-hours the runs it covers used on cards since it was decided, plus the
//     committed, not yet metered work of its other runs, plus est stay within the approved GPU-hours (est must be
//     known — an unknown retry cannot be weighed against a known approval);
//   - when its estimate was unknown (or not recorded): every continuation of the run inherits; a retry runs the run's
//     own steps and parameters, and a smaller batch scale changes nothing that was approved.
//
// Any actor inherits, people and agents alike: the approval is of the run's spend, not of who sends the retry.
func Inherit(ctx context.Context, q storage.Querier, runID, rule string, est *policy.Estimate, now time.Time) (Inherited, bool, error) {
	if runID == "" {
		return Inherited{}, false, nil
	}
	var (
		id, arule, state string
		decided          *time.Time
		raw              []byte
	)
	err := q.QueryRow(ctx, `SELECT a.id, a.rule, a.state, a.decided_at, a.estimate FROM pipeline_runs pr
		JOIN approvals a ON a.id = pr.approval_id WHERE pr.id = $1`, runID).Scan(&id, &arule, &state, &decided, &raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return Inherited{}, false, nil
	}
	if err != nil {
		return Inherited{}, false, fmt.Errorf("read the approval of pipeline run %s: %w", runID, err)
	}
	if state != StateApproved || arule != rule || decided == nil || !sameUTCDay(*decided, now) {
		return Inherited{}, false, nil
	}
	approved, known, err := approvedHours(raw)
	if err != nil {
		return Inherited{}, false, err
	}
	if !known {
		return Inherited{ApprovalID: id, Reason: fmt.Sprintf(
			"continues pipeline run %s, approved today by %s with an unknown GPU estimate", runID, id)}, true, nil
	}
	if est == nil || est.Unknown {
		return Inherited{}, false, nil
	}
	spent, err := spentUnder(ctx, q, id, runID, *decided)
	if err != nil {
		return Inherited{}, false, err
	}
	if spent+est.GPUHours > approved+inheritEpsilon {
		return Inherited{}, false, nil
	}
	return Inherited{ApprovalID: id, Reason: fmt.Sprintf(
		"continues pipeline run %s, approved today by %s for %.2f GPU-hours: %.2f used so far plus %.2f estimated stay within it",
		runID, id, approved, spent, est.GPUHours)}, true, nil
}

// approvedHours reads an approval's estimate column; known is false for an unknown or missing estimate.
func approvedHours(raw []byte) (float64, bool, error) {
	if len(raw) == 0 {
		return 0, false, nil
	}
	var e storedEstimate
	if err := json.Unmarshal(raw, &e); err != nil {
		return 0, false, fmt.Errorf("decode estimate: %w", err)
	}
	return e.GPUHours, !e.Unknown, nil
}

// spentUnder is what the pipeline runs that carry approval id used on GPU cards since it was decided, plus the
// committed, not yet metered estimates of those runs' unfinished steps other than run except's (the retry's own
// estimate covers those).
func spentUnder(ctx context.Context, q storage.Querier, id, except string, since time.Time) (float64, error) {
	var used, committed float64
	if err := q.QueryRow(ctx, `SELECT coalesce(sum(extract(epoch FROM
			coalesce(l.ended_at, now()) - greatest(l.created_at, $2::timestamptz))), 0) / 3600
		FROM leases l JOIN step_jobs s ON s.job_id = l.job_id JOIN pipeline_runs pr ON pr.id = s.spec->>'pipelineRunId'
		WHERE l.card_index IS NOT NULL AND pr.approval_id = $1 AND coalesce(l.ended_at, now()) > $2`,
		id, since).Scan(&used); err != nil {
		return 0, fmt.Errorf("meter the GPU-hours used under %s: %w", id, err)
	}
	if err := q.QueryRow(ctx, `SELECT coalesce(sum(greatest(
			ps.estimate_seconds / 3600 * greatest(1, coalesce((ps.resources->>'gpus')::int, 1))
			- coalesce((SELECT sum(extract(epoch FROM now() - l.created_at)) / 3600 FROM leases l
				WHERE l.job_id = ps.job_id AND l.state = 'active' AND l.card_index IS NOT NULL), 0), 0)), 0)
		FROM pipeline_steps ps JOIN pipeline_runs pr ON pr.id = ps.pipeline_run_id
		WHERE ps.state IN ('waiting', 'queued', 'running') AND pr.state = 'running' AND pr.approval_id = $1 AND pr.id <> $2
			AND coalesce((ps.resources->>'gpu')::boolean, false) AND ps.estimate_seconds IS NOT NULL`,
		id, except).Scan(&committed); err != nil {
		return 0, fmt.Errorf("committed GPU-hours under %s: %w", id, err)
	}
	return used + committed, nil
}

// Continue records that pipeline run runID now continues under approval id (a retry a person approved): a later
// retry inherits that approval instead of the one the run started under.
func Continue(ctx context.Context, tx pgx.Tx, runID, id string) error {
	if runID == "" || id == "" {
		return nil
	}
	if _, err := tx.Exec(ctx, "UPDATE pipeline_runs SET approval_id = $2 WHERE id = $1", runID, id); err != nil {
		return fmt.Errorf("record the approval of pipeline run %s: %w", runID, err)
	}
	return nil
}

func sameUTCDay(a, b time.Time) bool {
	ay, am, ad := a.UTC().Date()
	by, bm, bd := b.UTC().Date()
	return ay == by && am == bm && ad == bd
}
