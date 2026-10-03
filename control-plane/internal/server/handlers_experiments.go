package server

import (
	"context"
	"net/http"

	"github.com/jackc/pgx/v5"

	"github.com/usunrise88/cadence/control-plane/internal/api"
	"github.com/usunrise88/cadence/control-plane/internal/auth"
	"github.com/usunrise88/cadence/control-plane/internal/commands"
	"github.com/usunrise88/cadence/control-plane/internal/events"
	"github.com/usunrise88/cadence/control-plane/internal/experiments"
	"github.com/usunrise88/cadence/control-plane/internal/registry"
)

// Experiments and sweeps (phase 3 · wave 2 · stream X): experiments.new|get|list and sweeps.run. The domain lives
// in internal/experiments; runs.new's experiment field and runs.list's filter are in handlers_runs.go. sweeps.run
// plans once outside the command so the policy weighs the sweep's whole estimate against the GPU budgets (gpu-spend),
// then plans again inside the command's transaction.

func (c commandResponse) VisitExperimentsNewResponse(w http.ResponseWriter) error { return c.write(w) }
func (c commandResponse) VisitSweepsRunResponse(w http.ResponseWriter) error      { return c.write(w) }

// newExperimentsService wires the experiments domain to the runs service; Install makes run changes drive sweeps.
func (s *Server) newExperimentsService() *experiments.Service {
	return &experiments.Service{Pool: s.Pool, Runs: s.runs, Defaults: s.defaultsDoc,
		RenderVersion: func(v registry.Version) any { return apiVersion(v) }}
}

// scopedExperiment reads the project of experiment id and checks the request's scope reaches it.
func (s *Server) scopedExperiment(ctx context.Context, id string) (string, error) {
	projectID, err := experiments.ProjectOf(ctx, s.Pool, id)
	if err != nil {
		return "", err
	}
	return projectID, auth.CheckProject(ctx, projectID)
}

// ExperimentsNew implements experiments.new.
func (s *Server) ExperimentsNew(ctx context.Context, req api.ExperimentsNewRequestObject) (api.ExperimentsNewResponseObject, error) {
	p, err := scopedProject(ctx, s.Pool, req.P)
	if err != nil {
		return nil, err
	}
	ctx = commands.WithProject(ctx, p.ID)
	b := req.Body
	cmd := command(ctx, "experiments.new", req.Params.IdempotencyKey, req.Params.DryRun)
	return s.run(ctx, cmd, func(ctx context.Context, tx pgx.Tx) (commands.Result, []events.Draft, error) {
		in := experiments.NewInput{ProjectID: p.ID, Name: b.Name, Question: b.Question, Mix: b.Mix, MixRevision: deref(b.MixRevision),
			BaseModel: deref(b.BaseModel), Tag: deref(b.Tag), Actor: cmd.Actor}
		pe, err := s.experiments.Prepare(ctx, tx, in)
		if err != nil {
			return commands.Result{}, nil, err
		}
		if cmd.DryRun {
			v, err := s.experiments.Preview(ctx, tx, pe)
			return commands.Result{Status: http.StatusOK, Body: v}, nil, err
		}
		v, drafts, err := s.experiments.Create(ctx, tx, pe)
		if err != nil {
			return commands.Result{}, nil, err
		}
		return commands.Result{Status: http.StatusCreated, Body: v, ETag: commands.ETag(v.Rev)}, drafts, nil
	})
}

// ExperimentsList implements experiments.list.
func (s *Server) ExperimentsList(ctx context.Context, req api.ExperimentsListRequestObject) (api.ExperimentsListResponseObject, error) {
	p, err := scopedProject(ctx, s.Pool, req.P)
	if err != nil {
		return nil, err
	}
	list, err := s.experiments.List(ctx, s.Pool, p.ID, deref(req.Params.Limit))
	if err != nil {
		return nil, err
	}
	out, err := convert[api.ExperimentList](map[string]any{"items": list})
	if err != nil {
		return nil, err
	}
	return api.ExperimentsList200JSONResponse(out), nil
}

// ExperimentsGet implements experiments.get: the comparison of the experiment's runs.
func (s *Server) ExperimentsGet(ctx context.Context, req api.ExperimentsGetRequestObject) (api.ExperimentsGetResponseObject, error) {
	if _, err := s.scopedExperiment(ctx, req.Id); err != nil {
		return nil, err
	}
	v, err := s.experiments.Get(ctx, s.Pool, req.Id)
	if err != nil {
		return nil, err
	}
	out, err := convert[api.Experiment](v)
	if err != nil {
		return nil, err
	}
	etag := commands.ETag(v.Rev)
	return api.ExperimentsGet200JSONResponse{Body: out, Headers: api.ExperimentsGet200ResponseHeaders{ETag: &etag}}, nil
}

// SweepsRun implements sweeps.run: the dry run answers the points and the estimate against the cap; the real call
// writes the sweep and starts its first run (201), or waits for an approval when the policy says so (202).
func (s *Server) SweepsRun(ctx context.Context, req api.SweepsRunRequestObject) (api.SweepsRunResponseObject, error) {
	rev, err := commands.ParseIfMatch(req.Params.IfMatch)
	if err != nil {
		return nil, err
	}
	projectID, err := s.scopedExperiment(ctx, req.Id)
	if err != nil {
		return nil, err
	}
	ctx = commands.WithProject(ctx, projectID)
	b := req.Body
	actor, _ := auth.FromContext(ctx)
	in := experiments.SweepInput{ExperimentID: req.Id, Rev: rev, Runs: deref(b.Runs), Cap: deref(b.GpuHourCap), Seed: b.Seed,
		Steps: b.Steps, Priority: deref(b.Priority), Actor: actor}
	if b.Mode != nil {
		in.Mode = string(*b.Mode)
	}
	for _, prm := range b.Parameters {
		p := experiments.Param{Name: prm.Name, Values: deref(prm.Values), Min: prm.Min, Max: prm.Max, Integer: deref(prm.Integer)}
		if prm.Scale != nil {
			p.Scale = string(*prm.Scale)
		}
		in.Parameters = append(in.Parameters, p)
	}
	weighed := true
	if pl, err := s.experiments.Plan(ctx, s.Pool, in); err != nil {
		ctx, weighed = spending(ctx, 0), false // the command fails on the same plan; nothing to weigh
	} else {
		ctx = spending(ctx, pl.Total.Value)
	}
	cmd := command(ctx, "sweeps.run", req.Params.IdempotencyKey, req.Params.DryRun)
	return s.run(ctx, cmd, func(ctx context.Context, tx pgx.Tx) (commands.Result, []events.Draft, error) {
		in.Actor = cmd.Actor
		pl, err := s.experiments.Plan(ctx, tx, in)
		if err != nil {
			return commands.Result{}, nil, err
		}
		if !weighed {
			return commands.Result{}, nil, unweighed(cmd.Operation)
		}
		if cmd.DryRun {
			return commands.Result{Status: http.StatusOK, Body: pl.PlanJSON()}, nil, nil
		}
		v, drafts, err := s.experiments.Start(ctx, tx, pl)
		if err != nil {
			return commands.Result{}, nil, err
		}
		return commands.Result{Status: http.StatusCreated, Body: v, ETag: commands.ETag(v.Rev)}, drafts, nil
	})
}
