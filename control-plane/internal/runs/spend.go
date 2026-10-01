package runs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/usunrise88/cadence/control-plane/internal/defaults"
	"github.com/usunrise88/cadence/control-plane/internal/policies"
	"github.com/usunrise88/cadence/control-plane/internal/projects"
	"github.com/usunrise88/cadence/control-plane/internal/storage"
)

// GPU spend is metered from leases on GPU cards (internal/workers): a lease on a card counts from its start until
// it ended (or now), so a paused step or a closed window stops counting. A lease without a card (a CPU step) is
// free. Spend counts against the project's daily budget and the agent session's budget.

// leaseHours sums the hours of the card leases selected by cond ($1 is its argument), clipped to [since, until).
func leaseHours(ctx context.Context, q storage.Querier, join, cond string, arg any, since, until time.Time) (float64, error) {
	var h float64
	err := q.QueryRow(ctx, `SELECT coalesce(sum(extract(epoch FROM
			least(coalesce(l.ended_at, now()), $3::timestamptz) - greatest(l.created_at, $2::timestamptz))), 0) / 3600
		FROM leases l `+join+`
		WHERE l.card_index IS NOT NULL AND `+cond+` AND l.created_at < $3 AND coalesce(l.ended_at, now()) > $2`,
		arg, since, until).Scan(&h)
	if err != nil {
		return 0, fmt.Errorf("meter GPU-hours: %w", err)
	}
	return h, nil
}

// ProjectGPUHours is what the project's leases on GPU cards used in [since, until).
func ProjectGPUHours(ctx context.Context, q storage.Querier, projectID string, since, until time.Time) (float64, error) {
	return leaseHours(ctx, q, "JOIN step_jobs s ON s.job_id = l.job_id", "s.project_id = $1", projectID, since, until)
}

// SessionGPUHours is what the leases of step jobs an agent session started (their actor's session) used so far.
func SessionGPUHours(ctx context.Context, q storage.Querier, sessionID string) (float64, error) {
	return leaseHours(ctx, q, "JOIN jobs j ON j.id = l.job_id", "j.actor->>'sessionId' = $1", sessionID,
		time.Unix(0, 0), time.Now().Add(time.Hour))
}

// RunGPUHours is what a run's step jobs used on GPU cards so far.
func RunGPUHours(ctx context.Context, q storage.Querier, runID string) (float64, error) {
	return leaseHours(ctx, q, "JOIN step_jobs s ON s.job_id = l.job_id", "s.spec->>'runId' = $1", runID,
		time.Unix(0, 0), time.Now().Add(time.Hour))
}

// committedHours is the GPU time already committed but not metered yet: the estimates of the steps that need a card
// and have not ended (waiting for an input, queued, or running) in pipeline runs selected by cond ($1 is its
// argument; ps is pipeline_steps, pr pipeline_runs), less what a running step's live lease used so far. A step
// without an estimate adds nothing (the policy gates the command that starts it instead).
func committedHours(ctx context.Context, q storage.Querier, cond string, arg any) (float64, error) {
	var h float64
	err := q.QueryRow(ctx, `SELECT coalesce(sum(greatest(
			ps.estimate_seconds / 3600 * greatest(1, coalesce((ps.resources->>'gpus')::int, 1))
			- coalesce((SELECT sum(extract(epoch FROM now() - l.created_at)) / 3600 FROM leases l
				WHERE l.job_id = ps.job_id AND l.state = 'active' AND l.card_index IS NOT NULL), 0), 0)), 0)
		FROM pipeline_steps ps JOIN pipeline_runs pr ON pr.id = ps.pipeline_run_id
		WHERE ps.state IN ('waiting', 'queued', 'running') AND pr.state = 'running'
			AND coalesce((ps.resources->>'gpu')::boolean, false) AND ps.estimate_seconds IS NOT NULL AND `+cond, arg).Scan(&h)
	if err != nil {
		return 0, fmt.Errorf("committed GPU-hours: %w", err)
	}
	return h, nil
}

// ProjectCommittedGPUHours is the project's committed, not yet metered GPU time (committedHours).
func ProjectCommittedGPUHours(ctx context.Context, q storage.Querier, projectID string) (float64, error) {
	return committedHours(ctx, q, "ps.project_id = $1", projectID)
}

// SessionCommittedGPUHours is the committed GPU time of the pipeline runs an agent session started.
func SessionCommittedGPUHours(ctx context.Context, q storage.Querier, sessionID string) (float64, error) {
	return committedHours(ctx, q, "pr.actor->>'sessionId' = $1", sessionID)
}

// DayStart is the start of today in the instance timezone (policies).
func DayStart(ctx context.Context, q storage.Querier, d *defaults.Defaults, now time.Time) (time.Time, error) {
	pol, err := policies.Get(ctx, q, d)
	if err != nil {
		return time.Time{}, err
	}
	local := now.In(pol.Location())
	return time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, local.Location()), nil
}

// ProjectBudget is a project's daily GPU budget with today's use and the work already committed (queued and
// running steps' remaining estimates), so three runs started back to back cannot each see the whole remainder.
type ProjectBudget struct {
	PerDay    float64
	Used      float64
	Committed float64
}

// Remaining is the budget minus today's use and the committed work (negative when overspent).
func (b ProjectBudget) Remaining() float64 { return b.PerDay - b.Used - b.Committed }

// ProjectBudgetOf reads the project's daily budget (projects.edit, from budgets.gpu_hours_per_project_per_day at
// creation) and what it used today.
func ProjectBudgetOf(ctx context.Context, q storage.Querier, d *defaults.Defaults, projectID string, now time.Time) (ProjectBudget, error) {
	p, err := projects.GetByID(ctx, q, projectID)
	if err != nil {
		return ProjectBudget{}, err
	}
	since, err := DayStart(ctx, q, d, now)
	if err != nil {
		return ProjectBudget{}, err
	}
	used, err := ProjectGPUHours(ctx, q, projectID, since, now)
	if err != nil {
		return ProjectBudget{}, err
	}
	committed, err := ProjectCommittedGPUHours(ctx, q, projectID)
	if err != nil {
		return ProjectBudget{}, err
	}
	return ProjectBudget{PerDay: p.Budgets.GPUHoursPerDay, Used: round(used, 3), Committed: round(committed, 3)}, nil
}

// SessionBudget is an agent session's GPU budget with its use so far.
type SessionBudget struct {
	Hours     float64
	Used      float64
	Committed float64 // queued and running steps' remaining estimates
}

// SessionBudgetOf reads the session's GPU budget (its budget.gpuHours, else budgets.agent_gpu_hours_per_session) and
// its use. A session id without a row (tests, a token without a session) gets the default budget.
func SessionBudgetOf(ctx context.Context, q storage.Querier, d *defaults.Defaults, sessionID string) (SessionBudget, error) {
	b := SessionBudget{Hours: d.Budgets.AgentGPUHoursPerSession.Value}
	var raw []byte
	err := q.QueryRow(ctx, "SELECT budget FROM agent_sessions WHERE id = $1", sessionID).Scan(&raw)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
	case err != nil:
		return SessionBudget{}, fmt.Errorf("read session budget: %w", err)
	default:
		var sb struct {
			GPUHours float64 `json:"gpuHours"`
		}
		if json.Unmarshal(raw, &sb) == nil && sb.GPUHours > 0 {
			b.Hours = sb.GPUHours
		}
	}
	used, err := SessionGPUHours(ctx, q, sessionID)
	if err != nil {
		return SessionBudget{}, err
	}
	b.Used = round(used, 3)
	committed, err := SessionCommittedGPUHours(ctx, q, sessionID)
	if err != nil {
		return SessionBudget{}, err
	}
	b.Committed = round(committed, 3)
	return b, nil
}

// Meter is the policy engine's budget source (policy.Budget and policy.SessionBudget) and the daily digest's spend
// (notify.SpendFunc).
type Meter struct {
	Pool     *pgxpool.Pool
	Defaults func() *defaults.Defaults // defaults.Get when nil
	Now      func() time.Time          // time.Now when nil
}

func (m Meter) defaults() *defaults.Defaults {
	if m.Defaults != nil {
		return m.Defaults()
	}
	return defaults.Get()
}

func (m Meter) now() time.Time {
	if m.Now != nil {
		return m.Now()
	}
	return time.Now()
}

// RemainingGPUHours implements policy.Budget: the project's daily budget minus today's use. A command outside a
// project gets the instance default.
func (m Meter) RemainingGPUHours(ctx context.Context, projectID string) (float64, error) {
	d := m.defaults()
	if projectID == "" || m.Pool == nil {
		return d.Budgets.GPUHoursPerProjectPerDay.Value, nil
	}
	b, err := ProjectBudgetOf(ctx, m.Pool, d, projectID, m.now())
	if err != nil {
		return 0, err
	}
	return b.Remaining(), nil
}

// RemainingSessionGPUHours implements policy.SessionBudget.
func (m Meter) RemainingSessionGPUHours(ctx context.Context, sessionID string) (float64, error) {
	d := m.defaults()
	if m.Pool == nil {
		return d.Budgets.AgentGPUHoursPerSession.Value, nil
	}
	b, err := SessionBudgetOf(ctx, m.Pool, d, sessionID)
	if err != nil {
		return 0, err
	}
	return b.Hours - b.Used - b.Committed, nil
}

// Spend implements notify.SpendFunc: the project's metered GPU-hours in [since, until).
func Spend(ctx context.Context, q storage.Querier, projectID string, since, until time.Time) (float64, bool, error) {
	h, err := ProjectGPUHours(ctx, q, projectID, since, until)
	return round(h, 6), true, err
}
