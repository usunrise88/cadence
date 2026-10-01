package workers

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/usunrise88/cadence/control-plane/internal/events"
	"github.com/usunrise88/cadence/control-plane/internal/jobs"
	"github.com/usunrise88/cadence/control-plane/internal/problems"
	"github.com/usunrise88/cadence/control-plane/internal/steps"
)

// Published is an intermediate output a running step publishes during its lease (the contract's WorkerOutput): a
// validation checkpoint, say, which must not wait for the release (a pause, a window close or a failure would lose
// it).
type Published struct {
	Name     string             `json:"name"`
	Artifact steps.ArtifactRef  `json:"artifact"`
	Metrics  map[string]float64 `json:"metrics,omitempty"`
}

// PublishHook records a published output of the step job jobID inside the publishing transaction and returns the
// events to emit with it (pipelines.Engine.Published records the artifact and runs the output hooks).
type PublishHook func(ctx context.Context, tx pgx.Tx, jobID string, spec steps.Spec, out Published) ([]events.Draft, error)

// OnPublished sets the hook workerOutputs.new runs.
func (s *Service) OnPublished(fn PublishHook) { s.onPublished = fn }

// Publish records an intermediate output of the active lease leaseID: the artifact must be in the content store and
// name one of the step's outputs with that output's type. The hook runs in this request's own transaction, so a
// published output stays recorded whatever happens to the lease afterwards; publishing a hash again is a no-op as
// far as the hooks go (they are idempotent per hash).
func (s *Service) Publish(ctx context.Context, c Caller, leaseID string, in Published) error {
	if in.Name == "" {
		return problems.Validation([]problems.FieldError{{Path: "/name", Message: "the step output this is an instance of"}})
	}
	if !steps.ValidHash(in.Artifact.Hash) {
		return problems.Validation([]problems.FieldError{{Path: "/artifact/hash", Message: "an artifact hash is b3:<64 hex>"}})
	}
	if s.cas != nil {
		ok, _, err := s.cas.Has(in.Artifact.Hash)
		if err != nil {
			return fmt.Errorf("look up output %s: %w", in.Name, err)
		}
		if !ok {
			return problems.ArtifactMissing.New("output %q (%s) is not in the content store; write it (or upload it with workerArtifacts.set) before publishing", in.Name, in.Artifact.Hash)
		}
	}
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		l, err := lockLease(ctx, tx, c, leaseID)
		if err != nil {
			return err
		}
		if l.State != LeaseActive {
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
		typ, ok := spec.Outputs[in.Name]
		switch {
		case !ok:
			return problems.Validation([]problems.FieldError{{Path: "/name", Message: fmt.Sprintf("%s has no output %q", spec.KindRef(), in.Name)}})
		case in.Artifact.Type != typ:
			return problems.Validation([]problems.FieldError{{Path: "/artifact/type", Message: fmt.Sprintf("output %q is a %s", in.Name, typ)}})
		}
		if s.onPublished == nil {
			return nil
		}
		drafts, err := s.onPublished(ctx, tx, l.JobID, spec, in)
		if err != nil {
			return err
		}
		return events.Append(ctx, tx, jobs.System, nil, drafts)
	})
}
