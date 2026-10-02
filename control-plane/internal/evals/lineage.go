package evals

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/usunrise88/cadence/control-plane/internal/lineage"
	"github.com/usunrise88/cadence/control-plane/internal/storage"
)

// LineageSource answers registry.lineage for evals and eval records (internal/lineage, phase 3): an eval is built
// from its subject, its baseline, its golden sets, the noise banks of its augmentations and the eval records of its
// cells; a record from its model (the checkpoints and model versions with its weights hash, or the base model
// version), its golden set and its normalizer. So a checkpoint, a model, a base model, a golden set or a normalizer
// shows the evals and records that used it ("used by"). Registered models name their eval in their payload
// (evalId), which the registry source already follows.
type LineageSource struct{}

var _ lineage.Source = LineageSource{}

func prefixOf(id string) string {
	p, _, _ := strings.Cut(id, "_")
	return p
}

// Describe implements lineage.Source.
func (LineageSource) Describe(ctx context.Context, q storage.Querier, id string) (lineage.Node, bool, error) {
	n := lineage.Node{ID: id}
	var err error
	switch prefixOf(id) {
	case "evl":
		var label string
		n.Kind = Kind
		err = q.QueryRow(ctx, `SELECT project_id, status, coalesce(subject->>'label', subject_id) FROM evals WHERE id = $1`, id).
			Scan(&n.ProjectID, &n.State, &label)
		n.Label = "eval of " + label
	case "erc":
		var (
			profile string
			wer     *float64
		)
		n.Kind = "eval_record"
		err = q.QueryRow(ctx, `SELECT profile, (summary->>'wer')::float8 FROM eval_records WHERE id = $1`, id).Scan(&profile, &wer)
		n.Label = "eval record at " + profile
		if wer != nil {
			n.Label += fmt.Sprintf(" (WER %.4f)", *wer)
		}
	default:
		return lineage.Node{}, false, nil
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return lineage.Node{}, false, nil
	}
	if err != nil {
		return lineage.Node{}, false, fmt.Errorf("lineage: read %s: %w", id, err)
	}
	return n, true, nil
}

func refRows(ctx context.Context, q storage.Querier, sql string, args ...any) ([]lineage.Ref, error) {
	rows, err := q.Query(ctx, sql, args...)
	if err != nil {
		return nil, fmt.Errorf("lineage: %w", err)
	}
	out, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (lineage.Ref, error) {
		var x lineage.Ref
		return x, r.Scan(&x.ID, &x.Relation)
	})
	if err != nil {
		return nil, fmt.Errorf("lineage: %w", err)
	}
	return out, nil
}

// Upstream implements lineage.Source.
func (LineageSource) Upstream(ctx context.Context, q storage.Querier, id string) ([]lineage.Ref, error) {
	switch prefixOf(id) {
	case "evl":
		var subject, baseline, golden, augs []byte
		err := q.QueryRow(ctx, `SELECT subject, baseline, golden_sets, augmentations FROM evals WHERE id = $1`, id).
			Scan(&subject, &baseline, &golden, &augs)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		if err != nil {
			return nil, fmt.Errorf("lineage: read eval %s: %w", id, err)
		}
		var s, b Model
		var gs []GoldenSet
		var as []Augmentation
		for _, f := range []struct {
			raw []byte
			v   any
		}{{subject, &s}, {baseline, &b}, {golden, &gs}, {augs, &as}} {
			if err := json.Unmarshal(f.raw, f.v); err != nil {
				return nil, fmt.Errorf("lineage: read eval %s: %w", id, err)
			}
		}
		out := []lineage.Ref{{ID: s.ID, Relation: "subject"}, {ID: b.ID, Relation: "baseline"}}
		for _, g := range gs {
			out = append(out, lineage.Ref{ID: g.VersionID, Relation: "golden set"})
		}
		for _, a := range as {
			if a.NoiseBank != "" {
				out = append(out, lineage.Ref{ID: a.NoiseBank, Relation: "noise bank (" + a.Name + ")"})
			}
		}
		recs, err := refRows(ctx, q, `SELECT DISTINCT record_id, 'cell' FROM eval_cells WHERE eval_id = $1 AND record_id IS NOT NULL`, id)
		if err != nil {
			return nil, err
		}
		return append(out, recs...), nil
	case "erc":
		return refRows(ctx, q, `SELECT x.id, x.rel FROM eval_records r, LATERAL (VALUES
				(r.golden_set_version_id, 'golden set'), (r.normalizer_version_id, 'normalizer'),
				(CASE WHEN r.model_key LIKE 'base:%' THEN substr(r.model_key, 6) END, 'model')) AS x(id, rel)
			WHERE r.id = $1 AND x.id IS NOT NULL
			UNION ALL SELECT c.id, 'model' FROM eval_records r JOIN checkpoints c ON c.weights_hash = r.model_key
				WHERE r.id = $1 AND r.model_key <> ''
			UNION ALL SELECT v.id, 'model' FROM eval_records r JOIN registry_versions v ON v.payload->>'weightsHash' = r.model_key
				WHERE r.id = $1 AND r.model_key NOT LIKE 'base:%'`, id)
	}
	return nil, nil
}

// Downstream implements lineage.Source.
func (LineageSource) Downstream(ctx context.Context, q storage.Querier, id string) ([]lineage.Ref, error) {
	switch prefixOf(id) {
	case "ckp":
		return refRows(ctx, q, `SELECT id, 'subject' FROM evals WHERE subject_id = $1
			UNION ALL SELECT r.id, 'model' FROM eval_records r JOIN checkpoints c ON c.weights_hash = r.model_key
				WHERE c.id = $1 AND c.weights_hash <> ''`, id)
	case "ver":
		return refRows(ctx, q, `SELECT id, 'subject' FROM evals WHERE subject_id = $1
			UNION ALL SELECT id, 'baseline' FROM evals WHERE baseline->>'id' = $1
			UNION ALL SELECT id, 'golden set' FROM evals WHERE golden_sets @> jsonb_build_array(jsonb_build_object('versionId', $1::text))
			UNION ALL SELECT id, 'noise bank' FROM evals WHERE augmentations @> jsonb_build_array(jsonb_build_object('noiseBank', $1::text))
			UNION ALL SELECT id, 'golden set' FROM eval_records WHERE golden_set_version_id = $1
			UNION ALL SELECT id, 'normalizer' FROM eval_records WHERE normalizer_version_id = $1
			UNION ALL SELECT id, 'model' FROM eval_records WHERE model_key = 'base:' || $1
			UNION ALL SELECT r.id, 'model' FROM eval_records r JOIN registry_versions v ON v.payload->>'weightsHash' = r.model_key
				WHERE v.id = $1 AND coalesce(v.payload->>'weightsHash', '') <> ''`, id)
	case "erc":
		return refRows(ctx, q, `SELECT DISTINCT eval_id, 'cell' FROM eval_cells WHERE record_id = $1`, id)
	}
	return nil, nil
}
