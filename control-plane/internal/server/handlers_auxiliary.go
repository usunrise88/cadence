package server

import (
	"context"

	"github.com/usunrise88/cadence/control-plane/internal/api"
	"github.com/usunrise88/cadence/control-plane/internal/auth"
	"github.com/usunrise88/cadence/control-plane/internal/projects"
	"github.com/usunrise88/cadence/control-plane/internal/registry"
	"github.com/usunrise88/cadence/control-plane/internal/storage"
	"github.com/usunrise88/cadence/control-plane/internal/triage"
)

// Auxiliary models and the triage queue (phase 4 · stream X). Auxiliary versions are registry data read here (their
// adoption is projects.adopt, gated in handlers_registry.go); the triage queue of disputed pseudo-labels is project
// work, filled by the segments output hook (internal/triage); the Triage panel with its verbs arrives with annotation.

func auxiliaryVersions(ctx context.Context, q storage.Querier, f registry.Filter) ([]api.AuxiliaryVersion, error) {
	common, payloads, used, err := versions[api.AuxiliaryPayload](ctx, q, f)
	if err != nil {
		return nil, err
	}
	out := make([]api.AuxiliaryVersion, 0, len(common))
	for i, v := range common {
		out = append(out, api.AuxiliaryVersion{
			Actor: v.Actor, CollectionId: v.CollectionId, CreatedAt: v.CreatedAt, Fingerprint: v.Fingerprint, Id: v.Id,
			Kind: v.Kind, Licence: v.Licence, Name: v.Name, State: v.State, Tags: v.Tags, UpdatedAt: v.UpdatedAt,
			Version: v.Version, Auxiliary: payloads[i], UsedBy: used[i],
		})
	}
	return out, nil
}

// AuxiliariesList implements auxiliaries.list.
func (s *Server) AuxiliariesList(ctx context.Context, req api.AuxiliariesListRequestObject) (api.AuxiliariesListResponseObject, error) {
	if err := auth.CheckRegistryRead(ctx); err != nil {
		return nil, err
	}
	items, err := auxiliaryVersions(ctx, s.Pool, versionFilter(registry.KindAuxiliary, req.Params.Collection, req.Params.State))
	if err != nil {
		return nil, err
	}
	return api.AuxiliariesList200JSONResponse{Items: items}, nil
}

// AuxiliariesGet implements auxiliaries.get.
func (s *Server) AuxiliariesGet(ctx context.Context, req api.AuxiliariesGetRequestObject) (api.AuxiliariesGetResponseObject, error) {
	if err := auth.CheckRegistryRead(ctx); err != nil {
		return nil, err
	}
	items, err := auxiliaryVersions(ctx, s.Pool, registry.Filter{Kind: registry.KindAuxiliary, IDs: []string{req.Id}})
	if err != nil {
		return nil, err
	}
	v, err := one(items, registry.KindAuxiliary, req.Id)
	if err != nil {
		return nil, err
	}
	// A service auxiliary says whether it answers now, as a pipeline's dry run would probe it (Cadence never starts
	// it; the "Adapt a new language" playbook waits on this while a person starts it).
	if pr := s.Pipelines.Prober(); pr != nil && v.Auxiliary.Service != nil {
		reachable := pr.Probe(ctx, v.Auxiliary.Service.Endpoint) == nil
		v.Reachable = &reachable
	}
	return api.AuxiliariesGet200JSONResponse(v), nil
}

// TriageList implements triage.list.
func (s *Server) TriageList(ctx context.Context, req api.TriageListRequestObject) (api.TriageListResponseObject, error) {
	p, err := projects.Get(ctx, s.Pool, req.P)
	if err != nil {
		return nil, err
	}
	if err := auth.CheckProject(ctx, p.ID); err != nil {
		return nil, err
	}
	f := triage.Filter{PipelineRunID: deref(req.Params.PipelineRun), Limit: deref(req.Params.Limit)}
	if req.Params.State != nil {
		f.State = string(*req.Params.State)
	}
	if req.Params.Reason != nil {
		f.Reason = string(*req.Params.Reason)
	}
	list, err := triage.List(ctx, s.Pool, p.ID, f)
	if err != nil {
		return nil, err
	}
	items, err := convert[[]api.TriageItem](list)
	if err != nil {
		return nil, err
	}
	if items == nil {
		items = []api.TriageItem{}
	}
	return api.TriageList200JSONResponse{Items: items}, nil
}
