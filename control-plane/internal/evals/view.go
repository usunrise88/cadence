package evals

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/usunrise88/cadence/control-plane/internal/auth"
	"github.com/usunrise88/cadence/control-plane/internal/pipelines"
	"github.com/usunrise88/cadence/control-plane/internal/storage"
)

// CellView is a cell as evals.get answers it (the contract's EvalCell).
type CellView struct {
	ID                 string          `json:"id"`
	Role               string          `json:"role"`
	GoldenSetVersionID string          `json:"goldenSetVersionId"`
	Profile            string          `json:"profile"`
	DecodingIndex      int             `json:"decodingIndex"`
	AugmentationIndex  int             `json:"augmentationIndex"`
	DecodingHash       string          `json:"decodingHash"`
	ModelKey           string          `json:"modelKey"`
	State              string          `json:"state"`
	RecordID           string          `json:"recordId,omitempty"`
	Scores             string          `json:"scores,omitempty"`
	Hypotheses         string          `json:"hypotheses,omitempty"`
	Summary            json.RawMessage `json:"summary,omitempty"`
	Delta              json.RawMessage `json:"delta,omitempty"`
	Worst              []Worst         `json:"worst,omitempty"`
	Metrics            *MetricsView    `json:"metrics,omitempty"`
}

// Progress counts an eval's cells.
type Progress struct {
	CellsTotal  int `json:"cellsTotal"`
	CellsDone   int `json:"cellsDone"`
	CellsCached int `json:"cellsCached"`
}

// View is an eval as evals.get answers it (the contract's Eval); evals.list leaves the cells out.
type View struct {
	ID             string          `json:"id"`
	ProjectID      string          `json:"projectId"`
	Status         string          `json:"status"`
	Error          string          `json:"error,omitempty"`
	Subject        Model           `json:"subject"`
	Baseline       Model           `json:"baseline"`
	GoldenSets     []GoldenSet     `json:"goldenSets"`
	Profiles       []Profile       `json:"profiles"`
	PrimaryProfile string          `json:"primaryProfile"`
	Decoding       []Decoding      `json:"decoding"`
	Augmentations  []Augmentation  `json:"augmentations"`
	Significance   Significance    `json:"significance"`
	PipelineRunID  string          `json:"pipelineRunId,omitempty"`
	Progress       Progress        `json:"progress"`
	Estimate       Estimate        `json:"estimate"`
	Gate           json.RawMessage `json:"gate,omitempty"`
	Cells          []CellView      `json:"cells,omitempty"`
	Robustness     []RobustnessRow `json:"robustness,omitempty"`
	Rev            int             `json:"rev"`
	Actor          auth.Actor      `json:"actor"`
	CreatedAt      time.Time       `json:"createdAt"`
	UpdatedAt      time.Time       `json:"updatedAt"`
	FinishedAt     *time.Time      `json:"finishedAt,omitempty"`
}

// ViewQuery shapes evals.get: filters on the cells and the worst utterances of the matching subject cells.
type ViewQuery struct {
	NoCells   bool   // evals.list
	Worst     int    // add the N worst utterances to each matching subject cell (or to Cell)
	Cell      string // only this cell
	GoldenSet string // only cells of this golden set (version id or collection name)
	Profile   string
	Role      string
}

// matches reports whether cell c passes the query's filters.
func (vq ViewQuery) matches(c Cell, gsName string) bool {
	switch {
	case vq.Cell != "" && c.ID != vq.Cell:
		return false
	case vq.GoldenSet != "" && c.GoldenSetVersionID != vq.GoldenSet && gsName != vq.GoldenSet:
		return false
	case vq.Profile != "" && c.Profile != vq.Profile:
		return false
	case vq.Role != "" && c.Role != vq.Role:
		return false
	}
	return true
}

// View renders eval e.
func (s *Service) View(ctx context.Context, q storage.Querier, e Eval, vq ViewQuery) (View, error) {
	v := View{
		ID: e.ID, ProjectID: e.ProjectID, Status: e.Status, Error: e.Error, Subject: e.Subject, Baseline: e.Baseline,
		GoldenSets: e.GoldenSets, Profiles: e.Profiles, PrimaryProfile: e.PrimaryProfile, Decoding: e.Decoding, Augmentations: e.Augmentations,
		Significance: e.Significance, PipelineRunID: e.PipelineRunID, Estimate: e.Estimate, Rev: e.Rev, Actor: e.Actor,
		CreatedAt: e.CreatedAt, UpdatedAt: e.UpdatedAt, FinishedAt: e.FinishedAt,
	}
	if len(e.Gate) > 0 && string(e.Gate) != "null" {
		v.Gate = e.Gate
	}
	cells, err := cellsOf(ctx, q, e.ID)
	if err != nil {
		return View{}, err
	}
	for _, c := range cells {
		v.Progress.CellsTotal++
		if c.RecordID != "" {
			v.Progress.CellsDone++
		}
		if c.State == CellCached {
			v.Progress.CellsCached++
		}
	}
	if vq.NoCells {
		return v, nil
	}
	names := map[string]string{}
	for _, g := range e.GoldenSets {
		names[g.VersionID] = g.Name
	}
	recs, err := recordsByID(ctx, q, recordIDs(cells))
	if err != nil {
		return View{}, err
	}
	running := s.runningSteps(ctx, q, e)
	v.Cells = []CellView{}
	for _, c := range cells {
		if !vq.matches(c, names[c.GoldenSetVersionID]) {
			continue
		}
		cv := CellView{ID: c.ID, Role: c.Role, GoldenSetVersionID: c.GoldenSetVersionID, Profile: c.Profile, DecodingIndex: c.DecodingIndex,
			AugmentationIndex: c.AugmentationIndex, DecodingHash: c.DecodingHash, ModelKey: c.ModelKey, State: c.State, RecordID: c.RecordID}
		if cv.Metrics, err = metricsOf(ctx, q, c); err != nil {
			return View{}, err
		}
		if c.State == CellQueued && running[c.ScoreStep] {
			cv.State = CellRunning
		}
		if len(c.Delta) > 0 && string(c.Delta) != "null" {
			cv.Delta = c.Delta
		}
		if r, ok := recs[c.RecordID]; ok {
			cv.Scores, cv.Hypotheses, cv.Summary = r.Scores, r.Hypotheses, r.Summary
			if vq.Worst > 0 && (c.Role == RoleSubject || vq.Cell != "") {
				if rows, err := ReadUtterances(s.CAS, r.Scores); err == nil {
					cv.Worst = WorstOf(rows, vq.Worst)
				}
			}
		}
		v.Cells = append(v.Cells, cv)
	}
	v.Robustness = robustness(cells, recs, func(gsID string) bool { return s.charScored(e, gsID) })
	return v, nil
}

func recordIDs(cells []Cell) []string {
	var ids []string
	for _, c := range cells {
		if c.RecordID != "" {
			ids = append(ids, c.RecordID)
		}
	}
	return ids
}

// runningSteps names the score steps whose cell is being computed: its transcribe or score step runs or is done.
func (s *Service) runningSteps(ctx context.Context, q storage.Querier, e Eval) map[string]bool {
	out := map[string]bool{}
	if e.PipelineRunID == "" || e.Status != StatusRunning {
		return out
	}
	pr, err := pipelines.Get(ctx, q, e.PipelineRunID)
	if err != nil {
		return out
	}
	for _, st := range pr.Steps {
		active := st.State == pipelines.StepRunning || st.State == pipelines.StepDone || st.State == pipelines.StepReused
		if !active {
			continue
		}
		if n, ok := strings.CutPrefix(st.Step, "transcribe-"); ok {
			out["score-"+n] = true
		} else if strings.HasPrefix(st.Step, "score-") {
			out[st.Step] = true
		}
	}
	return out
}

// Get reads eval id as a view.
func (s *Service) Get(ctx context.Context, q storage.Querier, id string, vq ViewQuery) (View, error) {
	e, err := Get(ctx, q, id)
	if err != nil {
		return View{}, err
	}
	return s.View(ctx, q, e, vq)
}

// List returns the project's evals as views without cells.
func (s *Service) List(ctx context.Context, q storage.Querier, f ListFilter) ([]View, error) {
	list, err := List(ctx, q, f)
	if err != nil {
		return nil, err
	}
	out := make([]View, 0, len(list))
	for _, e := range list {
		v, err := s.View(ctx, q, e, ViewQuery{NoCells: true})
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, nil
}
