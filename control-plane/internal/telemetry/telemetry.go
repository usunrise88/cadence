// Package telemetry stores the metric points workers post (loss, validation WER, learning rate, throughput, card
// memory) in the metric_points table and reads them back as series (docs/spec/08-resolutions.md R15: thousands of
// points per run need no TSDB). The worker protocol writes (internal/workers); metrics.get reads through Get.
package telemetry

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/usunrise88/cadence/control-plane/internal/storage"
)

// Point is one metric value. Its JSON form is the contract's MetricPoint.
type Point struct {
	Name     string    `json:"name"`
	Step     *int64    `json:"step,omitempty"`
	Epoch    *float64  `json:"epoch,omitempty"`
	Value    float64   `json:"value"`
	WallTime time.Time `json:"wallTime"`
}

// Source is the job a batch of points comes from.
type Source struct {
	JobID     string
	RunID     string // the training run, when the step belongs to one
	StepID    string // the pipeline step
	ProjectID string
}

// Insert stores pts from src inside tx.
func Insert(ctx context.Context, tx pgx.Tx, src Source, pts []Point) error {
	if len(pts) == 0 {
		return nil
	}
	rows := make([][]any, 0, len(pts))
	for _, p := range pts {
		rows = append(rows, []any{src.JobID, nullable(src.RunID), nullable(src.StepID), nullable(src.ProjectID), p.Name,
			p.Step, p.Epoch, p.Value, p.WallTime})
	}
	_, err := tx.CopyFrom(ctx, pgx.Identifier{"metric_points"},
		[]string{"job_id", "run_id", "step_id", "project_id", "name", "step", "epoch", "value", "wall_time"}, pgx.CopyFromRows(rows))
	if err != nil {
		return fmt.Errorf("store metric points: %w", err)
	}
	return nil
}

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// Query selects points: by run or by job (one is required), optionally some names, after an optimiser step, and
// thinned to at most MaxPoints per series.
type Query struct {
	RunID     string
	JobID     string
	Names     []string
	AfterStep *int64
	// MaxPoints thins each series evenly to at most this many points, keeping the first and the last (0: all).
	MaxPoints int
}

// Series is the points of one metric in step order (wall time where steps are absent).
type Series struct {
	Name   string  `json:"name"`
	Points []Point `json:"points"`
	// Total is how many points the series has before thinning.
	Total int `json:"total"`
}

// Get reads series for q, by name.
func Get(ctx context.Context, q storage.Querier, qu Query) ([]Series, error) {
	if (qu.RunID == "") == (qu.JobID == "") {
		return nil, fmt.Errorf("telemetry: query by run or by job")
	}
	rows, err := q.Query(ctx, `SELECT name, step, epoch, value, wall_time FROM metric_points
		WHERE ($1 = '' OR run_id = $1) AND ($2 = '' OR job_id = $2)
		  AND (coalesce(cardinality($3::text[]), 0) = 0 OR name = ANY($3)) AND ($4::bigint IS NULL OR step > $4)
		ORDER BY name, step NULLS LAST, wall_time, id`, qu.RunID, qu.JobID, qu.Names, qu.AfterStep)
	if err != nil {
		return nil, fmt.Errorf("query metric points: %w", err)
	}
	pts, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (Point, error) {
		var p Point
		err := row.Scan(&p.Name, &p.Step, &p.Epoch, &p.Value, &p.WallTime)
		return p, err
	})
	if err != nil {
		return nil, fmt.Errorf("read metric points: %w", err)
	}
	byName := map[string][]Point{}
	for _, p := range pts {
		byName[p.Name] = append(byName[p.Name], p)
	}
	out := make([]Series, 0, len(byName))
	for name, list := range byName {
		out = append(out, Series{Name: name, Points: Thin(list, qu.MaxPoints), Total: len(list)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// Thin keeps at most limit points of pts evenly spread, always keeping the first and the last (limit ≤ 0: all).
func Thin(pts []Point, limit int) []Point {
	if limit <= 0 || len(pts) <= limit {
		return pts
	}
	if limit == 1 {
		return pts[len(pts)-1:]
	}
	out := make([]Point, 0, limit)
	step := float64(len(pts)-1) / float64(limit-1)
	for i := range limit {
		out = append(out, pts[int(float64(i)*step+0.5)])
	}
	return out
}
