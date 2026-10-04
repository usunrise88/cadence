package server

import (
	"context"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/usunrise88/cadence/control-plane/internal/api"
	"github.com/usunrise88/cadence/control-plane/internal/commands"
	"github.com/usunrise88/cadence/control-plane/internal/events"
	"github.com/usunrise88/cadence/control-plane/internal/goldensets"
	"github.com/usunrise88/cadence/control-plane/internal/policy"
)

// goldenSets.align (phase 4 tail): one pipeline run aligns the reference texts of several golden sets
// (internal/goldensets/align.go), so a batch is one GPU-spend decision with a known estimate.

func (c commandResponse) VisitGoldenSetsAlignResponse(w http.ResponseWriter) error { return c.write(w) }

// GoldenSetsAlign implements goldenSets.align. It plans once outside the command so the policy weighs the batch's
// GPU-hour estimate against the budgets, then again inside; a dry run, or a real call with nothing left to align,
// answers the plan.
func (s *Server) GoldenSetsAlign(ctx context.Context, req api.GoldenSetsAlignRequestObject) (api.GoldenSetsAlignResponseObject, error) {
	p, err := scopedProject(ctx, s.Pool, req.P)
	if err != nil {
		return nil, err
	}
	ctx = commands.WithProject(ctx, p.ID)
	in := goldensets.AlignInput{ProjectID: p.ID}
	if b := req.Body; b != nil {
		in.GoldenSets, in.Aligner, in.Priority = deref(b.GoldenSets), strings.TrimSpace(deref(b.Aligner)), deref(b.Priority)
	}
	al := &goldensets.Aligner{Engine: s.Pipelines, CAS: s.CAS, Defaults: s.defaultsDoc}
	weighed := true
	if pl, err := al.Prepare(ctx, s.Pool, in); err != nil {
		ctx, weighed = spending(ctx, 0), false // the command fails on the same plan; nothing to weigh
	} else {
		ctx = commands.WithEstimate(ctx, policy.Estimate{GPUHours: pl.GPUHours(), Unknown: pl.Unknown()})
	}
	cmd := command(ctx, goldensets.AlignOperation, req.Params.IdempotencyKey, req.Params.DryRun)
	return s.run(ctx, cmd, func(ctx context.Context, tx pgx.Tx) (commands.Result, []events.Draft, error) {
		in.Actor = cmd.Actor
		pl, err := al.Prepare(ctx, tx, in)
		if err != nil {
			return commands.Result{}, nil, err
		}
		if !weighed && pl.Started() {
			return commands.Result{}, nil, unweighed(cmd.Operation)
		}
		if cmd.DryRun || !pl.Started() {
			return commands.Result{Status: http.StatusOK, Body: pl}, nil, nil
		}
		pl, drafts, err := al.Start(ctx, tx, pl, cmd.Actor)
		if err != nil {
			return commands.Result{}, nil, err
		}
		return commands.Result{Status: http.StatusCreated, Body: pl}, drafts, nil
	})
}
