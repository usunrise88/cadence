package server

import (
	"context"
	"net/http"

	"github.com/jackc/pgx/v5"

	"github.com/usunrise88/cadence/control-plane/internal/api"
	"github.com/usunrise88/cadence/control-plane/internal/commands"
	"github.com/usunrise88/cadence/control-plane/internal/delivery"
	"github.com/usunrise88/cadence/control-plane/internal/events"
	"github.com/usunrise88/cadence/control-plane/internal/modelexports"
	"github.com/usunrise88/cadence/control-plane/internal/policy"
)

// Model exports, parity checks and benchmarks (phase 5 · stream D1): models.export|parity|benchmark run pipelines
// generated per request (internal/modelexports). Each plans once outside the command so the policy weighs its
// GPU-hour estimate against the budgets (as goldenSets.align does), then again inside; a dry run, or an export with
// nothing left to export, answers the plan.

func (c commandResponse) VisitModelsExportResponse(w http.ResponseWriter) error    { return c.write(w) }
func (c commandResponse) VisitModelsParityResponse(w http.ResponseWriter) error    { return c.write(w) }
func (c commandResponse) VisitModelsBenchmarkResponse(w http.ResponseWriter) error { return c.write(w) }

// newModelExports wires the export service: its output hooks and its pipeline-run observer.
func (s *Server) newModelExports() {
	s.modelExports = &modelexports.Service{Engine: s.Pipelines, CAS: s.CAS, Evals: s.evals, Defaults: s.defaultsDoc}
	s.modelExports.Install(s.StepHooks)
}

// deliverySources are the delivery bundle's sources: the content store, with the smoke set of the record's export.
func (s *Server) deliverySources() delivery.Sources {
	return delivery.WithSmoke{Sources: delivery.StoreSources{CAS: s.CAS},
		From: func(ctx context.Context, versionID, deployableHash string) (delivery.Smoke, error) {
			return s.modelExports.Smoke(ctx, s.Pool, versionID, deployableHash)
		}}
}

// estimated is what the policy weighs of a plan.
type estimated interface {
	GPUHours() float64
	Unknown() bool
}

// weigh plans once outside the command: the context then carries the plan's estimate for the policy, or nothing to
// weigh when planning failed (the command fails on the same plan).
func weigh[P estimated](ctx context.Context, plan func() (P, error)) (context.Context, bool) {
	pl, err := plan()
	if err != nil {
		return spending(ctx, 0), false
	}
	return commands.WithEstimate(ctx, policy.Estimate{GPUHours: pl.GPUHours(), Unknown: pl.Unknown()}), true
}

// ModelsExport implements models.export.
func (s *Server) ModelsExport(ctx context.Context, req api.ModelsExportRequestObject) (api.ModelsExportResponseObject, error) {
	p, err := scopedProject(ctx, s.Pool, req.P)
	if err != nil {
		return nil, err
	}
	ctx = commands.WithProject(ctx, p.ID)
	in := modelexports.ExportInput{ProjectID: p.ID}
	if b := req.Body; b != nil {
		in.Version, in.Profiles, in.Format, in.Priority = b.Version, deref(b.Profiles), refOr(b.Format), deref(b.Priority)
	}
	ctx, weighed := weigh(ctx, func() (modelexports.ExportPlan, error) { return s.modelExports.PrepareExport(ctx, s.Pool, in) })
	cmd := command(ctx, modelexports.OpExport, req.Params.IdempotencyKey, req.Params.DryRun)
	return s.run(ctx, cmd, func(ctx context.Context, tx pgx.Tx) (commands.Result, []events.Draft, error) {
		in.Actor = cmd.Actor
		pl, err := s.modelExports.PrepareExport(ctx, tx, in)
		if err != nil {
			return commands.Result{}, nil, err
		}
		if !weighed && pl.Started() {
			return commands.Result{}, nil, unweighed(cmd.Operation)
		}
		if cmd.DryRun || !pl.Started() {
			return commands.Result{Status: http.StatusOK, Body: pl}, nil, nil
		}
		pl, drafts, err := s.modelExports.StartExport(ctx, tx, pl, cmd.Actor)
		if err != nil {
			return commands.Result{}, nil, err
		}
		return commands.Result{Status: http.StatusCreated, Body: pl}, drafts, nil
	})
}

// ModelsParity implements models.parity.
func (s *Server) ModelsParity(ctx context.Context, req api.ModelsParityRequestObject) (api.ModelsParityResponseObject, error) {
	p, err := scopedProject(ctx, s.Pool, req.P)
	if err != nil {
		return nil, err
	}
	ctx = commands.WithProject(ctx, p.ID)
	in := modelexports.CheckInput{ProjectID: p.ID}
	if b := req.Body; b != nil {
		in.Version, in.Profile, in.Format, in.GoldenSet, in.Priority = b.Version, refOr(b.Profile), refOr(b.Format), refOr(b.GoldenSet), deref(b.Priority)
	}
	ctx, weighed := weigh(ctx, func() (modelexports.ParityPlan, error) { return s.modelExports.PrepareParity(ctx, s.Pool, in) })
	cmd := command(ctx, modelexports.OpParity, req.Params.IdempotencyKey, req.Params.DryRun)
	return s.run(ctx, cmd, func(ctx context.Context, tx pgx.Tx) (commands.Result, []events.Draft, error) {
		in.Actor = cmd.Actor
		pl, err := s.modelExports.PrepareParity(ctx, tx, in)
		if err != nil {
			return commands.Result{}, nil, err
		}
		if !weighed {
			return commands.Result{}, nil, unweighed(cmd.Operation)
		}
		if cmd.DryRun {
			return commands.Result{Status: http.StatusOK, Body: pl}, nil, nil
		}
		pl, drafts, err := s.modelExports.StartParity(ctx, tx, pl, cmd.Actor)
		if err != nil {
			return commands.Result{}, nil, err
		}
		return commands.Result{Status: http.StatusCreated, Body: pl}, drafts, nil
	})
}

// ModelsBenchmark implements models.benchmark.
func (s *Server) ModelsBenchmark(ctx context.Context, req api.ModelsBenchmarkRequestObject) (api.ModelsBenchmarkResponseObject, error) {
	p, err := scopedProject(ctx, s.Pool, req.P)
	if err != nil {
		return nil, err
	}
	ctx = commands.WithProject(ctx, p.ID)
	in := modelexports.CheckInput{ProjectID: p.ID}
	if b := req.Body; b != nil {
		in.Version, in.Profile, in.Format, in.GoldenSet, in.Priority = b.Version, refOr(b.Profile), refOr(b.Format), refOr(b.GoldenSet), deref(b.Priority)
		in.Target, in.Streams = refOr(b.Target), deref(b.Streams)
		if b.SecondsPerLevel != nil {
			in.SecondsPerLevel = float64(*b.SecondsPerLevel)
		}
	}
	ctx, weighed := weigh(ctx, func() (modelexports.BenchmarkPlan, error) { return s.modelExports.PrepareBenchmark(ctx, s.Pool, in) })
	cmd := command(ctx, modelexports.OpBenchmark, req.Params.IdempotencyKey, req.Params.DryRun)
	return s.run(ctx, cmd, func(ctx context.Context, tx pgx.Tx) (commands.Result, []events.Draft, error) {
		in.Actor = cmd.Actor
		pl, err := s.modelExports.PrepareBenchmark(ctx, tx, in)
		if err != nil {
			return commands.Result{}, nil, err
		}
		if !weighed {
			return commands.Result{}, nil, unweighed(cmd.Operation)
		}
		if cmd.DryRun {
			return commands.Result{Status: http.StatusOK, Body: pl}, nil, nil
		}
		pl, drafts, err := s.modelExports.StartBenchmark(ctx, tx, pl, cmd.Actor)
		if err != nil {
			return commands.Result{}, nil, err
		}
		return commands.Result{Status: http.StatusCreated, Body: pl}, drafts, nil
	})
}
