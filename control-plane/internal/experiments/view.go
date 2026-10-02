package experiments

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/usunrise88/cadence/control-plane/internal/auth"
	"github.com/usunrise88/cadence/control-plane/internal/pipelines"
	"github.com/usunrise88/cadence/control-plane/internal/registry"
	"github.com/usunrise88/cadence/control-plane/internal/runs"
	"github.com/usunrise88/cadence/control-plane/internal/storage"
)

// MixRef is the experiment's fixed mix revision (the contract's ExperimentMixRef).
type MixRef struct {
	ID          string  `json:"id"`
	Name        string  `json:"name"`
	Revision    int     `json:"revision"`
	ReplayShare float64 `json:"replayShare"`
}

// ParamView is a compared parameter (the contract's ExperimentParameter).
type ParamView struct {
	Name    string `json:"name"`
	Default any    `json:"default,omitempty"`
	Swept   bool   `json:"swept"`
	Departs bool   `json:"departs"`
}

// EvalRef is the latest eval of a run's best checkpoint (the contract's ExperimentEval).
type EvalRef struct {
	EvalID  string `json:"evalId"`
	Status  string `json:"status"`
	Verdict string `json:"verdict,omitempty"`
}

// RunRow is one run in the comparison (the contract's ExperimentRun).
type RunRow struct {
	RunID            string         `json:"runId"`
	Status           string         `json:"status"`
	SweepID          string         `json:"sweepId,omitempty"`
	Point            *int           `json:"point,omitempty"`
	Values           map[string]any `json:"values"`
	Departures       []string       `json:"departures"`
	BestValWER       *float64       `json:"bestValWer,omitempty"`
	BestCheckpointID string         `json:"bestCheckpointId,omitempty"`
	BestStep         *int64         `json:"bestStep,omitempty"`
	GPUHours         float64        `json:"gpuHours"`
	EstimateGPUHours *float64       `json:"estimateGpuHours,omitempty"`
	Best             bool           `json:"best"`
	Eval             *EvalRef       `json:"eval,omitempty"`
	Error            string         `json:"error,omitempty"`
	CreatedAt        time.Time      `json:"createdAt"`
	FinishedAt       *time.Time     `json:"finishedAt,omitempty"`
}

// Best is the run with the lowest validation WER and whether its checkpoint can be registered (ExperimentBest).
type Best struct {
	RunID        string   `json:"runId"`
	CheckpointID string   `json:"checkpointId"`
	ValWER       float64  `json:"valWer"`
	Eval         *EvalRef `json:"eval,omitempty"`
	Registrable  bool     `json:"registrable"`
	Reason       string   `json:"reason,omitempty"`
}

// View is the contract's Experiment.
type View struct {
	ID         string      `json:"id"`
	ProjectID  string      `json:"projectId"`
	Name       string      `json:"name"`
	Question   string      `json:"question"`
	Tag        string      `json:"tag,omitempty"`
	Mix        MixRef      `json:"mix"`
	BaseModel  any         `json:"baseModel"`
	RunCount   int         `json:"runCount"`
	Parameters []ParamView `json:"parameters,omitempty"`
	Runs       []RunRow    `json:"runs,omitempty"`
	Best       *Best       `json:"best,omitempty"`
	Sweeps     []SweepView `json:"sweeps"`
	Rev        int         `json:"rev"`
	Actor      auth.Actor  `json:"actor"`
	CreatedAt  time.Time   `json:"createdAt"`
	UpdatedAt  time.Time   `json:"updatedAt"`
}

// Get answers experiments.get: the experiment with its comparison.
func (s *Service) Get(ctx context.Context, q storage.Querier, id string) (View, error) {
	x, err := getExp(ctx, q, id, "")
	if err != nil {
		return View{}, err
	}
	return s.view(ctx, q, x, true)
}

// List answers experiments.list: the project's experiments, newest first, without the comparison rows.
func (s *Service) List(ctx context.Context, q storage.Querier, projectID string, limit int) ([]View, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := q.Query(ctx, "SELECT "+expCols+" FROM experiments WHERE project_id = $1 ORDER BY created_at DESC, id DESC LIMIT $2", projectID, limit)
	if err != nil {
		return nil, fmt.Errorf("list experiments: %w", err)
	}
	list, err := pgx.CollectRows(rows, scanExp)
	if err != nil {
		return nil, fmt.Errorf("read experiments: %w", err)
	}
	out := make([]View, 0, len(list))
	for _, x := range list {
		v, err := s.view(ctx, q, x, false)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, nil
}

// shell is the experiment without its runs: the pinned mix and base model, the run count and the sweeps.
func (s *Service) shell(ctx context.Context, q storage.Querier, x row) (View, error) {
	v := View{ID: x.ID, ProjectID: x.ProjectID, Name: x.Name, Question: x.Question, Tag: x.Tag, Rev: x.Rev, Actor: x.Actor,
		CreatedAt: x.CreatedAt, UpdatedAt: x.UpdatedAt, Sweeps: []SweepView{}}
	base, err := registry.GetVersion(ctx, q, registry.KindBaseModel, x.BaseVersionID)
	if err != nil {
		return View{}, err
	}
	v.BaseModel = s.renderVersion(base)
	_, content, rev, err := runs.ResolveMix(ctx, q, x.ProjectID, x.MixID, x.MixRev)
	if err != nil {
		return View{}, err
	}
	v.Mix = MixRef{ID: x.MixID, Name: content.Name, Revision: rev, ReplayShare: content.ReplayShare}
	if err := q.QueryRow(ctx, "SELECT count(*) FROM runs WHERE experiment_id = $1", x.ID).Scan(&v.RunCount); err != nil {
		return View{}, fmt.Errorf("count runs of %s: %w", x.ID, err)
	}
	sweeps, err := listSweeps(ctx, q, x.ID)
	if err != nil {
		return View{}, err
	}
	for _, sw := range sweeps {
		sv, err := s.sweepView(ctx, q, sw)
		if err != nil {
			return View{}, err
		}
		v.Sweeps = append(v.Sweeps, sv)
	}
	return v, nil
}

func (s *Service) view(ctx context.Context, q storage.Querier, x row, full bool) (View, error) {
	v, err := s.shell(ctx, q, x)
	if err != nil {
		return View{}, err
	}
	if !full {
		// The list names the best run without building the comparison: the lowest validation WER of any checkpoint.
		var b Best
		err := q.QueryRow(ctx, `SELECT c.run_id, c.id, c.val_wer FROM checkpoints c JOIN runs r ON r.id = c.run_id
			WHERE r.experiment_id = $1 AND c.val_wer IS NOT NULL ORDER BY c.val_wer, c.created_at, c.id LIMIT 1`, x.ID).
			Scan(&b.RunID, &b.CheckpointID, &b.ValWER)
		switch {
		case errors.Is(err, pgx.ErrNoRows):
		case err != nil:
			return View{}, fmt.Errorf("best run of %s: %w", x.ID, err)
		default:
			if err := s.judge(ctx, q, x.ProjectID, &b); err != nil {
				return View{}, err
			}
			v.Best = &b
		}
		return v, nil
	}
	return s.compare(ctx, q, x, v)
}

// pointRef is where a run sits in a sweep.
type pointRef struct {
	sweep  string
	index  int
	values map[string]any
}

// compare builds the comparison: one row per run (oldest first) with the compared parameters' values — every swept
// parameter, then every train-step parameter some run departs from defaults with — the best checkpoint by
// validation WER, GPU-hours and the latest eval of that checkpoint; and the best run.
func (s *Service) compare(ctx context.Context, q storage.Querier, x row, v View) (View, error) {
	list, err := s.Runs.List(ctx, q, runs.ListFilter{ProjectID: x.ProjectID, ExperimentID: x.ID, Limit: 500})
	if err != nil {
		return View{}, err
	}
	slices.Reverse(list) // oldest first
	sweeps, err := listSweeps(ctx, q, x.ID)
	if err != nil {
		return View{}, err
	}
	points := map[string]pointRef{}
	var swept []string
	for i := len(sweeps) - 1; i >= 0; i-- { // oldest sweep first: its parameters lead the columns
		for _, prm := range sweeps[i].Parameters {
			if !slices.Contains(swept, prm.Name) {
				swept = append(swept, prm.Name)
			}
		}
		for _, p := range sweeps[i].Points {
			if p.RunID != "" {
				points[p.RunID] = pointRef{sweep: sweeps[i].ID, index: p.Index, values: p.Values}
			}
		}
	}
	type runParams struct {
		train map[string]any
		deps  map[string]pipelines.Departure
	}
	per := make([]runParams, len(list))
	departing := map[string]bool{}
	defaults := map[string]any{ReplayShare: v.Mix.ReplayShare}
	for i, r := range list {
		rp := runParams{train: map[string]any{}, deps: map[string]pipelines.Departure{}}
		pr, err := pipelines.Get(ctx, q, r.PipelineRunID)
		if err != nil {
			return View{}, err
		}
		for _, st := range pr.Steps {
			if st.Step != r.TrainStep {
				continue
			}
			rp.train = st.Params
			for _, d := range st.Departures {
				rp.deps[d.Param] = d
				departing[d.Param] = true
				if _, ok := defaults[d.Param]; !ok && d.Default != nil {
					defaults[d.Param] = d.Default
				}
			}
		}
		per[i] = rp
	}
	names := slices.Clone(swept)
	var extra []string
	for name := range departing {
		if !slices.Contains(names, name) {
			extra = append(extra, name)
		}
	}
	sort.Strings(extra)
	names = append(names, extra...)

	params := make([]ParamView, 0, len(names))
	for _, name := range names {
		pv := ParamView{Name: name, Swept: slices.Contains(swept, name)}
		if d, ok := defaults[name]; ok {
			pv.Default = d
		} else {
			for _, rp := range per { // nobody departs: every value is the default
				if val, ok := rp.train[name]; ok {
					pv.Default = val
					break
				}
			}
		}
		params = append(params, pv)
	}

	rows := make([]RunRow, 0, len(list))
	bestIdx := -1
	for i, r := range list {
		rr := RunRow{RunID: r.ID, Status: r.Status, Values: map[string]any{}, Departures: []string{}, GPUHours: r.GPUHours,
			Error: r.Error, CreatedAt: r.CreatedAt, FinishedAt: r.FinishedAt}
		pt, inSweep := points[r.ID]
		if inSweep {
			idx := pt.index
			rr.SweepID, rr.Point = pt.sweep, &idx
		}
		for j, pv := range params {
			var val any
			departs := false
			switch pv.Name {
			case ReplayShare:
				val = v.Mix.ReplayShare
				if inSweep {
					if pval, ok := pt.values[ReplayShare]; ok {
						val = pval
					}
				}
				f, _ := number(val)
				departs = f != v.Mix.ReplayShare
			default:
				if tv, ok := per[i].train[pv.Name]; ok {
					val = tv
				} else if inSweep {
					val = pt.values[pv.Name]
				}
				_, departs = per[i].deps[pv.Name]
				if !departs && pv.Default != nil && val != nil && !reflect.DeepEqual(val, pv.Default) {
					departs = true
				}
			}
			rr.Values[pv.Name] = val
			if departs {
				rr.Departures = append(rr.Departures, pv.Name)
				params[j].Departs = true
			}
		}
		if est := estimateOf(r.Estimate); est != nil {
			rr.EstimateGPUHours = est
		}
		best, err := runs.ListCheckpoints(ctx, q, runs.CheckpointFilter{RunID: r.ID, Limit: 1})
		if err != nil {
			return View{}, err
		}
		if len(best) == 1 && best[0].ValWER != nil {
			wer := *best[0].ValWER
			rr.BestValWER, rr.BestCheckpointID, rr.BestStep = &wer, best[0].ID, best[0].Step
			if rr.Eval, _, err = s.evalOf(ctx, q, x.ProjectID, best[0].ID); err != nil {
				return View{}, err
			}
			if bestIdx < 0 || wer < *rows[bestIdx].BestValWER {
				bestIdx = i
			}
		}
		rows = append(rows, rr)
	}
	v.Parameters, v.Runs = params, rows
	if bestIdx >= 0 {
		rows[bestIdx].Best = true
		b := Best{RunID: rows[bestIdx].RunID, CheckpointID: rows[bestIdx].BestCheckpointID, ValWER: *rows[bestIdx].BestValWER}
		if err := s.judge(ctx, q, x.ProjectID, &b); err != nil {
			return View{}, err
		}
		v.Best = &b
	}
	return v, nil
}

// estimateOf reads the GPU-hours of a run's start estimate.
func estimateOf(raw json.RawMessage) *float64 {
	var e struct {
		GPUHours *struct {
			Value float64 `json:"value"`
		} `json:"gpuHours"`
	}
	if len(raw) == 0 || json.Unmarshal(raw, &e) != nil || e.GPUHours == nil {
		return nil
	}
	h := e.GPUHours.Value
	return &h
}

// judge fills the best run's eval and whether models.register would accept its checkpoint now (its latest gated
// eval passed, as models.register checks).
func (s *Service) judge(ctx context.Context, q storage.Querier, projectID string, b *Best) error {
	latest, gated, err := s.evalOf(ctx, q, projectID, b.CheckpointID)
	if err != nil {
		return err
	}
	b.Eval = latest
	switch {
	case gated != nil && gated.Verdict == "passed":
		b.Registrable = true
	case gated != nil:
		b.Reason = fmt.Sprintf("the gate failed on %s; models.register needs a passed gate — train further or evaluate another checkpoint", gated.EvalID)
	case latest != nil && latest.Status == "done":
		b.Reason = fmt.Sprintf("eval %s is not gated yet: run evals.gate on it", latest.EvalID)
	case latest != nil:
		b.Reason = fmt.Sprintf("eval %s is %s; gate it with evals.gate when it is done", latest.EvalID, latest.Status)
	default:
		b.Reason = fmt.Sprintf("checkpoint %s has no eval: run evals.new with checkpointId %s, then evals.gate", b.CheckpointID, b.CheckpointID)
	}
	return nil
}

// evalOf reads the latest eval of a checkpoint and its latest gated eval (nil when there is none).
func (s *Service) evalOf(ctx context.Context, q storage.Querier, projectID, checkpointID string) (latest, gated *EvalRef, _ error) {
	read := func(cond, order string) (*EvalRef, error) {
		var e EvalRef
		var verdict *string
		err := q.QueryRow(ctx, `SELECT id, status, gate->>'verdict' FROM evals WHERE project_id = $1 AND subject_id = $2 `+cond+
			` ORDER BY `+order+` LIMIT 1`, projectID, checkpointID).Scan(&e.EvalID, &e.Status, &verdict)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		if err != nil {
			return nil, fmt.Errorf("evals of %s: %w", checkpointID, err)
		}
		if verdict != nil {
			e.Verdict = *verdict
		}
		return &e, nil
	}
	latest, err := read("", "created_at DESC, id DESC")
	if err != nil {
		return nil, nil, err
	}
	gated, err = read("AND gate IS NOT NULL AND gate <> 'null'::jsonb", "gated_at DESC NULLS LAST, id DESC")
	return latest, gated, err
}
