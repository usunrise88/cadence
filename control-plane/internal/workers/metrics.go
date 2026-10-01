package workers

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"

	"github.com/jackc/pgx/v5"

	"github.com/usunrise88/cadence/control-plane/internal/events"
	"github.com/usunrise88/cadence/control-plane/internal/jobs"
	"github.com/usunrise88/cadence/control-plane/internal/problems"
	"github.com/usunrise88/cadence/control-plane/internal/steps"
	"github.com/usunrise88/cadence/control-plane/internal/telemetry"
)

var metricName = regexp.MustCompile(`^[a-z][a-z0-9_./]{0,99}$`)

// AppendMetrics stores a batch of metric points from the step on lease leaseID; when the step belongs to a training
// run they also go out on run.{runId}.metrics (in events of at most 1000 points).
func (s *Service) AppendMetrics(ctx context.Context, c Caller, leaseID string, pts []telemetry.Point) error {
	for i, p := range pts {
		if !metricName.MatchString(p.Name) || p.WallTime.IsZero() {
			return problems.Validation([]problems.FieldError{{Path: fmt.Sprintf("/points/%d", i), Message: "name (lower case) and wallTime are required"}})
		}
	}
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		l, err := lockLease(ctx, tx, c, leaseID)
		if err != nil {
			return err
		}
		if l.State == LeaseReaped {
			return ended(l)
		}
		var raw []byte
		if err := tx.QueryRow(ctx, "SELECT spec FROM step_jobs WHERE job_id = $1", l.JobID).Scan(&raw); err != nil {
			return fmt.Errorf("read step job: %w", err)
		}
		var spec steps.Spec
		if err := json.Unmarshal(raw, &spec); err != nil {
			return fmt.Errorf("read step spec: %w", err)
		}
		src := telemetry.Source{JobID: l.JobID, RunID: spec.RunID, StepID: spec.StepID, ProjectID: spec.ProjectID}
		if err := telemetry.Insert(ctx, tx, src, pts); err != nil {
			return err
		}
		if spec.RunID == "" {
			return nil
		}
		var drafts []events.Draft
		for start := 0; start < len(pts); start += maxEventPoints {
			chunk := pts[start:min(start+maxEventPoints, len(pts))]
			drafts = append(drafts, events.Draft{
				Topic: "run." + spec.RunID + ".metrics", Type: EventMetrics, ProjectID: spec.ProjectID,
				Payload: map[string]any{"runId": spec.RunID, "jobId": l.JobID, "stepId": spec.StepID, "points": chunk},
			})
		}
		return events.Append(ctx, tx, jobs.System, nil, drafts)
	})
}
