package evals

import (
	"context"

	"github.com/usunrise88/cadence/control-plane/internal/projects"
	"github.com/usunrise88/cadence/control-plane/internal/registry"
	"github.com/usunrise88/cadence/control-plane/internal/runs"
	"github.com/usunrise88/cadence/control-plane/internal/steps"
	"github.com/usunrise88/cadence/control-plane/internal/storage"
)

// Weights is a model version or a base model as a decoder outside an eval needs it (phase 5 · stream D4: the
// comparison side of a shadow replay): its family and the artifact its transcribe role reads — a checkpoint, or the
// rendered base_model artifact a base model's materialize role turns into one.
type Weights struct {
	Kind      string // model | base_model
	VersionID string
	Label     string
	Family    runs.Family
	Artifact  steps.ArtifactRef
	// Source says where a baseline came from: request, alias or project-default.
	Source string
}

func weightsOf(m Model) Weights {
	return Weights{Kind: m.Kind, VersionID: m.ID, Label: m.Label, Family: m.family.Family, Artifact: m.artifact, Source: m.Source}
}

// ResolveWeights resolves ref as an eval names a baseline (ver_…, @alias, model/<name> or a base model's name), or
// with ref empty the project's @baseline, else its default base model.
func (s *Service) ResolveWeights(ctx context.Context, q storage.Querier, projectID, ref string) (Weights, error) {
	p, err := projects.GetByID(ctx, q, projectID)
	if err != nil {
		return Weights{}, err
	}
	m, err := s.resolveBaseline(ctx, q, p, ref)
	if err != nil {
		return Weights{}, err
	}
	return weightsOf(m), nil
}

// WeightsOf answers the weights of one registry version (a model version or a base model).
func (s *Service) WeightsOf(ctx context.Context, q storage.Querier, v registry.Version) (Weights, error) {
	m, err := s.versionModel(ctx, q, v)
	if err != nil {
		return Weights{}, err
	}
	return weightsOf(m), nil
}
