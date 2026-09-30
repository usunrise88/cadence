package playbooks

import (
	"context"
	"math"
	"strconv"

	"github.com/usunrise88/cadence/control-plane/internal/defaults"
	"github.com/usunrise88/cadence/control-plane/internal/policies"
	"github.com/usunrise88/cadence/control-plane/internal/problems"
	"github.com/usunrise88/cadence/control-plane/internal/runs"
	"github.com/usunrise88/cadence/control-plane/internal/storage"
)

// Basis values of a step estimate.
const (
	BasisTable    = "table"
	BasisMeasured = "measured"
	BasisHint     = "hint"  // the playbook's own estimate for the step
	BasisNone     = "none"  // the step spends no GPU time (or has no estimate: see Note)
	BasisMixed    = "mixed" // the sum mixes table and measured steps
)

// Range is a value with its bounds.
type Range struct {
	Value float64 `json:"value"`
	Low   float64 `json:"low"`
	High  float64 `json:"high"`
}

// StepEstimate is what one step of the chain costs (the contract's PlaybookStepEstimate).
type StepEstimate struct {
	ID              string  `json:"id"`
	Command         string  `json:"command"`
	Basis           string  `json:"basis"`
	GPUHours        *Range  `json:"gpuHours,omitempty"`
	DurationSeconds *Range  `json:"durationSeconds,omitempty"`
	Skipped         bool    `json:"skipped"`
	Note            string  `json:"note,omitempty"`
	PlusMinus       float64 `json:"-"`
}

// Budget is the project's daily GPU-hour budget against the estimate's upper bound.
type Budget struct {
	GPUHoursPerProjectPerDay float64 `json:"gpuHoursPerProjectPerDay"`
	WithinDailyBudget        bool    `json:"withinDailyBudget"`
}

// Estimate is the chain's estimate: the sum of its counted steps (the contract's PlaybookEstimate).
type Estimate struct {
	Basis           string         `json:"basis"`
	PlusMinus       float64        `json:"plusMinus"`
	GPUHours        Range          `json:"gpuHours"`
	DurationSeconds Range          `json:"durationSeconds"`
	Budget          *Budget        `json:"budget,omitempty"`
	Steps           []StepEstimate `json:"steps"`
}

// Estimator answers a step's estimate from its operation's own dry-run logic; with is the step's resolved `with`.
type Estimator func(ctx context.Context, q storage.Querier, d *defaults.Defaults, projectID string, with map[string]any) (StepEstimate, error)

// DefaultEstimators are the estimators that need nothing but the database: runs.new from the estimate table or the
// calibration (R12). The server adds runs.calibrate, runs.stage and runs.resume, which plan through the runs service.
func DefaultEstimators() map[string]Estimator {
	return map[string]Estimator{"runs.new": estimateRun}
}

func estimateRun(ctx context.Context, q storage.Querier, d *defaults.Defaults, projectID string, with map[string]any) (StepEstimate, error) {
	in := runs.Input{ProjectID: projectID}
	if s, ok := with["baseModel"].(string); ok {
		in.BaseModel = s
	}
	if f, ok := number(with["steps"]); ok {
		n := int(f)
		in.Steps = &n
	}
	if list, ok := with["datasets"].([]any); ok {
		for _, e := range list {
			if s, ok := e.(string); ok {
				in.Datasets = append(in.Datasets, s)
			}
		}
	}
	e, err := runs.EstimateRun(ctx, q, d, in)
	if err != nil {
		return StepEstimate{}, err
	}
	gh := Range(e.GPUHours)
	ds := Range(e.DurationSeconds)
	return StepEstimate{Basis: e.Basis, GPUHours: &gh, DurationSeconds: &ds, PlusMinus: e.PlusMinus}, nil
}

// EstimateChain sums the chain's step estimates (R12, R16): a step of a later phase is listed as skipped and not
// counted; a step whose operation has an estimator uses it (its error becomes the step's note, and the step is not
// counted); else the step's hint; else the step spends no GPU time. projectID may be empty (a listing without a
// project).
func EstimateChain(ctx context.Context, q storage.Querier, d *defaults.Defaults, est map[string]Estimator, p Playbook, r Resolved, projectID string) (Estimate, error) {
	steps, err := StepEstimates(ctx, q, d, est, p, r, projectID)
	if err != nil {
		return Estimate{}, err
	}
	out := Sum(steps)
	pol, err := policies.Get(ctx, q, d)
	if err != nil {
		return Estimate{}, err
	}
	b := pol.Budgets.GPUHoursPerProjectPerDay
	out.Budget = &Budget{GPUHoursPerProjectPerDay: b, WithinDailyBudget: out.GPUHours.High <= b}
	return out, nil
}

// StepEstimates estimates every step of the chain (see EstimateChain).
func StepEstimates(ctx context.Context, q storage.Querier, d *defaults.Defaults, est map[string]Estimator, p Playbook, r Resolved, projectID string) ([]StepEstimate, error) {
	out := make([]StepEstimate, 0, len(p.Chain))
	for _, s := range p.Chain {
		se := StepEstimate{ID: s.ID, Command: s.Command, Basis: BasisNone}
		switch {
		case !s.Available():
			se.Skipped = true
			se.Note = phaseNote(s.Phase)
		case est[s.Command] != nil:
			got, err := est[s.Command](ctx, q, d, projectID, With(s, r))
			if err != nil {
				pe, ok := problems.As(err)
				if !ok || pe.Type == problems.Internal {
					return nil, err
				}
				se.Note = pe.Detail
				if s.Estimate != nil { // what the operation cannot plan yet (no mix, no parent run): the hint
					se = hinted(s, se.Note)
				}
				break
			}
			got.ID, got.Command = s.ID, s.Command
			se = got
		case s.Estimate != nil:
			se = hinted(s, "")
		}
		out = append(out, se)
	}
	return out, nil
}

// hinted is the step's own estimate hint; note says why the operation's estimate was not used.
func hinted(s Step, note string) StepEstimate {
	pm := s.Estimate.PlusMinus
	return StepEstimate{ID: s.ID, Command: s.Command, Basis: BasisHint, PlusMinus: pm, Note: note,
		GPUHours: spread(s.Estimate.GPUHours, pm, 3), DurationSeconds: spread(s.Estimate.Minutes*60, pm, 0)}
}

// Sum adds the counted steps (not skipped, with GPU-hours): values and bounds add up, plusMinus is the largest of
// them, and the basis is theirs when they share one (mixed otherwise, none when nothing counts).
func Sum(steps []StepEstimate) Estimate {
	out := Estimate{Basis: BasisNone, Steps: steps}
	bases := map[string]bool{}
	for _, se := range steps {
		if se.GPUHours == nil || se.Skipped {
			continue
		}
		out.GPUHours = add(out.GPUHours, *se.GPUHours, 3)
		if se.DurationSeconds != nil {
			out.DurationSeconds = add(out.DurationSeconds, *se.DurationSeconds, 0)
		}
		out.PlusMinus = math.Max(out.PlusMinus, se.PlusMinus)
		bases[se.Basis] = true
	}
	switch {
	case len(bases) == 1:
		for b := range bases {
			out.Basis = b
		}
	case len(bases) > 1:
		out.Basis = BasisMixed
	}
	return out
}

func phaseNote(phase int) string {
	return "arrives in roadmap phase " + strconv.Itoa(phase) + "; listed and skipped"
}

func spread(v, pm float64, digits int) *Range {
	return &Range{Value: round(v, digits), Low: round(math.Max(0, v*(1-pm)), digits), High: round(v*(1+pm), digits)}
}

func add(a, b Range, digits int) Range {
	return Range{Value: round(a.Value+b.Value, digits), Low: round(a.Low+b.Low, digits), High: round(a.High+b.High, digits)}
}

func round(v float64, digits int) float64 {
	p := math.Pow10(digits)
	return math.Round(v*p) / p
}
