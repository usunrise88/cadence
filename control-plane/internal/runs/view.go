package runs

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"time"

	"github.com/usunrise88/cadence/control-plane/internal/auth"
	"github.com/usunrise88/cadence/control-plane/internal/pipelines"
	"github.com/usunrise88/cadence/control-plane/internal/registry"
	"github.com/usunrise88/cadence/control-plane/internal/steps"
	"github.com/usunrise88/cadence/control-plane/internal/storage"
)

// EstimateView is an estimate's JSON form: the contract's RunEstimate.
type EstimateView struct {
	Basis           string     `json:"basis"`
	PlusMinus       float64    `json:"plusMinus"`
	GPUHours        Range      `json:"gpuHours"`
	DurationSeconds Range      `json:"durationSeconds"`
	SecondsPerStep  float64    `json:"secondsPerStep"`
	LeaseOverhead   float64    `json:"leaseOverheadSeconds"`
	Steps           int        `json:"steps"`
	GPUs            int        `json:"gpus"`
	Precision       string     `json:"precision"`
	Init            string     `json:"init"`
	BaseModelView   any        `json:"baseModel"`
	Card            Card       `json:"card"`
	Data            DataView   `json:"data"`
	Budget          BudgetView `json:"budget"`
	Source          string     `json:"source"`
	MeasuredAt      *time.Time `json:"measuredAt,omitempty"`
	Mix             *MixRef    `json:"mix,omitempty"`
}

// DataView is the data volume of an estimate.
type DataView struct {
	Datasets []string `json:"datasets"`
	Hours    float64  `json:"hours"`
	Bytes    int64    `json:"bytes"`
}

// BudgetView is an estimate against the budgets.
type BudgetView struct {
	GPUHoursPerProjectPerDay float64  `json:"gpuHoursPerProjectPerDay"`
	UsedTodayGPUHours        float64  `json:"usedTodayGpuHours"`
	RemainingGPUHours        float64  `json:"remainingGpuHours"`
	WithinDailyBudget        bool     `json:"withinDailyBudget"`
	SessionGPUHours          *float64 `json:"sessionGpuHours,omitempty"`
	SessionUsedGPUHours      *float64 `json:"sessionUsedGpuHours,omitempty"`
}

func (s *Service) renderVersion(v registry.Version) any {
	if s.RenderVersion != nil {
		return s.RenderVersion(v)
	}
	return v.Summary()
}

func (s *Service) estimateView(e Estimate) EstimateView {
	v := EstimateView{
		Basis: e.Basis, PlusMinus: e.PlusMinus, GPUHours: e.GPUHours, DurationSeconds: e.DurationSeconds, SecondsPerStep: e.SecondsPerStep,
		LeaseOverhead: e.LeaseOverheadSeconds, Steps: e.Steps, GPUs: e.GPUs, Precision: e.Precision, Init: e.Init, BaseModelView: s.renderVersion(e.BaseModel),
		Card: Card{ComputeID: e.Slot.Host.ID, Host: e.Slot.Host.Name, Index: e.Slot.Card.Index, CardClass: e.Slot.Card.CardClass,
			MemoryCapGB: e.Slot.Card.MemoryCapGB},
		Data:   DataView{Datasets: e.Data.Datasets, Hours: e.Data.Hours, Bytes: e.Data.Bytes},
		Source: e.Source, MeasuredAt: e.MeasuredAt, Mix: e.Mix,
		Budget: BudgetView{GPUHoursPerProjectPerDay: e.DailyBudget, UsedTodayGPUHours: e.UsedToday,
			RemainingGPUHours: round(e.DailyBudget-e.UsedToday-e.Committed, 3), WithinDailyBudget: e.WithinBudget},
	}
	if v.Data.Datasets == nil {
		v.Data.Datasets = []string{}
	}
	if e.SessionBudget != nil {
		h, u := e.SessionBudget.Hours, e.SessionBudget.Used
		v.Budget.SessionGPUHours, v.Budget.SessionUsedGPUHours = &h, &u
	}
	return v
}

// EstimateJSON renders an estimate as the contract's RunEstimate.
func (s *Service) EstimateJSON(e Estimate) EstimateView { return s.estimateView(e) }

// StageEntry is one step of a run's pipeline run in the Run panel's timeline (the contract's RunStageEntry).
type StageEntry struct {
	Step            string           `json:"step"`
	Kind            string           `json:"kind"`
	KindVersion     string           `json:"kindVersion"`
	Role            string           `json:"role,omitempty"`
	State           string           `json:"state"`
	Attempts        int              `json:"attempts"`
	BatchScale      float64          `json:"batchScale,omitempty"`
	OOMRetries      int              `json:"oomRetries,omitempty"`
	JobID           string           `json:"jobId,omitempty"`
	Paused          bool             `json:"paused,omitempty"`
	EstimateSeconds *float64         `json:"estimateSeconds,omitempty"`
	StartedAt       *time.Time       `json:"startedAt,omitempty"`
	FinishedAt      *time.Time       `json:"finishedAt,omitempty"`
	Error           *steps.StepError `json:"error,omitempty"`
	ReusedFrom      string           `json:"reusedFrom,omitempty"`
	ReusedFromRun   string           `json:"reusedFromRun,omitempty"`
}

// Departure is a parameter of one step that differs from defaults.yaml.
type Departure struct {
	Step    string `json:"step"`
	Param   string `json:"param"`
	Value   any    `json:"value"`
	Default any    `json:"default,omitempty"`
}

// ParentDiff is a train-step parameter (or run field) that differs from the parent run's.
type ParentDiff struct {
	Param       string `json:"param"`
	Value       any    `json:"value"`
	ParentValue any    `json:"parentValue,omitempty"`
}

// View is a run's JSON form: the contract's Run.
type View struct {
	ID               string             `json:"id"`
	ProjectID        string             `json:"projectId"`
	Status           string             `json:"status"`
	Init             string             `json:"init"`
	BaseModel        any                `json:"baseModel"`
	CheckpointID     string             `json:"checkpointId,omitempty"`
	ParentRunID      string             `json:"parentRunId,omitempty"`
	Family           map[string]string  `json:"family"`
	Mix              MixRef             `json:"mix"`
	Recipe           Recipe             `json:"recipe"`
	PipelineRunID    string             `json:"pipelineRunId"`
	TrainStep        string             `json:"trainStep"`
	Steps            int                `json:"steps"`
	Seed             *int               `json:"seed,omitempty"`
	GPUs             int                `json:"gpus"`
	Precision        string             `json:"precision"`
	Runtime          Runtime            `json:"runtime"`
	Card             Card               `json:"card"`
	Estimate         json.RawMessage    `json:"estimate,omitempty"`
	Timeline         []StageEntry       `json:"timeline"`
	CurrentJobID     string             `json:"currentJobId,omitempty"`
	ResumedFrom      string             `json:"resumedFrom,omitempty"`
	Departures       []Departure        `json:"departures"`
	ParentDiff       []ParentDiff       `json:"parentDiff,omitempty"`
	FinalMetrics     map[string]float64 `json:"finalMetrics"`
	BestCheckpointID string             `json:"bestCheckpointId,omitempty"`
	CheckpointCount  int                `json:"checkpointCount"`
	GPUHours         float64            `json:"gpuHours"`
	Error            string             `json:"error,omitempty"`
	Rev              int                `json:"rev"`
	Actor            auth.Actor         `json:"actor"`
	CreatedAt        time.Time          `json:"createdAt"`
	UpdatedAt        time.Time          `json:"updatedAt"`
	FinishedAt       *time.Time         `json:"finishedAt,omitempty"`
}

// Get returns run id as the Run panel reads it.
func (s *Service) Get(ctx context.Context, q storage.Querier, id string) (View, error) {
	x, err := getRow(ctx, q, id)
	if err != nil {
		return View{}, err
	}
	return s.view(ctx, q, x)
}

// ProjectOf returns the project run id belongs to (for scope checks).
func ProjectOf(ctx context.Context, q storage.Querier, id string) (string, error) {
	x, err := getRow(ctx, q, id)
	return x.ProjectID, err
}

// List returns runs of a project, newest first.
func (s *Service) List(ctx context.Context, q storage.Querier, f ListFilter) ([]View, error) {
	rows, err := listRows(ctx, q, f)
	if err != nil {
		return nil, err
	}
	out := make([]View, 0, len(rows))
	for _, x := range rows {
		v, err := s.view(ctx, q, x)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, nil
}

func (s *Service) view(ctx context.Context, q storage.Querier, x row) (View, error) {
	v := View{
		ID: x.ID, ProjectID: x.ProjectID, Status: x.Status, Init: x.Init, CheckpointID: x.CheckpointID, ParentRunID: x.ParentRunID,
		Family: map[string]string{"name": x.Family, "versionId": x.FamilyVersionID}, Mix: x.Mix, Recipe: x.Recipe,
		PipelineRunID: x.PipelineRunID, TrainStep: x.TrainStep, Steps: x.Steps, Seed: x.Seed, GPUs: x.GPUs, Precision: x.Precision,
		Runtime: x.Runtime, Card: x.Card, Estimate: x.Estimate, ResumedFrom: x.ResumedFrom, Error: x.Error, Rev: x.Rev,
		Actor: x.Actor, CreatedAt: x.CreatedAt, UpdatedAt: x.UpdatedAt, FinishedAt: x.FinishedAt,
		Timeline: []StageEntry{}, Departures: []Departure{}, FinalMetrics: map[string]float64{},
	}
	base, err := registry.GetVersion(ctx, q, registry.KindBaseModel, x.BaseVersionID)
	if err != nil {
		return View{}, err
	}
	v.BaseModel = s.renderVersion(base)
	fam, err := familyVersion(ctx, q, x.FamilyVersionID)
	if err != nil {
		return View{}, err
	}
	pr, err := pipelines.Get(ctx, q, x.PipelineRunID)
	if err != nil {
		return View{}, err
	}
	var train *pipelines.StepRow
	for i, st := range pr.Steps {
		e := StageEntry{Step: st.Step, Kind: st.Kind, KindVersion: st.KindVersion, Role: fam.RoleOf(st.Kind), State: st.State,
			Attempts: st.Attempts, JobID: st.JobID, EstimateSeconds: st.EstimateSeconds, StartedAt: st.StartedAt,
			FinishedAt: st.FinishedAt, Error: st.Error, ReusedFrom: st.ReusedFrom}
		if st.ReusedFrom != "" {
			if err := q.QueryRow(ctx, `SELECT coalesce((SELECT r.id FROM pipeline_steps ps JOIN runs r ON r.pipeline_run_id = ps.pipeline_run_id
				WHERE ps.id = $1), '')`, st.ReusedFrom).Scan(&e.ReusedFromRun); err != nil {
				return View{}, fmt.Errorf("run of reused step %s: %w", st.ReusedFrom, err)
			}
		}
		for _, a := range st.AttemptLog {
			if a.Reason == pipelines.ReasonOOM {
				e.OOMRetries++
			}
		}
		if n := len(st.AttemptLog); n > 0 {
			e.BatchScale = st.AttemptLog[n-1].BatchScale
		}
		if st.State == pipelines.StepQueued || st.State == pipelines.StepRunning {
			paused, _, err := jobState(ctx, q, st.JobID)
			if err != nil {
				return View{}, err
			}
			e.Paused = paused
			if v.CurrentJobID == "" {
				v.CurrentJobID = st.JobID
			}
		}
		v.Timeline = append(v.Timeline, e)
		for _, dep := range st.Departures {
			v.Departures = append(v.Departures, Departure{Step: st.Step, Param: dep.Param, Value: dep.Value, Default: dep.Default})
		}
		if st.Step == x.TrainStep {
			train = &pr.Steps[i]
			for k, m := range st.Metrics {
				v.FinalMetrics[k] = m
			}
		}
	}
	ckps, err := ListCheckpoints(ctx, q, CheckpointFilter{RunID: x.ID, Limit: 500})
	if err != nil {
		return View{}, err
	}
	v.CheckpointCount = len(ckps)
	for _, c := range ckps {
		if c.Kept {
			v.BestCheckpointID = c.ID
			break
		}
	}
	if v.GPUHours, err = RunGPUHours(ctx, q, x.ID); err != nil {
		return View{}, err
	}
	v.GPUHours = round(v.GPUHours, 6)
	if x.ParentRunID != "" && train != nil {
		if v.ParentDiff, err = parentDiff(ctx, q, x, train.Params); err != nil {
			return View{}, err
		}
	}
	return v, nil
}

// parentDiff compares run x's resolved train-step parameters (and its init, mix and pipeline) with its parent's.
func parentDiff(ctx context.Context, q storage.Querier, x row, params map[string]any) ([]ParentDiff, error) {
	parent, err := getRow(ctx, q, x.ParentRunID)
	if err != nil {
		return nil, err
	}
	pr, err := pipelines.Get(ctx, q, parent.PipelineRunID)
	if err != nil {
		return nil, err
	}
	var pp map[string]any
	for _, st := range pr.Steps {
		if st.Step == parent.TrainStep {
			pp = st.Params
		}
	}
	out := []ParentDiff{}
	add := func(param string, v, pv any) {
		if !reflect.DeepEqual(v, pv) {
			out = append(out, ParentDiff{Param: param, Value: v, ParentValue: pv})
		}
	}
	add("init", x.Init, parent.Init)
	add("mix", fmt.Sprintf("%s@%d", x.Mix.Name, x.Mix.Revision), fmt.Sprintf("%s@%d", parent.Mix.Name, parent.Mix.Revision))
	add("pipeline", x.Recipe.Pipeline+"@"+x.Recipe.Version, parent.Recipe.Pipeline+"@"+parent.Recipe.Version)
	keys := map[string]bool{}
	for k := range params {
		keys[k] = true
	}
	for k := range pp {
		keys[k] = true
	}
	for _, k := range sortedKeys(keys) {
		add(k, params[k], pp[k])
	}
	return out, nil
}
