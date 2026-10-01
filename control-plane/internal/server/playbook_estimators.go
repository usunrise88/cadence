package server

import (
	"context"
	"fmt"

	"github.com/usunrise88/cadence/control-plane/internal/auth"
	"github.com/usunrise88/cadence/control-plane/internal/defaults"
	"github.com/usunrise88/cadence/control-plane/internal/playbooks"
	"github.com/usunrise88/cadence/control-plane/internal/problems"
	"github.com/usunrise88/cadence/control-plane/internal/runs"
	"github.com/usunrise88/cadence/control-plane/internal/storage"
)

// playbookEstimators are the chain estimators of playbooks (R12, R16): runs.new from the estimate table (or the
// calibration), and runs.calibrate, runs.stage and runs.resume through the runs service's own plans. A step whose
// operation cannot plan yet — the mix or the parent run does not exist before the session — answers a problem, and
// the playbook uses the step's estimate hint.
func (s *Server) playbookEstimators() map[string]playbooks.Estimator {
	est := playbooks.DefaultEstimators()
	str := func(w map[string]any, k string) string { v, _ := w[k].(string); return v }
	need := func(w map[string]any, k, why string) (string, error) {
		if v := str(w, k); v != "" {
			return v, nil
		}
		return "", problems.EstimateUnavailable.New("%s is only known during the session (%s)", k, why)
	}
	est["runs.calibrate"] = func(ctx context.Context, q storage.Querier, _ *defaults.Defaults, projectID string, w map[string]any) (playbooks.StepEstimate, error) {
		mix, err := need(w, "mix", "the mix the calibration measures")
		if err != nil {
			return playbooks.StepEstimate{}, err
		}
		cp, err := s.runs.PlanCalibration(ctx, q, runs.CalibrateInput{ProjectID: projectID, BaseModel: str(w, "baseModel"), Mix: mix, Actor: auth.Actor{}})
		if err != nil {
			return playbooks.StepEstimate{}, err
		}
		if cp.GPUHours <= 0 {
			return playbooks.StepEstimate{}, problems.EstimateUnavailable.New("the calibrate kind %s publishes no estimate", cp.Kind.Ref())
		}
		r := playbooks.Range{Value: cp.GPUHours, Low: cp.GPUHours, High: cp.GPUHours}
		se := playbooks.StepEstimate{Basis: playbooks.BasisHint, GPUHours: &r}
		if cp.Current != nil {
			se.Note = fmt.Sprintf("a calibration of %s is cached (%.2f s/step)", cp.Key.BaseModel, cp.Current.SecondsPerStep)
		}
		return se, nil
	}
	fromRun := func(e runs.Estimate) playbooks.StepEstimate {
		gh, ds := playbooks.Range(e.GPUHours), playbooks.Range(e.DurationSeconds)
		return playbooks.StepEstimate{Basis: e.Basis, GPUHours: &gh, DurationSeconds: &ds, PlusMinus: e.PlusMinus}
	}
	est["runs.resume"] = func(ctx context.Context, q storage.Querier, _ *defaults.Defaults, _ string, w map[string]any) (playbooks.StepEstimate, error) {
		run, err := need(w, "run", "the run to resume")
		if err != nil {
			return playbooks.StepEstimate{}, err
		}
		rp, err := s.runs.PlanResume(ctx, q, run, "")
		if err != nil {
			return playbooks.StepEstimate{}, err
		}
		return fromRun(rp.Estimate), nil
	}
	est["runs.stage"] = func(ctx context.Context, q storage.Querier, _ *defaults.Defaults, _ string, w map[string]any) (playbooks.StepEstimate, error) {
		run, err := need(w, "run", "the parent run")
		if err != nil {
			return playbooks.StepEstimate{}, err
		}
		in := runs.StageInput{Mix: str(w, "mix")}
		if lr, ok := w["peakLr"].(float64); ok {
			in.PeakLR = lr
		}
		ni, err := s.runs.StageNew(ctx, q, run, -1, in, auth.Actor{})
		if err != nil {
			return playbooks.StepEstimate{}, err
		}
		pr, err := s.runs.Prepare(ctx, q, ni)
		if err != nil {
			return playbooks.StepEstimate{}, err
		}
		return fromRun(pr.Estimate), nil
	}
	return est
}
