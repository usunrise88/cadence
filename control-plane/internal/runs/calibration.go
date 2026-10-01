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
	"github.com/usunrise88/cadence/control-plane/internal/events"
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

// keyFor finds what a calibration output measured for: the run it belongs to, the runs.calibrate request of its
// pipeline run, or the artifact's own metadata. found is false when none says.
func keyFor(ctx context.Context, tx pgx.Tx, out steps.Output, m calibrationMeta) (Key, bool, error) {
	if out.RunID != "" {
		r, err := getRow(ctx, tx, out.RunID)
		if err == nil {
			base, err := registry.GetVersion(ctx, tx, registry.KindBaseModel, r.BaseVersionID)
			if err != nil {
				return Key{}, false, err
			}
			return Key{BaseModel: base.Name, CardClass: r.Card.CardClass, MemoryCapGB: r.Card.MemoryCapGB, Precision: r.Precision},
				r.Card.CardClass != "", nil
		}
	}
	var k Key
	err := tx.QueryRow(ctx, `SELECT base_model, card_class, memory_cap_gb, precision FROM calibration_requests
		WHERE pipeline_run_id = $1`, out.PipelineRunID).Scan(&k.BaseModel, &k.CardClass, &k.MemoryCapGB, &k.Precision)
	switch {
	case err == nil:
		return k, true, nil
	case !errors.Is(err, pgx.ErrNoRows):
		return Key{}, false, fmt.Errorf("read calibration request: %w", err)
	}
	k = Key{BaseModel: m.BaseModel, CardClass: m.CardClass, MemoryCapGB: m.MemoryCapGB, Precision: m.Precision}
	return k, k.BaseModel != "" && k.CardClass != "" && k.MemoryCapGB > 0 && k.Precision != "", nil
}

// calibrationHook caches a `calibration` output (idempotent per artifact: a reused output writes the same row).
// An output it cannot key or that carries no seconds per step is left alone; the step still succeeds.
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
	k, found, err := keyFor(ctx, tx, out, m)
	if err != nil || !found {
		return nil, err
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
