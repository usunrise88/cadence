// Package runs holds training runs. Phase 1 has only the estimate a dry run answers (docs/spec/08-resolutions.md
// R12, table path; R44 for init and gpus); the run itself, its job and its checkpoints arrive in phase 2.
package runs

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"slices"

	"github.com/usunrise88/cadence/control-plane/internal/compute"
	datasets "github.com/usunrise88/cadence/control-plane/internal/data"
	"github.com/usunrise88/cadence/control-plane/internal/defaults"
	"github.com/usunrise88/cadence/control-plane/internal/policies"
	"github.com/usunrise88/cadence/control-plane/internal/problems"
	"github.com/usunrise88/cadence/control-plane/internal/registry"
	"github.com/usunrise88/cadence/control-plane/internal/storage"
)

// Init values (the contract's RunInit).
const (
	InitBase       = "base"
	InitCheckpoint = "checkpoint"
)

// Basis values of an estimate.
const (
	BasisTable    = "table"
	BasisMeasured = "measured"
)

// Input is the body of runs.new; empty fields take their defaults.
type Input struct {
	ProjectID  string
	Init       string
	BaseModel  string // ver_…, collection name or @alias
	Checkpoint string
	Steps      *int
	Precision  string
	GPUs       *int
	Compute    string // host id or name
	Datasets   []string
}

// Range is a value with its low and high bound.
type Range struct {
	Value float64 `json:"value"`
	Low   float64 `json:"low"`
	High  float64 `json:"high"`
}

// Data is the data volume a run reads.
type Data struct {
	Datasets []string
	Hours    float64
	Bytes    int64
}

// Estimate is what a run would cost.
type Estimate struct {
	Basis           string
	PlusMinus       float64
	GPUHours        Range
	DurationSeconds Range
	SecondsPerStep  float64
	Steps           int
	GPUs            int
	Precision       string
	Init            string
	BaseModel       registry.Version
	Slot            compute.Slot
	Data            Data
	DailyBudget     float64
	WithinBudget    bool
	Source          string
}

// EstimateRun answers runs.new?dryRun=true from the estimate table in d, the compute entity and the policies. Field
// problems answer validation-failed; a combination the table does not cover answers estimate-unavailable.
func EstimateRun(ctx context.Context, q storage.Querier, d *defaults.Defaults, in Input) (Estimate, error) {
	e := Estimate{Basis: BasisTable, Init: or(in.Init, d.Training.Init.Value), Precision: or(in.Precision, d.Training.Precision.Value),
		Steps: d.Training.Steps.Value, GPUs: d.Training.GPUs.Value}
	if in.Steps != nil {
		e.Steps = *in.Steps
	}
	if in.GPUs != nil {
		e.GPUs = *in.GPUs
	}
	var fields []problems.FieldError
	if err := d.Training.Steps.Range.Check(float64(e.Steps)); err != nil {
		fields = append(fields, problems.FieldError{Path: "/steps", Message: err.Error() + " (defaults.yaml training.steps)"})
	}
	if err := d.Training.GPUs.Range.Check(float64(e.GPUs)); err != nil {
		fields = append(fields, problems.FieldError{Path: "/gpus", Message: err.Error() + "; multi-card runs are deferred (R44)"})
	}
	if !d.Training.Precision.Range.Allows(e.Precision) {
		fields = append(fields, problems.FieldError{Path: "/precision", Message: fmt.Sprintf("%q is not one of %v", e.Precision, d.Training.Precision.Range.Values)})
	}
	if !d.Training.Init.Range.Allows(e.Init) {
		fields = append(fields, problems.FieldError{Path: "/init", Message: fmt.Sprintf("%q is not one of %v", e.Init, d.Training.Init.Range.Values)})
	}
	if e.Init == InitCheckpoint && in.Checkpoint == "" {
		fields = append(fields, problems.FieldError{Path: "/checkpoint", Message: "required when init is checkpoint"})
	}
	if e.Init != InitCheckpoint && in.Checkpoint != "" {
		fields = append(fields, problems.FieldError{Path: "/checkpoint", Message: "only with init checkpoint"})
	}
	if len(fields) > 0 {
		return Estimate{}, problems.Validation(fields)
	}
	if e.Init == InitCheckpoint {
		return Estimate{}, problems.NotFound.New("no checkpoint %q: checkpoints arrive with training runs in roadmap phase 2", in.Checkpoint)
	}

	var err error
	if e.BaseModel, err = registry.Resolve(ctx, q, in.ProjectID, registry.KindBaseModel, or(in.BaseModel, d.Wizard.BaseModel.Value)); err != nil {
		return Estimate{}, err
	}
	if e.Slot, err = compute.ForJob(ctx, q, in.Compute, compute.JobTraining); err != nil {
		return Estimate{}, err
	}
	card := e.Slot.Card
	row, ok := d.TrainingEstimate(e.BaseModel.Name, card.CardClass, card.MemoryCapGB, e.Precision)
	if !ok {
		return Estimate{}, problems.EstimateUnavailable.New(
			"the estimate table in defaults.yaml has no row for %s on card class %s at %v GB in %s; add one, or calibrate on the card (runs.calibrate, phase 2)",
			e.BaseModel.Name, card.CardClass, card.MemoryCapGB, e.Precision)
	}
	e.SecondsPerStep, e.PlusMinus, e.Source = row.SecondsPerStep, row.PlusMinus, row.Source
	seconds := float64(e.Steps) * row.SecondsPerStep
	e.DurationSeconds = spread(math.Round(seconds), row.PlusMinus, 0)
	e.GPUHours = spread(seconds*float64(e.GPUs)/3600, row.PlusMinus, 3)

	if e.Data, err = data(ctx, q, d, in.ProjectID, in.Datasets); err != nil {
		return Estimate{}, err
	}
	pol, err := policies.Get(ctx, q, d)
	if err != nil {
		return Estimate{}, err
	}
	e.DailyBudget = pol.Budgets.GPUHoursPerProjectPerDay
	e.WithinBudget = e.GPUHours.High <= e.DailyBudget
	return e, nil
}

// data resolves the dataset references and sums their hours and bytes; a fixture without a size counts
// hours × estimates.bytes_per_audio_hour.
func data(ctx context.Context, q storage.Querier, d *defaults.Defaults, projectID string, refs []string) (Data, error) {
	out := Data{Datasets: []string{}}
	for _, ref := range refs {
		v, err := registry.Resolve(ctx, q, projectID, registry.KindDataset, ref)
		if err != nil {
			return Data{}, err
		}
		if slices.Contains(out.Datasets, v.ID) {
			continue
		}
		if err := datasets.Trainable(ctx, q, v); err != nil {
			return Data{}, err
		}
		var p struct {
			Hours float64 `json:"hours"`
			Bytes int64   `json:"bytes"`
		}
		if err := json.Unmarshal(v.Payload, &p); err != nil {
			return Data{}, fmt.Errorf("decode dataset %s: %w", v.ID, err)
		}
		if p.Bytes == 0 {
			p.Bytes = int64(p.Hours * float64(d.Estimates.BytesPerAudioHour.Value))
		}
		out.Datasets = append(out.Datasets, v.ID)
		out.Hours += p.Hours
		out.Bytes += p.Bytes
	}
	out.Hours = round(out.Hours, 3)
	return out, nil
}

func spread(v, plusMinus float64, digits int) Range {
	return Range{Value: round(v, digits), Low: round(math.Max(0, v*(1-plusMinus)), digits), High: round(v*(1+plusMinus), digits)}
}

func round(v float64, digits int) float64 {
	p := math.Pow10(digits)
	return math.Round(v*p) / p
}

func or(s, def string) string {
	if s == "" {
		return def
	}
	return s
}
