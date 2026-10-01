// Package runs holds training runs (docs/spec/04-blocks.md Block 2): a run is one optimisation stage — a pinned
// start (base model or checkpoint, R44), a mix revision rendered as a content-hashed artifact (R13), a recipe (a
// pipeline of the project repository at a commit), a step budget and a seed — executed as one pipeline run whose
// state the run mirrors. Estimates come from calibrations (basis measured) or the defaults table (R12); checkpoints
// and calibrations come from output hooks; GPU spend is metered from leases on GPU cards.
package runs

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"slices"
	"time"

	"github.com/usunrise88/cadence/control-plane/internal/compute"
	datasets "github.com/usunrise88/cadence/control-plane/internal/data"
	"github.com/usunrise88/cadence/control-plane/internal/defaults"
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

// Input is what an estimate is made for; empty fields take their defaults.
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

	// Base is the resolved base model when the caller has it (init checkpoint: the checkpoint's run's).
	Base *registry.Version
	// Mix, when set, gives the data volume instead of Datasets.
	Mix *RenderedMix
	// SessionID is the agent session asking; its GPU budget is reported beside the project's.
	SessionID string
	Now       time.Time
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
	// LeaseOverheadSeconds is the fixed time of one lease around its steps (load, final validation, saves).
	LeaseOverheadSeconds float64
	Steps                int
	GPUs                 int
	Precision            string
	Init                 string
	BaseModel            registry.Version
	Slot                 compute.Slot
	Data                 Data
	DailyBudget          float64
	UsedToday            float64
	WithinBudget         bool
	SessionBudget        *SessionBudget
	Source               string
	MeasuredAt           *time.Time
	Mix                  *MixRef
}

// EstimateRun answers the estimate of a run (runs.new?dryRun=true, and every spending command's policy check): the
// lease overhead plus steps × seconds per step from the newest calibration of (base model collection, card class, memory cap, precision) —
// basis measured — or else from the estimate table in d — basis table —, the card from the compute entity, the data
// volume from the mix (or the named dataset versions), and today's GPU spend against the project's daily budget. The
// overhead (model load before step 1, final validation and saves) is the calibration's measurement, else
// estimates.lease_overhead_seconds; the ± applies to the steps only.
// Field problems answer validation-failed; a combination nothing covers answers estimate-unavailable.
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

	var err error
	switch {
	case in.Base != nil:
		e.BaseModel = *in.Base
	case e.Init == InitCheckpoint:
		if e.BaseModel, err = checkpointBase(ctx, q, in.ProjectID, in.Checkpoint); err != nil {
			return Estimate{}, err
		}
	default:
		if e.BaseModel, err = registry.Resolve(ctx, q, in.ProjectID, registry.KindBaseModel, or(in.BaseModel, d.Wizard.BaseModel.Value)); err != nil {
			return Estimate{}, err
		}
	}
	if e.Slot, err = compute.ForJob(ctx, q, in.Compute, compute.JobTraining); err != nil {
		return Estimate{}, err
	}
	card := e.Slot.Card
	cal, found, err := LatestCalibration(ctx, q, Key{BaseModel: e.BaseModel.Name, CardClass: card.CardClass, MemoryCapGB: card.MemoryCapGB, Precision: e.Precision})
	if err != nil {
		return Estimate{}, err
	}
	e.LeaseOverheadSeconds = d.Estimates.LeaseOverheadSeconds.Value
	if found {
		at := cal.MeasuredAt
		e.Basis, e.SecondsPerStep, e.PlusMinus, e.MeasuredAt = BasisMeasured, cal.SecondsPerStep, cal.PlusMinus, &at
		if cal.LeaseOverheadSeconds != nil {
			e.LeaseOverheadSeconds = *cal.LeaseOverheadSeconds
		}
		e.Source = "runs.calibrate at " + at.UTC().Format(time.RFC3339) + " (calibration " + cal.ArtifactHash + ")"
	} else {
		row, ok := d.TrainingEstimate(e.BaseModel.Name, card.CardClass, card.MemoryCapGB, e.Precision)
		if !ok {
			return Estimate{}, problems.EstimateUnavailable.New(
				"no calibration and no row of the estimate table in defaults.yaml for %s on card class %s at %v GB in %s; calibrate on the card first (runs.calibrate)",
				e.BaseModel.Name, card.CardClass, card.MemoryCapGB, e.Precision)
		}
		e.SecondsPerStep, e.PlusMinus, e.Source = row.SecondsPerStep, row.PlusMinus, row.Source
	}
	seconds := float64(e.Steps) * e.SecondsPerStep
	e.DurationSeconds = spreadAfter(e.LeaseOverheadSeconds, seconds, e.PlusMinus, 0)
	e.GPUHours = spreadAfter(e.LeaseOverheadSeconds*float64(e.GPUs)/3600, seconds*float64(e.GPUs)/3600, e.PlusMinus, 3)

	if in.Mix != nil {
		e.Data, e.Mix = in.Mix.Data, &in.Mix.Ref
	} else if e.Data, err = data(ctx, q, d, in.ProjectID, in.Datasets); err != nil {
		return Estimate{}, err
	}
	now := in.Now
	if now.IsZero() {
		now = time.Now()
	}
	b, err := ProjectBudgetOf(ctx, q, d, in.ProjectID, now)
	if err != nil {
		return Estimate{}, err
	}
	e.DailyBudget, e.UsedToday = b.PerDay, b.Used
	e.WithinBudget = b.Used+e.GPUHours.Value <= b.PerDay
	if in.SessionID != "" {
		sb, err := SessionBudgetOf(ctx, q, d, in.SessionID)
		if err != nil {
			return Estimate{}, err
		}
		e.SessionBudget = &sb
	}
	return e, nil
}

// checkpointBase is the base model of the run a checkpoint of the project belongs to.
func checkpointBase(ctx context.Context, q storage.Querier, projectID, id string) (registry.Version, error) {
	c, err := GetCheckpoint(ctx, q, id)
	if err != nil {
		return registry.Version{}, err
	}
	if c.ProjectID != projectID {
		return registry.Version{}, problems.NotFound.New("the project has no checkpoint %q", id)
	}
	r, err := getRow(ctx, q, c.RunID)
	if err != nil {
		return registry.Version{}, err
	}
	return registry.GetVersion(ctx, q, registry.KindBaseModel, r.BaseVersionID)
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

// spreadAfter is fixed plus v with the relative spread on v only (a lease's overhead does not scale with its steps).
func spreadAfter(fixed, v, plusMinus float64, digits int) Range {
	r := spread(v, plusMinus, 12)
	return Range{Value: round(fixed+r.Value, digits), Low: round(fixed+r.Low, digits), High: round(fixed+r.High, digits)}
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
