package runs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/usunrise88/cadence/control-plane/internal/cas"
	"github.com/usunrise88/cadence/control-plane/internal/compute"
	"github.com/usunrise88/cadence/control-plane/internal/events"
	"github.com/usunrise88/cadence/control-plane/internal/pipelines"
	"github.com/usunrise88/cadence/control-plane/internal/problems"
	"github.com/usunrise88/cadence/control-plane/internal/registry"
	"github.com/usunrise88/cadence/control-plane/internal/steps"
	"github.com/usunrise88/cadence/control-plane/internal/storage"
)

// Key is what a measured estimate is cached by (R12): the base model's registry collection, the card class, the
// card's memory cap and the precision. The bucket configuration a calibration measured with is kept beside it.
type Key struct {
	BaseModel   string  `json:"baseModel"`
	CardClass   string  `json:"cardClass"`
	MemoryCapGB float64 `json:"memoryCapGb"`
	Precision   string  `json:"precision"`
}

// Calibration is one cached measurement.
type Calibration struct {
	Key
	BucketConfig   string          `json:"bucketConfig,omitempty"`
	SecondsPerStep float64         `json:"secondsPerStep"`
	PlusMinus      float64         `json:"plusMinus"`
	BatchSizes     json.RawMessage `json:"batchSizes,omitempty"`
	// LeaseOverheadSeconds is what a training lease adds to its steps, as the calibration measured it (nil: none).
	LeaseOverheadSeconds *float64  `json:"leaseOverheadSeconds,omitempty"`
	Family               string    `json:"family,omitempty"`
	ArtifactHash         string    `json:"artifact"`
	PipelineRunID        string    `json:"pipelineRunId,omitempty"`
	MeasuredAt           time.Time `json:"measuredAt"`
}

// LatestCalibration returns the newest calibration of k (any bucket configuration).
func LatestCalibration(ctx context.Context, q storage.Querier, k Key) (Calibration, bool, error) {
	c := Calibration{Key: k}
	err := q.QueryRow(ctx, `SELECT bucket_config, seconds_per_step, plus_minus, batch_sizes, lease_overhead_seconds, family,
			artifact_hash, coalesce(pipeline_run_id, ''), measured_at
		FROM calibrations WHERE base_model = $1 AND card_class = $2 AND memory_cap_gb = $3 AND precision = $4
		ORDER BY measured_at DESC LIMIT 1`, k.BaseModel, k.CardClass, k.MemoryCapGB, k.Precision).
		Scan(&c.BucketConfig, &c.SecondsPerStep, &c.PlusMinus, &c.BatchSizes, &c.LeaseOverheadSeconds, &c.Family, &c.ArtifactHash,
			&c.PipelineRunID, &c.MeasuredAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Calibration{}, false, nil
	}
	if err != nil {
		return Calibration{}, false, fmt.Errorf("read calibration: %w", err)
	}
	return c, true, nil
}

// calibrationMeta is the neutral metadata a calibrate step writes on its `calibration` artifact (R42). Only the
// seconds per step is required (else the step's final metric seconds_per_step); the spread is relative (±), or
// the standard deviation of the timed steps in seconds.
type calibrationMeta struct {
	Family            string          `json:"family"`
	SecondsPerStep    float64         `json:"secondsPerStep"`
	PlusMinus         *float64        `json:"plusMinus"`
	Spread            *float64        `json:"spread"`
	SecondsPerStepStd *float64        `json:"secondsPerStepStd"`
	BatchSize         *int            `json:"batchSize"`
	BatchSizes        json.RawMessage `json:"batchSizes"`
	BucketConfig      json.RawMessage `json:"bucketConfig"`
	// LeaseOverheadSeconds is the fixed time a training lease adds to its steps (model load before step 1, the
	// final validation and the saves at the end), when the step measured it.
	LeaseOverheadSeconds *float64 `json:"leaseOverheadSeconds"`
	// What the measurement ran for, when the step knows it (a run's own calibrate step, a calibration without a
	// request row).
	BaseModel   string  `json:"baseModel"`
	CardClass   string  `json:"cardClass"`
	MemoryCapGB float64 `json:"memoryCapGb"`
	Precision   string  `json:"precision"`
}

// calibrationKey finds what a calibration output measured for and whether it may replace the shared entry of that
// key in calibrations, which answers estimates for every project. Only a measurement whose origin the control plane
// itself set up is shared:
//
//   - the step belongs to a runs.calibrate pipeline run (calibration_requests) or to a training run that starts
//     from a base model (init base); the key is that request's or run's, never the artifact's metadata;
//   - its kind is the calibrate role of the model family (the run's family version; for a request the base
//     model's family);
//   - every base_model or checkpoint input is the base model the pipeline run was started with;
//   - a precision parameter, when the step has one, is the key's precision;
//   - the card of the lease that produced it, when a lease did, has the key's card class and memory cap.
//
// Anything else (an ad-hoc pipeline, a recipe's own step, a stage from a checkpoint) is keyed from its run, request
// or metadata when possible and recorded unshared, with why. skip is true for a run's output seen before the run
// row exists (steps reused inside pipelines.Engine.Start): registerExisting runs the hook again afterwards.
func calibrationKey(ctx context.Context, tx pgx.Tx, out steps.Output, m calibrationMeta) (k Key, shared bool, why string, skip bool, err error) {
	req, isRequest, err := calibrationRequest(ctx, tx, out.PipelineRunID)
	if err != nil {
		return Key{}, false, "", false, err
	}
	var (
		fam    Family
		famErr error
	)
	switch {
	case out.RunID != "":
		r, err := getRow(ctx, tx, out.RunID)
		if pe, ok := problems.As(err); err != nil && ok && pe.Type == problems.NotFound {
			return Key{}, false, "", true, nil
		}
		if err != nil {
			return Key{}, false, "", false, err
		}
		base, err := registry.GetVersion(ctx, tx, registry.KindBaseModel, r.BaseVersionID)
		if err != nil {
			return Key{}, false, "", false, err
		}
		k = Key{BaseModel: base.Name, CardClass: r.Card.CardClass, MemoryCapGB: r.Card.MemoryCapGB, Precision: r.Precision}
		switch {
		case r.Card.CardClass == "":
			return k, false, "the run has no card to key the calibration by", false, nil
		case r.Init != InitBase:
			return k, false, "the run starts from a checkpoint, not from the base model", false, nil
		}
		fam, famErr = familyVersion(ctx, tx, r.FamilyVersionID)
	case isRequest:
		k = req
		base, err := registry.Latest(ctx, tx, registry.KindBaseModel, req.BaseModel)
		if err != nil {
			return Key{}, false, "", false, err
		}
		fam, famErr = FamilyOf(ctx, tx, base)
	default:
		k = Key{BaseModel: m.BaseModel, CardClass: m.CardClass, MemoryCapGB: m.MemoryCapGB, Precision: m.Precision}
		return k, false, "not measured by runs.calibrate or by the calibrate step of a training run", false, nil
	}
	if famErr != nil {
		if _, ok := problems.As(famErr); ok {
			return k, false, famErr.Error(), false, nil
		}
		return Key{}, false, "", false, famErr
	}
	kind, err := fam.Role(RoleCalibrate)
	if err != nil {
		return k, false, err.Error(), false, nil
	}
	if out.Spec.Kind != kind {
		return k, false, fmt.Sprintf("step kind %q is not %s, the calibrate role of model family %s", out.Spec.Kind, kind, fam.Name), false, nil
	}
	why, err = calibrationInputs(ctx, tx, out, k)
	if err != nil {
		return Key{}, false, "", false, err
	}
	return k, why == "", why, false, nil
}

// calibrationInputs answers why a calibrate step's output does not measure key k ("" when it does): its base model
// inputs, its precision and the card of its lease.
func calibrationInputs(ctx context.Context, tx pgx.Tx, out steps.Output, k Key) (string, error) {
	pr, err := pipelines.GetRun(ctx, tx, out.PipelineRunID)
	if err != nil {
		return "", err
	}
	want := ""
	for _, name := range sortedKeys(pr.Inputs) {
		if ref := pr.Inputs[name]; ref.Type == TypeBaseModel {
			if want != "" && want != ref.Hash {
				return "the pipeline run was started with two base models", nil
			}
			want = ref.Hash
		}
	}
	if want == "" {
		return "the pipeline run was not started from a base model", nil
	}
	seen := false
	for _, name := range sortedKeys(out.Spec.Inputs) {
		ref := out.Spec.Inputs[name]
		if ref.Type != TypeBaseModel && ref.Type != TypeCheckpoint {
			continue
		}
		if ref.Hash != want {
			return fmt.Sprintf("input %q is not the base model the pipeline run was started with", name), nil
		}
		seen = true
	}
	if !seen {
		return "the step has no base model input", nil
	}
	if len(out.Spec.Params) > 0 {
		var p struct {
			Precision *string `json:"precision"`
		}
		if json.Unmarshal(out.Spec.Params, &p) == nil && p.Precision != nil && *p.Precision != k.Precision {
			return fmt.Sprintf("the step measured precision %s, the key is %s", *p.Precision, k.Precision), nil
		}
	}
	class, capGB, found, err := leaseCard(ctx, tx, out.Artifact.Hash)
	if err != nil || !found {
		return "", err
	}
	if class != k.CardClass || capGB != k.MemoryCapGB {
		return fmt.Sprintf("measured on a card of class %q capped at %g GB, the key is %s at %g GB", class, capGB, k.CardClass, k.MemoryCapGB), nil
	}
	return "", nil
}

// calibrationRequest reads the key a runs.calibrate pipeline run measures for.
func calibrationRequest(ctx context.Context, q storage.Querier, pipelineRunID string) (Key, bool, error) {
	var k Key
	err := q.QueryRow(ctx, `SELECT base_model, card_class, memory_cap_gb, precision FROM calibration_requests
		WHERE pipeline_run_id = $1`, pipelineRunID).Scan(&k.BaseModel, &k.CardClass, &k.MemoryCapGB, &k.Precision)
	if errors.Is(err, pgx.ErrNoRows) {
		return Key{}, false, nil
	}
	if err != nil {
		return Key{}, false, fmt.Errorf("read calibration request: %w", err)
	}
	return k, true, nil
}

// leaseCard is the card class and memory cap of the card whose lease produced artifact hash (the newest lease on a
// card of its producing step's job); found is false when no lease on a card produced it.
func leaseCard(ctx context.Context, q storage.Querier, hash string) (string, float64, bool, error) {
	var (
		hostID string
		index  int
	)
	err := q.QueryRow(ctx, `SELECT l.host_id, l.card_index FROM artifacts a
		JOIN pipeline_steps ps ON ps.id = a.step_id JOIN leases l ON l.job_id = ps.job_id
		WHERE a.hash = $1 AND l.card_index IS NOT NULL ORDER BY l.created_at DESC LIMIT 1`, hash).Scan(&hostID, &index)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", 0, false, nil
	}
	if err != nil {
		return "", 0, false, fmt.Errorf("read the lease of calibration %s: %w", hash, err)
	}
	h, err := compute.Get(ctx, q, hostID)
	if err != nil {
		return "", 0, false, err
	}
	for _, c := range h.Cards {
		if c.Index == index {
			return c.CardClass, c.MemoryCapGB, true, nil
		}
	}
	return "", 0, true, nil // the card left the host's configuration: no class matches
}

// calibrationHook records a `calibration` output (calibration_observations) and caches it in the shared table when
// calibrationKey trusts it; idempotent per artifact, a reused output writes the same rows. An output that carries
// no seconds per step is left alone; the step still succeeds.
func (s *Service) calibrationHook(ctx context.Context, tx pgx.Tx, out steps.Output) ([]events.Draft, error) {
	var m calibrationMeta
	if len(out.Artifact.Meta) > 0 {
		if err := json.Unmarshal(out.Artifact.Meta, &m); err != nil {
			return nil, fmt.Errorf("calibration meta: %w", err)
		}
	}
	if m.SecondsPerStep <= 0 {
		m.SecondsPerStep = out.Metrics["seconds_per_step"]
	}
	if m.SecondsPerStep <= 0 || math.IsInf(m.SecondsPerStep, 0) || math.IsNaN(m.SecondsPerStep) {
		return nil, nil
	}
	k, shared, why, skip, err := calibrationKey(ctx, tx, out, m)
	if err != nil || skip {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO calibration_observations (artifact_hash, pipeline_run_id, step_id, project_id, run_id,
			base_model, card_class, memory_cap_gb, precision, seconds_per_step, shared, reason)
		VALUES ($1, $2, $3, NULLIF($4, ''), NULLIF($5, ''), $6, $7, $8, $9, $10, $11, $12)
		ON CONFLICT (artifact_hash, pipeline_run_id, step_id) DO UPDATE SET shared = excluded.shared, reason = excluded.reason`,
		out.Artifact.Hash, out.PipelineRunID, out.StepID, out.ProjectID, out.RunID, k.BaseModel, k.CardClass, k.MemoryCapGB,
		k.Precision, m.SecondsPerStep, shared, why); err != nil {
		return nil, fmt.Errorf("record calibration: %w", err)
	}
	if !shared {
		return nil, nil
	}
	pm := s.defaults().Estimates.MeasuredPlusMinus.Value
	switch {
	case m.PlusMinus != nil:
		pm = *m.PlusMinus
	case m.Spread != nil:
		pm = *m.Spread
	case m.SecondsPerStepStd != nil:
		pm = 2 * *m.SecondsPerStepStd / m.SecondsPerStep // about 95 % of steps fall within two standard deviations
	}
	pm = math.Min(math.Max(pm, 0), 1)
	batch := m.BatchSizes
	if len(batch) == 0 && m.BatchSize != nil {
		batch, _ = json.Marshal(map[string]int{"batchSize": *m.BatchSize})
	}
	if len(batch) == 0 {
		batch = json.RawMessage(`{}`)
	}
	overhead := m.LeaseOverheadSeconds
	if overhead != nil && (*overhead < 0 || math.IsInf(*overhead, 0) || math.IsNaN(*overhead)) {
		overhead = nil
	}
	bucket := ""
	if len(m.BucketConfig) > 0 && string(m.BucketConfig) != "null" {
		var str string
		if json.Unmarshal(m.BucketConfig, &str) == nil {
			bucket = str
		} else {
			bucket = cas.Hash(m.BucketConfig)
		}
	}
	if _, err := tx.Exec(ctx, `INSERT INTO calibrations (base_model, card_class, memory_cap_gb, precision, bucket_config,
			seconds_per_step, plus_minus, batch_sizes, lease_overhead_seconds, family, artifact_hash, pipeline_run_id, measured_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $12, $9, $10, NULLIF($11, ''), now())
		ON CONFLICT (base_model, card_class, memory_cap_gb, precision, bucket_config) DO UPDATE SET
			seconds_per_step = excluded.seconds_per_step, plus_minus = excluded.plus_minus, batch_sizes = excluded.batch_sizes,
			lease_overhead_seconds = excluded.lease_overhead_seconds, family = excluded.family, artifact_hash = excluded.artifact_hash, pipeline_run_id = excluded.pipeline_run_id,
			measured_at = excluded.measured_at
		WHERE calibrations.artifact_hash <> excluded.artifact_hash`,
		k.BaseModel, k.CardClass, k.MemoryCapGB, k.Precision, bucket, m.SecondsPerStep, pm, batch, m.Family,
		out.Artifact.Hash, out.PipelineRunID, overhead); err != nil {
		return nil, fmt.Errorf("cache calibration: %w", err)
	}
	return nil, nil
}
