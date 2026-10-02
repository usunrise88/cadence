package evals

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/usunrise88/cadence/control-plane/internal/events"
	"github.com/usunrise88/cadence/control-plane/internal/steps"
	"github.com/usunrise88/cadence/control-plane/internal/storage"
)

// metricSummary is the part of a metric_scores summary.json the control plane reads; the whole document is kept.
type metricSummary struct {
	Schema    string `json:"schema"`
	Scorer    string `json:"scorer"`
	Metric    string `json:"metric"`
	Available bool   `json:"available"`
}

// readMetricSummary reads and checks summary.json of a metric_scores artifact.
func (s *Service) readMetricSummary(hash string) (json.RawMessage, metricSummary, error) {
	f, err := openFile(s.CAS, hash, SummaryFile)
	if err != nil {
		return nil, metricSummary{}, err
	}
	defer func() { _ = f.Close() }()
	b, err := io.ReadAll(io.LimitReader(f, maxSummary+1))
	if err != nil {
		return nil, metricSummary{}, fmt.Errorf("read %s: %w", SummaryFile, err)
	}
	if len(b) > maxSummary {
		return nil, metricSummary{}, fmt.Errorf("%s of %s is larger than %d bytes", SummaryFile, hash, maxSummary)
	}
	var m metricSummary
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, metricSummary{}, fmt.Errorf("%s of %s: %w", SummaryFile, hash, err)
	}
	if m.Schema != MetricScoresFormat {
		return nil, metricSummary{}, fmt.Errorf("%s of %s has schema %q, not %s", SummaryFile, hash, m.Schema, MetricScoresFormat)
	}
	return b, m, nil
}

// metricsHook records the metric_scores output of an eval's metric step (entity accuracy, latency to final) beside
// the eval record of the cells that planned it: one eval_metrics row per record key, scorer and configuration,
// shared by every project like the records (idempotent). Outside an eval it records nothing.
func (s *Service) metricsHook(ctx context.Context, tx pgx.Tx, out steps.Output) ([]events.Draft, error) {
	if !strings.HasPrefix(out.RunID, "evl_") {
		return nil, nil
	}
	e, found, err := getEval(ctx, tx, out.RunID, "")
	if err != nil || !found {
		return nil, err
	}
	var step string
	if err := tx.QueryRow(ctx, "SELECT step FROM pipeline_steps WHERE id = $1", out.StepID).Scan(&step); err != nil {
		return nil, fmt.Errorf("read step %s: %w", out.StepID, err)
	}
	cells, err := cellsOf(ctx, tx, e.ID)
	if err != nil {
		return nil, err
	}
	var (
		cell   *Cell
		metric string
		plan   MetricPlan
	)
	for i := range cells {
		for m, mp := range cells[i].Metrics {
			if mp.Step == step {
				cell, metric, plan = &cells[i], m, mp
			}
		}
	}
	if cell == nil {
		return nil, nil
	}
	summary, sum, err := s.readMetricSummary(out.Artifact.Hash)
	if err != nil {
		return nil, err
	}
	if sum.Scorer != "" && sum.Scorer != plan.Scorer {
		return nil, fmt.Errorf("the metric scores of step %s say scorer %s, the cell expects %s", step, sum.Scorer, plan.Scorer)
	}
	_, err = tx.Exec(ctx, `INSERT INTO eval_metrics (id, model_key, golden_set_version_id, decoding_hash, scorer, config, metric, scores_hash,
			summary, project_id, eval_id, pipeline_run_id, step_id)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
		ON CONFLICT (model_key, golden_set_version_id, decoding_hash, scorer, config) DO NOTHING`,
		"erm_"+uuid.Must(uuid.NewV7()).String(), cell.ModelKey, cell.GoldenSetVersionID, cell.DecodingHash, plan.Scorer, plan.Config, metric,
		out.Artifact.Hash, summary, e.ProjectID, e.ID, out.PipelineRunID, out.StepID)
	if err != nil {
		return nil, fmt.Errorf("insert eval metric: %w", err)
	}
	return nil, nil
}

// MetricsView is a cell's metrics beside WER (the contract's EvalCellMetrics).
type MetricsView struct {
	Entities    json.RawMessage     `json:"entities,omitempty"`
	Latency     json.RawMessage     `json:"latency,omitempty"`
	Unavailable []MetricUnavailable `json:"unavailable,omitempty"`
}

// MetricUnavailable says why a cell has no value for a metric.
type MetricUnavailable struct {
	Metric string `json:"metric"`
	Reason string `json:"reason"`
}

// metricsOf reads the metrics a cell planned: the stored summaries (with their artifact as scores), and the
// reasons of the unavailable ones. nil when the cell planned none (an eval from before the metrics existed).
func metricsOf(ctx context.Context, q storage.Querier, c Cell) (*MetricsView, error) {
	if len(c.Metrics) == 0 {
		return nil, nil
	}
	v := &MetricsView{}
	for _, metric := range []string{MetricEntities, MetricLatency} {
		mp, ok := c.Metrics[metric]
		if !ok {
			continue
		}
		if mp.Unavailable != "" {
			v.Unavailable = append(v.Unavailable, MetricUnavailable{Metric: metric, Reason: mp.Unavailable})
			continue
		}
		_, summary, scores, found, err := findMetric(ctx, q, metricKey{c.ModelKey, c.GoldenSetVersionID, c.DecodingHash, mp.Scorer, mp.Config})
		if err != nil {
			return nil, err
		}
		if !found {
			continue // its step has not finished
		}
		doc := map[string]any{}
		if json.Unmarshal(summary, &doc) != nil {
			continue
		}
		doc["scores"] = scores
		delete(doc, "schema")
		if metric == MetricEntities {
			v.Entities = mustJSON(doc)
		} else {
			v.Latency = mustJSON(doc)
		}
	}
	return v, nil
}

// RobustnessRow is one cell of the robustness matrix (the contract's EvalRobustnessRow).
type RobustnessRow struct {
	Role               string   `json:"role"`
	GoldenSetVersionID string   `json:"goldenSetVersionId"`
	Profile            string   `json:"profile"`
	DecodingIndex      int      `json:"decodingIndex"`
	AugmentationIndex  int      `json:"augmentationIndex"`
	CellID             string   `json:"cellId"`
	WER                *float64 `json:"wer,omitempty"`
	WERNone            *float64 `json:"werNone,omitempty"`
	Degradation        *float64 `json:"degradation,omitempty"`
}

// robustness is the matrix golden set × augmentation × latency profile: every augmented cell's WER against the same
// cell (role, golden set, profile, decoding) without augmentation. Empty when the eval has no augmentation.
func robustness(cells []Cell, recs map[string]Record) []RobustnessRow {
	wer := func(c Cell) *float64 {
		var sm Summary
		if r, ok := recs[c.RecordID]; ok && json.Unmarshal(r.Summary, &sm) == nil {
			w := sm.WER
			return &w
		}
		return nil
	}
	var out []RobustnessRow
	for _, c := range cells {
		if c.AugmentationIndex == 0 {
			continue
		}
		row := RobustnessRow{Role: c.Role, GoldenSetVersionID: c.GoldenSetVersionID, Profile: c.Profile, DecodingIndex: c.DecodingIndex,
			AugmentationIndex: c.AugmentationIndex, CellID: c.ID, WER: wer(c)}
		for _, o := range cells {
			if o.AugmentationIndex == 0 && o.Role == c.Role && o.GoldenSetVersionID == c.GoldenSetVersionID && o.Profile == c.Profile &&
				o.DecodingIndex == c.DecodingIndex {
				row.WERNone = wer(o)
			}
		}
		if row.WER != nil && row.WERNone != nil {
			d := round6(*row.WER - *row.WERNone)
			row.Degradation = &d
		}
		out = append(out, row)
	}
	return out
}
