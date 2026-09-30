package runs

import (
	"context"
	"fmt"
	"math"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/usunrise88/cadence/control-plane/internal/storage"
	"github.com/usunrise88/cadence/control-plane/internal/telemetry"
)

// Metric axes (the contract's MetricAxis).
const (
	AxisStep     = "step"
	AxisEpoch    = "epoch"
	AxisWall     = "wall"
	AxisGPUHours = "gpuHours"
)

// DefaultMaxPoints is metrics.get's maxPoints when the request names none (the contract's default).
const DefaultMaxPoints = 1000

// MetricsQuery is a metrics.get request.
type MetricsQuery struct {
	RunID     string
	Names     []string
	X         string
	MaxPoints int
	AfterStep *int64
}

// Bin is one point of a series, or the bucket of points a long series was binned into (R53: chart data is
// contract data — the browser gets the same numbers an agent does).
type Bin struct {
	X        float64   `json:"x"`
	Value    float64   `json:"value"`
	Min      float64   `json:"min"`
	Max      float64   `json:"max"`
	Count    int       `json:"count"`
	Step     *int64    `json:"step,omitempty"`
	Epoch    *float64  `json:"epoch,omitempty"`
	WallTime time.Time `json:"wallTime"`
}

// SeriesView is one metric's series.
type SeriesView struct {
	Name   string `json:"name"`
	Total  int    `json:"total"`
	Binned bool   `json:"binned"`
	Points []Bin  `json:"points"`
}

// CheckpointMark is a checkpoint drawn on a chart.
type CheckpointMark struct {
	ID     string   `json:"id"`
	Step   *int64   `json:"step,omitempty"`
	ValWER *float64 `json:"valWer,omitempty"`
	Kind   string   `json:"kind,omitempty"`
	Kept   bool     `json:"kept"`
}

// SeriesSet is metrics.get's answer (the contract's MetricSeriesSet).
type SeriesSet struct {
	RunID       string           `json:"runId"`
	X           string           `json:"x"`
	MaxPoints   int              `json:"maxPoints"`
	LastStep    *int64           `json:"lastStep,omitempty"`
	Series      []SeriesView     `json:"series"`
	Checkpoints []CheckpointMark `json:"checkpoints"`
}

// Metrics reads a run's series from internal/telemetry and bins each to at most MaxPoints on the chosen axis.
func Metrics(ctx context.Context, q storage.Querier, mq MetricsQuery) (SeriesSet, error) {
	if mq.X == "" {
		mq.X = AxisStep
	}
	if mq.MaxPoints <= 0 {
		mq.MaxPoints = DefaultMaxPoints
	}
	if _, err := getRow(ctx, q, mq.RunID); err != nil {
		return SeriesSet{}, err
	}
	series, err := telemetry.Get(ctx, q, telemetry.Query{RunID: mq.RunID, Names: mq.Names, AfterStep: mq.AfterStep})
	if err != nil {
		return SeriesSet{}, err
	}
	out := SeriesSet{RunID: mq.RunID, X: mq.X, MaxPoints: mq.MaxPoints, Series: []SeriesView{}, Checkpoints: []CheckpointMark{}}
	var origin time.Time
	var gpu gpuClock
	switch mq.X {
	case AxisWall, AxisGPUHours:
		if origin, err = firstPoint(ctx, q, mq.RunID); err != nil {
			return SeriesSet{}, err
		}
		if mq.X == AxisGPUHours {
			if gpu, err = gpuIntervals(ctx, q, mq.RunID); err != nil {
				return SeriesSet{}, err
			}
		}
	}
	for _, s := range series {
		var xs []float64
		var pts []telemetry.Point
		for _, p := range s.Points {
			x, ok := axis(mq.X, p, origin, gpu)
			if !ok {
				continue
			}
			xs, pts = append(xs, x), append(pts, p)
			if p.Step != nil && (out.LastStep == nil || *p.Step > *out.LastStep) {
				st := *p.Step
				out.LastStep = &st
			}
		}
		bins, binned := bin(xs, pts, mq.MaxPoints)
		out.Series = append(out.Series, SeriesView{Name: s.Name, Total: len(pts), Binned: binned, Points: bins})
	}
	ckps, err := ListCheckpoints(ctx, q, CheckpointFilter{RunID: mq.RunID, Limit: 500})
	if err != nil {
		return SeriesSet{}, err
	}
	sort.SliceStable(ckps, func(i, j int) bool { return ckps[i].CreatedAt.Before(ckps[j].CreatedAt) })
	for _, c := range ckps {
		out.Checkpoints = append(out.Checkpoints, CheckpointMark{ID: c.ID, Step: c.Step, ValWER: c.ValWER, Kind: c.Kind, Kept: c.Kept})
	}
	return out, nil
}

// axis is a point's x on axis a; ok is false when the point has no value there (no step, no epoch).
func axis(a string, p telemetry.Point, origin time.Time, gpu gpuClock) (float64, bool) {
	switch a {
	case AxisEpoch:
		if p.Epoch == nil {
			return 0, false
		}
		return *p.Epoch, true
	case AxisWall:
		return p.WallTime.Sub(origin).Seconds(), true
	case AxisGPUHours:
		return gpu.at(p.WallTime, origin), true
	default:
		if p.Step == nil {
			return 0, false
		}
		return float64(*p.Step), true
	}
}

// bin keeps a series of at most limit points as it is; a longer one is cut into limit equal buckets of x, each
// answering its points' mean value with their min and max, the mean x, and the last step, epoch and wall time.
func bin(xs []float64, pts []telemetry.Point, limit int) ([]Bin, bool) {
	out := []Bin{}
	if len(pts) <= limit {
		for i, p := range pts {
			out = append(out, Bin{X: xs[i], Value: p.Value, Min: p.Value, Max: p.Value, Count: 1, Step: p.Step, Epoch: p.Epoch, WallTime: p.WallTime})
		}
		return out, false
	}
	lo, hi := xs[0], xs[0]
	for _, x := range xs {
		lo, hi = math.Min(lo, x), math.Max(hi, x)
	}
	width := (hi - lo) / float64(limit)
	type acc struct {
		sumX, sum, min, max float64
		n                   int
		last                telemetry.Point
	}
	buckets := make([]*acc, limit)
	for i, p := range pts {
		b := 0
		if width > 0 {
			b = min(limit-1, int((xs[i]-lo)/width))
		}
		a := buckets[b]
		if a == nil {
			a = &acc{min: p.Value, max: p.Value}
			buckets[b] = a
		}
		a.sumX += xs[i]
		a.sum += p.Value
		a.min, a.max = math.Min(a.min, p.Value), math.Max(a.max, p.Value)
		a.n++
		if a.last.WallTime.IsZero() || !p.WallTime.Before(a.last.WallTime) {
			a.last = p
		}
	}
	for _, a := range buckets {
		if a == nil {
			continue
		}
		out = append(out, Bin{X: a.sumX / float64(a.n), Value: a.sum / float64(a.n), Min: a.min, Max: a.max, Count: a.n,
			Step: a.last.Step, Epoch: a.last.Epoch, WallTime: a.last.WallTime})
	}
	return out, true
}

// firstPoint is the wall time of a run's first metric point (the origin of the wall axis).
func firstPoint(ctx context.Context, q storage.Querier, runID string) (time.Time, error) {
	var t *time.Time
	if err := q.QueryRow(ctx, "SELECT min(wall_time) FROM metric_points WHERE run_id = $1", runID).Scan(&t); err != nil {
		return time.Time{}, fmt.Errorf("read first metric point: %w", err)
	}
	if t == nil {
		return time.Time{}, nil
	}
	return *t, nil
}

// gpuClock is the run's leases on GPU cards: the GPU-hours at a wall time are the lease time before it.
type gpuClock []struct{ from, to time.Time }

func gpuIntervals(ctx context.Context, q storage.Querier, runID string) (gpuClock, error) {
	rows, err := q.Query(ctx, `SELECT l.created_at, coalesce(l.ended_at, now()) FROM leases l JOIN step_jobs s ON s.job_id = l.job_id
		WHERE l.card_index IS NOT NULL AND s.spec->>'runId' = $1 ORDER BY l.created_at`, runID)
	if err != nil {
		return nil, fmt.Errorf("read run leases: %w", err)
	}
	var out gpuClock
	var from, to time.Time
	if _, err := pgx.ForEachRow(rows, []any{&from, &to}, func() error {
		out = append(out, struct{ from, to time.Time }{from, to})
		return nil
	}); err != nil {
		return nil, fmt.Errorf("read run leases: %w", err)
	}
	return out, nil
}

// at is the GPU-hours used before t; without GPU leases (a CPU pack) the wall hours since origin stand in.
func (g gpuClock) at(t, origin time.Time) float64 {
	if len(g) == 0 {
		return math.Max(0, t.Sub(origin).Hours())
	}
	var h float64
	for _, iv := range g {
		if t.After(iv.from) {
			end := iv.to
			if t.Before(end) {
				end = t
			}
			h += end.Sub(iv.from).Hours()
		}
	}
	return h
}
