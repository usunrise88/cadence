package deployments

import (
	"context"
	"encoding/json"

	"github.com/usunrise88/cadence/control-plane/internal/problems"
	"github.com/usunrise88/cadence/control-plane/internal/steps"
	"github.com/usunrise88/cadence/control-plane/internal/storage"
	"github.com/usunrise88/cadence/control-plane/internal/transcriptions"
)

// Resolve implements transcriptions.Deployments (R47): a deployment of the project as a lane of a manual test needs
// it — its export's deployable and the staging target that serves it (the deployment's own while it is a shadow,
// else the default staging target: a canary's or production's export is still served on staging for tests).
func (s *Service) Resolve(ctx context.Context, q storage.Querier, projectID, id string) (transcriptions.Deployment, error) {
	d, err := Get(ctx, q, id)
	if err != nil {
		return transcriptions.Deployment{}, err
	}
	if d.ProjectID != projectID {
		return transcriptions.Deployment{}, problems.NotFound.New("the project has no deployment %q", id)
	}
	if !d.Live() {
		return transcriptions.Deployment{}, problems.Conflict.New("deployment %s is %s; it serves nothing", d.ID, d.State)
	}
	dp, err := s.deployedOf(ctx, q, d)
	if err != nil {
		return transcriptions.Deployment{}, err
	}
	staging, err := s.stagingOf(ctx, q, d, dp.model.Family.Name)
	if err != nil {
		return transcriptions.Deployment{}, err
	}
	meta, _ := json.Marshal(map[string]any{"family": dp.model.Family.Name, "format": d.Format, "profile": d.Profile})
	label := dp.label + " · " + d.Stage
	if d.Slot != "" {
		label += " " + d.Slot
	}
	return transcriptions.Deployment{ID: d.ID, ProjectID: d.ProjectID, Label: label, ModelVersionID: d.ModelVersionID,
		BaseModelVersionID: dp.model.Payload.BaseModelVersionID, CheckpointID: dp.model.Payload.CheckpointID, Profile: d.Profile,
		Format: d.Format, Deployable: steps.ArtifactRef{Hash: dp.export.DeployableHash, Type: steps.TypeDeployable, Meta: meta},
		TargetID: staging.ID}, nil
}
