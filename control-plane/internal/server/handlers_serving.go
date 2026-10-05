package server

import (
	"context"

	"github.com/usunrise88/cadence/control-plane/internal/api"
	"github.com/usunrise88/cadence/control-plane/internal/serving"
	"github.com/usunrise88/cadence/control-plane/internal/storage"
)

// Staging serving (phase 5 · stream D2; docs/spec/06-platform.md "Staging serving"): the staging target's health
// check, the served models' lease counts and the grant hook that names a served step's target. Deployment targets
// carry their health and served models on read.

// newServing wires staging serving: a lease that serves a model gets its target's endpoint and is counted.
func (s *Server) newServing() *serving.Service {
	sv := &serving.Service{Pool: s.Pool, Defaults: s.defaultsDoc, Log: s.Log}
	if s.Workers != nil {
		s.Workers.OnGranted(sv.Granted)
	}
	return sv
}

// CheckServing checks the staging targets' servers and unloads idle models (periodic, serving.health_check_seconds).
func (s *Server) CheckServing(ctx context.Context) error { return s.serving.Tick(ctx) }

// Serving is the staging serving service (tests drive its clock and HTTP client).
func (s *Server) Serving() *serving.Service { return s.serving }

// withServing adds each staging target's health and served models to targets as q sees them.
func (s *Server) withServing(ctx context.Context, q storage.Querier, ts []api.DeploymentTarget) error {
	var ids []string
	for _, t := range ts {
		if t.Kind == api.DeploymentTargetKindStaging {
			ids = append(ids, t.Id)
		}
	}
	if len(ids) == 0 {
		return nil
	}
	health, err := serving.HealthOf(ctx, q, ids)
	if err != nil {
		return err
	}
	models, err := s.serving.Models(ctx, q, ids)
	if err != nil {
		return err
	}
	for i := range ts {
		if ts[i].Kind != api.DeploymentTargetKindStaging {
			continue
		}
		var h api.ServingHealth
		if err := recode(health[ts[i].Id], &h); err != nil {
			return err
		}
		ms := []api.ServedModel{}
		if list := models[ts[i].Id]; len(list) > 0 {
			if err := recode(list, &ms); err != nil {
				return err
			}
		}
		ts[i].Health, ts[i].ServedModels = &h, &ms
	}
	return nil
}
