package experiments

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/usunrise88/cadence/control-plane/internal/auth"
	"github.com/usunrise88/cadence/control-plane/internal/commands"
	"github.com/usunrise88/cadence/control-plane/internal/events"
	"github.com/usunrise88/cadence/control-plane/internal/problems"
	"github.com/usunrise88/cadence/control-plane/internal/runs"
	"github.com/usunrise88/cadence/control-plane/internal/storage"
)

// Point is one point of a sweep as stored: its values, the estimate it had when the sweep was planned, and its run
// once started.
type Point struct {
	Index            int            `json:"index"`
	Values           map[string]any `json:"values"`
	EstimateGPUHours float64        `json:"estimateGpuHours"`
	RunID            string         `json:"runId,omitempty"`
}

// sweepRow is a sweeps row.
type sweepRow struct {
	ID           string
	ExperimentID string
	ProjectID    string
	Mode         string
	Parameters   []Param
	Seed         *int
	Steps        *int
	Priority     int
	Cap          float64
	RecipeRef    string
	Points       []Point
	CurrentRunID string
	Estimate     float64
	State        string
	StopReason   string
	Actor        auth.Actor
	Rev          int
	CreatedAt    time.Time
	UpdatedAt    time.Time
	FinishedAt   *time.Time
}

const sweepCols = `id, experiment_id, project_id, mode, parameters, seed, steps, priority, gpu_hour_cap, recipe_ref, points,
	coalesce(current_run_id, ''), estimate_gpu_hours, state, coalesce(stop_reason, ''), actor, rev, created_at, updated_at, finished_at`

func scanSweep(r pgx.CollectableRow) (sweepRow, error) {
	var x sweepRow
	err := r.Scan(&x.ID, &x.ExperimentID, &x.ProjectID, &x.Mode, &x.Parameters, &x.Seed, &x.Steps, &x.Priority, &x.Cap, &x.RecipeRef,
		&x.Points, &x.CurrentRunID, &x.Estimate, &x.State, &x.StopReason, &x.Actor, &x.Rev, &x.CreatedAt, &x.UpdatedAt, &x.FinishedAt)
	return x, err
}

func getSweep(ctx context.Context, q storage.Querier, id, lock string) (sweepRow, error) {
	rows, err := q.Query(ctx, "SELECT "+sweepCols+" FROM sweeps WHERE id = $1 "+lock, id)
	if err != nil {
		return sweepRow{}, fmt.Errorf("read sweep %s: %w", id, err)
	}
	x, err := pgx.CollectExactlyOneRow(rows, scanSweep)
	if errors.Is(err, pgx.ErrNoRows) {
		return sweepRow{}, problems.NotFound.New("no sweep %q", id)
	}
	if err != nil {
		return sweepRow{}, fmt.Errorf("read sweep %s: %w", id, err)
	}
	return x, nil
}

func listSweeps(ctx context.Context, q storage.Querier, experimentID string) ([]sweepRow, error) {
	rows, err := q.Query(ctx, "SELECT "+sweepCols+" FROM sweeps WHERE experiment_id = $1 ORDER BY created_at DESC, id DESC", experimentID)
	if err != nil {
		return nil, fmt.Errorf("list sweeps: %w", err)
	}
	out, err := pgx.CollectRows(rows, scanSweep)
	if err != nil {
		return nil, fmt.Errorf("read sweeps: %w", err)
	}
	return out, nil
}

func saveSweep(ctx context.Context, tx pgx.Tx, sw *sweepRow) error {
	rows, err := tx.Query(ctx, `UPDATE sweeps SET points = $2, current_run_id = NULLIF($3, ''), state = $4, stop_reason = NULLIF($5, ''),
		finished_at = $6, rev = rev + 1, updated_at = now() WHERE id = $1 RETURNING `+sweepCols,
		sw.ID, sw.Points, sw.CurrentRunID, sw.State, sw.StopReason, sw.FinishedAt)
	if err != nil {
		return fmt.Errorf("save sweep %s: %w", sw.ID, err)
	}
	x, err := pgx.CollectExactlyOneRow(rows, scanSweep)
	if err != nil {
		return fmt.Errorf("save sweep %s: %w", sw.ID, err)
	}
	*sw = x
	return nil
}

// ---------------------------------------------------------------- plan

// SweepInput is sweeps.run on an experiment.
type SweepInput struct {
	ExperimentID string
	Rev          int // the experiment's revision (If-Match); < 0 skips the check
	Mode         string
	Parameters   []Param
	Runs         int
	Cap          float64
	Seed         *int
	Steps        *int
	Priority     int
	Actor        auth.Actor
}

// SweepPlan is a sweep checked and estimated: every point prepared as a run (the runs.new path) and the sum of their
// estimates against the cap and today's budget.
type SweepPlan struct {
	Exp          row
	In           SweepInput // mode, seed and cap resolved
	Points       []Point
	Total        runs.Range
	Basis        string
	Fits         int
	WithinCap    bool
	Remaining    float64
	WithinBudget bool
	RecipeRef    string
}

// Plan resolves a sweeps.run request: the points (grid or random draw), each prepared as a run of the experiment
// (recipe, parameters and ranges checked by the pipeline engine, the estimate), the recipe pinned at the commit the
// first point read it at, and the total against the cap and the project's remaining GPU budget.
func (s *Service) Plan(ctx context.Context, q storage.Querier, in SweepInput) (SweepPlan, error) {
	d := s.defaults()
	x, err := getExp(ctx, q, in.ExperimentID, "")
	if err != nil {
		return SweepPlan{}, err
	}
	if in.Rev >= 0 {
		if err := commands.CheckRev(Kind, in.Rev, x.Rev); err != nil {
			return SweepPlan{}, err
		}
	}
	in.Mode = or(in.Mode, d.Sweeps.Mode.Value)
	if !d.Sweeps.Mode.Range.Allows(in.Mode) {
		return SweepPlan{}, problems.Validation([]problems.FieldError{{Path: "/mode", Message: fmt.Sprintf("%q is not one of %v", in.Mode, d.Sweeps.Mode.Range.Values)}})
	}
	if in.Cap <= 0 {
		in.Cap = d.Sweeps.GPUHourCap.Value
	}
	if err := d.Sweeps.GPUHourCap.Range.Check(in.Cap); err != nil {
		return SweepPlan{}, problems.Validation([]problems.FieldError{{Path: "/gpuHourCap", Message: fmt.Sprintf("%v (defaults.yaml sweeps.gpu_hour_cap)", err)}})
	}
	if in.Seed == nil {
		seed := d.Sweeps.Seed.Value
		in.Seed = &seed
	}
	values, err := Points(in.Mode, in.Parameters, in.Runs, d.Sweeps.RandomRuns.Value, d.Sweeps.MaxRuns.Value, *in.Seed)
	if err != nil {
		return SweepPlan{}, err
	}
	pl := SweepPlan{Exp: x, In: in}
	bases := map[string]bool{}
	for i, vals := range values {
		ri, err := pointInput(x, in.Steps, in.Priority, "", pl.RecipeRef, vals, in.Actor)
		if err != nil {
			return SweepPlan{}, err
		}
		p, err := s.Runs.Prepare(ctx, q, ri)
		if err != nil {
			return SweepPlan{}, atPoint(i, vals, err)
		}
		if i == 0 { // a name the train step's kind lacks was refused by the engine (pipeline-invalid)
			pl.RecipeRef = p.Source.Commit
		}
		g := p.Estimate.GPUHours
		pl.Points = append(pl.Points, Point{Index: i, Values: vals, EstimateGPUHours: round6(g.Value)})
		pl.Total.Value += g.Value
		pl.Total.Low += g.Low
		pl.Total.High += g.High
		if pl.Total.Value <= in.Cap+1e-9 {
			pl.Fits++
		}
		bases[p.Estimate.Basis] = true
	}
	pl.Total = runs.Range{Value: round6(pl.Total.Value), Low: round6(pl.Total.Low), High: round6(pl.Total.High)}
	pl.WithinCap = pl.Fits == len(pl.Points)
	switch len(bases) {
	case 1:
		for b := range bases {
			pl.Basis = b
		}
	default:
		pl.Basis = "mixed"
	}
	b, err := runs.ProjectBudgetOf(ctx, q, d, x.ProjectID, s.now())
	if err != nil {
		return SweepPlan{}, err
	}
	pl.Remaining = round6(b.Remaining())
	pl.WithinBudget = pl.Total.Value <= pl.Remaining+1e-9
	return pl, nil
}

// pointInput is the runs.new input of one point of a sweep on experiment x: the experiment's mix revision and base
// model, the point's values as train-step overrides (replayShare as the rendered mix's replay share), and the
// recipe at ref.
func pointInput(x row, steps *int, priority int, sweepID, ref string, vals map[string]any, actor auth.Actor) (runs.NewInput, error) {
	in := runs.NewInput{ProjectID: x.ProjectID, Actor: actor, Init: runs.InitBase, BaseModel: x.BaseVersionID, Mix: x.MixID,
		MixRevision: x.MixRev, Steps: steps, Ref: ref, Priority: priority, ExperimentID: x.ID, SweepID: sweepID, Params: map[string]any{}}
	for k, v := range vals {
		if k == ReplayShare {
			f, ok := number(v)
			if !ok {
				return runs.NewInput{}, problems.Validation([]problems.FieldError{{Path: "/parameters", Message: fmt.Sprintf("replayShare is a number, not %v", v)}})
			}
			in.ReplayShare = &f
			continue
		}
		in.Params[k] = v
	}
	return in, nil
}

// atPoint names the point a problem came from.
func atPoint(i int, vals map[string]any, err error) error {
	var pe *problems.Error
	if !errors.As(err, &pe) {
		return err
	}
	b, _ := json.Marshal(vals)
	out := *pe
	out.Detail = fmt.Sprintf("point %d %s: %s", i, b, pe.Detail)
	return &out
}

// ---------------------------------------------------------------- start and advance

// Start writes a planned sweep and starts its first run. The whole estimate must fit the cap (sweep-over-cap), and
// only one sweep of a project runs at a time (its runs share the project's slot).
func (s *Service) Start(ctx context.Context, tx pgx.Tx, pl SweepPlan) (SweepView, []events.Draft, error) {
	if !pl.WithinCap {
		return SweepView{}, nil, problems.SweepOverCap.New("the sweep's %d runs are estimated at %.3g GPU-hours (%.3g–%.3g), over its cap of %.3g; the first %d fit: lower runs, narrow the grid or raise gpuHourCap",
			len(pl.Points), pl.Total.Value, pl.Total.Low, pl.Total.High, pl.In.Cap, pl.Fits)
	}
	var running string
	err := tx.QueryRow(ctx, "SELECT id FROM sweeps WHERE project_id = $1 AND state = $2", pl.Exp.ProjectID, StateRunning).Scan(&running)
	switch {
	case err == nil:
		return SweepView{}, nil, problems.Conflict.New("sweep %s is running in this project; sweeps queue their runs one after another on the project's slot — wait for it to end or cancel its current run", running)
	case !errors.Is(err, pgx.ErrNoRows):
		return SweepView{}, nil, fmt.Errorf("find running sweep: %w", err)
	}
	x, err := getExp(ctx, tx, pl.Exp.ID, "FOR UPDATE")
	if err != nil {
		return SweepView{}, nil, err
	}
	if pl.In.Rev >= 0 {
		if err := commands.CheckRev(Kind, pl.In.Rev, x.Rev); err != nil {
			return SweepView{}, nil, err
		}
	}
	id := "swp_" + uuid.Must(uuid.NewV7()).String()
	rows, err := tx.Query(ctx, `INSERT INTO sweeps (id, experiment_id, project_id, mode, parameters, seed, steps, priority, gpu_hour_cap,
		recipe_ref, points, estimate_gpu_hours, actor) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13) RETURNING `+sweepCols,
		id, x.ID, x.ProjectID, pl.In.Mode, pl.In.Parameters, pl.In.Seed, pl.In.Steps, pl.In.Priority, pl.In.Cap, pl.RecipeRef, pl.Points,
		pl.Total.Value, pl.In.Actor)
	if err != nil {
		return SweepView{}, nil, fmt.Errorf("insert sweep: %w", err)
	}
	sw, err := pgx.CollectExactlyOneRow(rows, scanSweep)
	if err != nil {
		return SweepView{}, nil, fmt.Errorf("insert sweep: %w", err)
	}
	if x, err = bump(ctx, tx, x.ID); err != nil {
		return SweepView{}, nil, err
	}
	drafts := []events.Draft{sweepDraft(x, sw, EventSweepStart)}
	more, err := s.advance(ctx, tx, x, &sw, true)
	if err != nil {
		return SweepView{}, nil, err
	}
	v, err := s.sweepView(ctx, tx, sw)
	return v, append(drafts, more...), err
}

// bump raises the experiment's revision (a sweep started on it).
func bump(ctx context.Context, tx pgx.Tx, id string) (row, error) {
	rows, err := tx.Query(ctx, "UPDATE experiments SET rev = rev + 1, updated_at = now() WHERE id = $1 RETURNING "+expCols, id)
	if err != nil {
		return row{}, fmt.Errorf("update experiment %s: %w", id, err)
	}
	x, err := pgx.CollectExactlyOneRow(rows, scanExp)
	if err != nil {
		return row{}, fmt.Errorf("update experiment %s: %w", id, err)
	}
	return x, nil
}

// advance starts the sweep's next run when its current one has ended: the next point without a run, prepared again
// through the runs.new path (a fresh estimate) and started when the GPU-hours the sweep's runs used plus that
// estimate stay within the cap; else the sweep stops. A run that ends at once (every step reused) is followed by the
// next point in the same call. Inside sweeps.run (strict) a point that cannot start fails the command; later it
// fails the sweep, never the transaction that ended the previous run.
func (s *Service) advance(ctx context.Context, tx pgx.Tx, x row, sw *sweepRow, strict bool) ([]events.Draft, error) {
	var drafts []events.Draft
	for sw.State == StateRunning {
		if sw.CurrentRunID != "" {
			status, err := runStatus(ctx, tx, sw.CurrentRunID)
			if err != nil {
				return nil, err
			}
			if !runs.Ended(status) {
				return drafts, nil
			}
			if status == runs.StatusCancelled {
				more, err := s.finish(ctx, tx, x, sw, StateCancelled, fmt.Sprintf("run %s was cancelled", sw.CurrentRunID))
				return append(drafts, more...), err
			}
		}
		next := -1
		for i, p := range sw.Points {
			if p.RunID == "" {
				next = i
				break
			}
		}
		if next < 0 {
			more, err := s.finish(ctx, tx, x, sw, StateDone, "")
			return append(drafts, more...), err
		}
		spent, err := s.spent(ctx, tx, *sw)
		if err != nil {
			return nil, err
		}
		pt := sw.Points[next]
		v, more, stop, err := s.startPoint(ctx, tx, x, *sw, pt, spent)
		if err != nil {
			if strict {
				return nil, err
			}
			stop = &stopAs{StateFailed, fmt.Sprintf("point %d could not start: %v", pt.Index, err)}
		}
		if stop != nil {
			if strict && stop.state == StateStopped {
				return nil, problems.SweepOverCap.New("%s", stop.reason)
			}
			more, err := s.finish(ctx, tx, x, sw, stop.state, stop.reason)
			return append(drafts, more...), err
		}
		drafts = append(drafts, more...)
		sw.Points[next].RunID, sw.CurrentRunID = v.ID, v.ID
		if err := saveSweep(ctx, tx, sw); err != nil {
			return nil, err
		}
		drafts = append(drafts, sweepDraft(x, *sw, EventSweepStep))
	}
	return drafts, nil
}

type stopAs struct{ state, reason string }

// startPoint prepares and creates the run of one point in a savepoint, so a point that cannot start leaves nothing
// behind. stop is set (and nothing created) when the run would pass the cap.
func (s *Service) startPoint(ctx context.Context, tx pgx.Tx, x row, sw sweepRow, pt Point, spent float64) (runs.View, []events.Draft, *stopAs, error) {
	in, err := pointInput(x, sw.Steps, sw.Priority, sw.ID, sw.RecipeRef, pt.Values, sw.Actor)
	if err != nil {
		return runs.View{}, nil, nil, err
	}
	sp, err := tx.Begin(ctx)
	if err != nil {
		return runs.View{}, nil, nil, fmt.Errorf("savepoint: %w", err)
	}
	rollback := func() { _ = sp.Rollback(ctx) }
	p, err := s.Runs.Prepare(ctx, sp, in)
	if err != nil {
		rollback()
		return runs.View{}, nil, nil, atPoint(pt.Index, pt.Values, err)
	}
	if next := p.Estimate.GPUHours.Value; spent+next > sw.Cap+1e-9 {
		rollback()
		return runs.View{}, nil, &stopAs{StateStopped, fmt.Sprintf("the GPU-hour cap: the sweep's runs used %.3g GPU-hours and point %d is estimated at %.3g, over the cap of %.3g",
			spent, pt.Index, next, sw.Cap)}, nil
	}
	v, drafts, err := s.Runs.Create(ctx, sp, p)
	if err != nil {
		rollback()
		return runs.View{}, nil, nil, atPoint(pt.Index, pt.Values, err)
	}
	if err := sp.Commit(ctx); err != nil {
		return runs.View{}, nil, nil, fmt.Errorf("release savepoint: %w", err)
	}
	return v, drafts, nil, nil
}

// finish ends a sweep in state with the reason it stopped early, and announces it.
func (s *Service) finish(ctx context.Context, tx pgx.Tx, x row, sw *sweepRow, state, reason string) ([]events.Draft, error) {
	now := s.now().UTC()
	sw.State, sw.StopReason, sw.FinishedAt = state, reason, &now
	if err := saveSweep(ctx, tx, sw); err != nil {
		return nil, err
	}
	return []events.Draft{sweepDraft(x, *sw, EventSweepEnd)}, nil
}

// spent is what the sweep's runs used on GPU cards so far.
func (s *Service) spent(ctx context.Context, q storage.Querier, sw sweepRow) (float64, error) {
	total := 0.0
	for _, p := range sw.Points {
		if p.RunID == "" {
			continue
		}
		h, err := runs.RunGPUHours(ctx, q, p.RunID)
		if err != nil {
			return 0, err
		}
		total += h
	}
	return total, nil
}

func runStatus(ctx context.Context, q storage.Querier, id string) (string, error) {
	var status string
	if err := q.QueryRow(ctx, "SELECT status FROM runs WHERE id = $1", id).Scan(&status); err != nil {
		return "", fmt.Errorf("read run %s: %w", id, err)
	}
	return status, nil
}

// sweepDraft is a sweep.* event on entity.experiment.{id}.
func sweepDraft(x row, sw sweepRow, typ string) events.Draft {
	done := 0
	for _, p := range sw.Points {
		if p.RunID != "" && p.RunID != sw.CurrentRunID {
			done++
		}
	}
	if sw.State != StateRunning && sw.CurrentRunID != "" {
		done++
	}
	return expDraft(x, typ, map[string]any{"sweep": map[string]any{
		"id": sw.ID, "state": sw.State, "stopReason": sw.StopReason, "currentRunId": sw.CurrentRunID,
		"points": len(sw.Points), "started": countStarted(sw), "ended": done, "gpuHourCap": sw.Cap,
	}})
}

func countStarted(sw sweepRow) int {
	n := 0
	for _, p := range sw.Points {
		if p.RunID != "" {
			n++
		}
	}
	return n
}

func round6(v float64) float64 { return math.Round(v*1e6) / 1e6 }

// ---------------------------------------------------------------- views

// PointView is a point as the contract's SweepPoint shows it.
type PointView struct {
	Index            int            `json:"index"`
	Values           map[string]any `json:"values"`
	State            string         `json:"state"`
	RunID            string         `json:"runId,omitempty"`
	EstimateGPUHours *float64       `json:"estimateGpuHours,omitempty"`
	GPUHours         *float64       `json:"gpuHours,omitempty"`
}

// SweepView is the contract's Sweep.
type SweepView struct {
	ID               string      `json:"id"`
	ExperimentID     string      `json:"experimentId"`
	Mode             string      `json:"mode"`
	Parameters       []Param     `json:"parameters"`
	Seed             *int        `json:"seed,omitempty"`
	Steps            *int        `json:"steps,omitempty"`
	GPUHourCap       float64     `json:"gpuHourCap"`
	State            string      `json:"state"`
	StopReason       string      `json:"stopReason,omitempty"`
	Points           []PointView `json:"points"`
	RunsDone         int         `json:"runsDone"`
	CurrentRunID     string      `json:"currentRunId,omitempty"`
	GPUHoursSpent    float64     `json:"gpuHoursSpent"`
	EstimateGPUHours float64     `json:"estimateGpuHours"`
	Rev              int         `json:"rev"`
	Actor            auth.Actor  `json:"actor"`
	CreatedAt        time.Time   `json:"createdAt"`
	UpdatedAt        time.Time   `json:"updatedAt"`
	FinishedAt       *time.Time  `json:"finishedAt,omitempty"`
}

func (s *Service) sweepView(ctx context.Context, q storage.Querier, sw sweepRow) (SweepView, error) {
	v := SweepView{ID: sw.ID, ExperimentID: sw.ExperimentID, Mode: sw.Mode, Parameters: sw.Parameters, Seed: sw.Seed, Steps: sw.Steps,
		GPUHourCap: sw.Cap, State: sw.State, StopReason: sw.StopReason, Points: make([]PointView, 0, len(sw.Points)),
		EstimateGPUHours: sw.Estimate, Rev: sw.Rev, Actor: sw.Actor, CreatedAt: sw.CreatedAt, UpdatedAt: sw.UpdatedAt, FinishedAt: sw.FinishedAt}
	if sw.State == StateRunning {
		v.CurrentRunID = sw.CurrentRunID
	}
	if v.Parameters == nil {
		v.Parameters = []Param{}
	}
	for _, p := range sw.Points {
		est := p.EstimateGPUHours
		pv := PointView{Index: p.Index, Values: p.Values, RunID: p.RunID, EstimateGPUHours: &est}
		switch {
		case p.RunID != "":
			status, err := runStatus(ctx, q, p.RunID)
			if err != nil {
				return SweepView{}, err
			}
			h, err := runs.RunGPUHours(ctx, q, p.RunID)
			if err != nil {
				return SweepView{}, err
			}
			h = round6(h)
			pv.State, pv.GPUHours = status, &h
			v.GPUHoursSpent += h
			if runs.Ended(status) {
				v.RunsDone++
			}
		case sw.State == StateRunning:
			pv.State = "pending"
		default:
			pv.State = "skipped"
		}
		v.Points = append(v.Points, pv)
	}
	v.GPUHoursSpent = round6(v.GPUHoursSpent)
	return v, nil
}

// PlanJSON is the contract's SweepPlan.
func (pl SweepPlan) PlanJSON() map[string]any {
	points := make([]PointView, 0, len(pl.Points))
	for _, p := range pl.Points {
		est := p.EstimateGPUHours
		points = append(points, PointView{Index: p.Index, Values: p.Values, State: "pending", EstimateGPUHours: &est})
	}
	out := map[string]any{
		"experimentId": pl.Exp.ID, "mode": pl.In.Mode, "seed": pl.In.Seed, "points": points,
		"estimateGpuHours": map[string]float64{"value": pl.Total.Value, "low": pl.Total.Low, "high": pl.Total.High},
		"gpuHourCap":       pl.In.Cap, "withinCap": pl.WithinCap, "fits": pl.Fits, "basis": pl.Basis,
		"budget": map[string]any{"remainingGpuHours": pl.Remaining, "withinDailyBudget": pl.WithinBudget},
	}
	if pl.In.Steps != nil {
		out["steps"] = *pl.In.Steps
	}
	return out
}
