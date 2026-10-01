package server

import (
	"context"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/usunrise88/cadence/control-plane/internal/api"
	"github.com/usunrise88/cadence/control-plane/internal/auth"
	"github.com/usunrise88/cadence/control-plane/internal/commands"
	"github.com/usunrise88/cadence/control-plane/internal/events"
	"github.com/usunrise88/cadence/control-plane/internal/jobs"
	"github.com/usunrise88/cadence/control-plane/internal/pipelines"
	"github.com/usunrise88/cadence/control-plane/internal/policy"
	"github.com/usunrise88/cadence/control-plane/internal/problems"
	"github.com/usunrise88/cadence/control-plane/internal/registry"
	"github.com/usunrise88/cadence/control-plane/internal/runs"
)

// Runs, checkpoints and metrics (phase 2 · wave 2 · stream R): runs.new|list|get|calibrate|resume|stage,
// checkpoints.list|get|average, metrics.get. The domain lives in internal/runs; these handlers map the contract
// onto it. Spending commands plan once outside the command so the policy weighs their estimate against the GPU
// budgets (as pipelines.run does), then plan again inside the command's transaction.

func (c commandResponse) VisitRunsNewResponse(w http.ResponseWriter) error       { return c.write(w) }
func (c commandResponse) VisitRunsCalibrateResponse(w http.ResponseWriter) error { return c.write(w) }
func (c commandResponse) VisitRunsResumeResponse(w http.ResponseWriter) error    { return c.write(w) }
func (c commandResponse) VisitRunsStageResponse(w http.ResponseWriter) error     { return c.write(w) }
func (c commandResponse) VisitCheckpointsAverageResponse(w http.ResponseWriter) error {
	return c.write(w)
}

// newRunsService wires the runs domain to the server's engine, store and defaults.
func (s *Server) newRunsService() *runs.Service {
	return &runs.Service{Pool: s.Pool, Engine: s.Pipelines, CAS: s.CAS, Defaults: s.defaultsDoc,
		RenderVersion: func(v registry.Version) any { return apiVersion(v) }}
}

// withRunStatus adds the status change of the run a paused or resumed step job belongs to.
func (s *Server) withRunStatus(ctx context.Context, tx pgx.Tx, j jobs.Job, drafts []events.Draft, err error) (jobs.Job, []events.Draft, error) {
	if err != nil {
		return j, nil, err
	}
	more, err := s.runs.JobChanged(ctx, tx, j.ID)
	if err != nil {
		return jobs.Job{}, nil, err
	}
	return j, append(drafts, more...), nil
}

// spending names a command's GPU-hours estimate to the policy engine (budgets, R12). A spending command without one
// waits for a person (the policy fails closed), so every spending handler names one.
func spending(ctx context.Context, gpuHours float64) context.Context {
	return commands.WithEstimate(ctx, policy.Estimate{GPUHours: gpuHours})
}

func refOr(p *string) string { return strings.TrimSpace(deref(p)) }

// scopedRun reads the project of run id and checks the request's scope reaches it.
func (s *Server) scopedRun(ctx context.Context, id string) (string, error) {
	projectID, err := runs.ProjectOf(ctx, s.Pool, id)
	if err != nil {
		return "", err
	}
	return projectID, auth.CheckProject(ctx, projectID)
}

// ---------------------------------------------------------------- runs

// RunsNew implements runs.new: the dry run answers the estimate; the real run starts the recipe's pipeline run and
// answers 201 with the run (202 with an approval when an agent's estimate exceeds a GPU budget).
func (s *Server) RunsNew(ctx context.Context, req api.RunsNewRequestObject) (api.RunsNewResponseObject, error) {
	p, err := scopedProject(ctx, s.Pool, req.P)
	if err != nil {
		return nil, err
	}
	ctx = commands.WithProject(ctx, p.ID)
	b := req.Body
	actor, _ := auth.FromContext(ctx)
	if refOr(b.Mix) == "" {
		if req.Params.DryRun == nil || !*req.Params.DryRun {
			return nil, problems.Validation([]problems.FieldError{{Path: "/mix", Message: "name the mix to train on (mix_… or its name); without one only a dry run answers, from datasets"}})
		}
		in := runs.Input{ProjectID: p.ID, BaseModel: deref(b.BaseModel), Checkpoint: deref(b.Checkpoint), Steps: b.Steps, GPUs: b.Gpus,
			Compute: deref(b.Compute), Datasets: deref(b.Datasets), SessionID: actor.SessionID}
		if b.Init != nil {
			in.Init = string(*b.Init)
		}
		if b.Precision != nil {
			in.Precision = string(*b.Precision)
		}
		return s.run(ctx, command(ctx, "runs.new", req.Params.IdempotencyKey, req.Params.DryRun), func(ctx context.Context, tx pgx.Tx) (commands.Result, []events.Draft, error) {
			e, err := runs.EstimateRun(ctx, tx, s.defaultsDoc(), in)
			if err != nil {
				return commands.Result{}, nil, err
			}
			return commands.Result{Status: http.StatusOK, Body: s.runs.EstimateJSON(e)}, nil, nil
		})
	}
	in := runs.NewInput{
		ProjectID: p.ID, Actor: actor, BaseModel: deref(b.BaseModel), Checkpoint: deref(b.Checkpoint), Steps: b.Steps, Seed: b.Seed,
		GPUs: b.Gpus, Compute: deref(b.Compute), Mix: refOr(b.Mix), MixRevision: deref(b.MixRevision), Pipeline: deref(b.Pipeline),
		Ref: deref(b.Ref), Params: deref(b.Params), Priority: deref(b.Priority),
	}
	if b.Init != nil {
		in.Init = string(*b.Init)
	}
	if b.Precision != nil {
		in.Precision = string(*b.Precision)
	}
	return s.startRun(ctx, "runs.new", req.Params.IdempotencyKey, req.Params.DryRun, func(ctx context.Context, q pgx.Tx) (runs.NewInput, error) {
		return in, nil
	}, in)
}

// startRun runs runs.new or runs.stage: input builds the run's input inside the command (checks that need the
// transaction, such as the parent's revision); outside is the same input for the policy's estimate.
func (s *Server) startRun(ctx context.Context, op, key string, dryRun *bool, input func(context.Context, pgx.Tx) (runs.NewInput, error),
	outside runs.NewInput) (commandResponse, error) {
	if pr, err := s.runs.Prepare(ctx, s.Pool, outside); err != nil {
		ctx = spending(ctx, 0) // the command fails on the same plan; nothing to weigh
	} else {
		ctx = spending(ctx, pr.Estimate.GPUHours.Value)
	}
	cmd := command(ctx, op, key, dryRun)
	return s.run(ctx, cmd, func(ctx context.Context, tx pgx.Tx) (commands.Result, []events.Draft, error) {
		in, err := input(ctx, tx)
		if err != nil {
			return commands.Result{}, nil, err
		}
		in.Actor = cmd.Actor
		pr, err := s.runs.Prepare(ctx, tx, in)
		if err != nil {
			return commands.Result{}, nil, err
		}
		if cmd.DryRun {
			return commands.Result{Status: http.StatusOK, Body: s.runs.EstimateJSON(pr.Estimate)}, nil, nil
		}
		v, drafts, err := s.runs.Create(ctx, tx, pr)
		if err != nil {
			return commands.Result{}, nil, err
		}
		return commands.Result{Status: http.StatusCreated, Body: v, ETag: commands.ETag(v.Rev)}, drafts, nil
	})
}

// RunsList implements runs.list.
func (s *Server) RunsList(ctx context.Context, req api.RunsListRequestObject) (api.RunsListResponseObject, error) {
	p, err := scopedProject(ctx, s.Pool, req.P)
	if err != nil {
		return nil, err
	}
	f := runs.ListFilter{ProjectID: p.ID, Limit: deref(req.Params.Limit)}
	if req.Params.Status != nil {
		f.Status = string(*req.Params.Status)
	}
	list, err := s.runs.List(ctx, s.Pool, f)
	if err != nil {
		return nil, err
	}
	out, err := convert[api.RunList](map[string]any{"items": list})
	if err != nil {
		return nil, err
	}
	return api.RunsList200JSONResponse(out), nil
}

// RunsGet implements runs.get.
func (s *Server) RunsGet(ctx context.Context, req api.RunsGetRequestObject) (api.RunsGetResponseObject, error) {
	if _, err := s.scopedRun(ctx, req.Id); err != nil {
		return nil, err
	}
	v, err := s.runs.Get(ctx, s.Pool, req.Id)
	if err != nil {
		return nil, err
	}
	out, err := convert[api.Run](v)
	if err != nil {
		return nil, err
	}
	etag := commands.ETag(v.Rev)
	return api.RunsGet200JSONResponse{Body: out, Headers: api.RunsGet200ResponseHeaders{ETag: &etag}}, nil
}

// RunsCalibrate implements runs.calibrate: the family's calibrate role as a one-step pipeline run.
func (s *Server) RunsCalibrate(ctx context.Context, req api.RunsCalibrateRequestObject) (api.RunsCalibrateResponseObject, error) {
	p, err := scopedProject(ctx, s.Pool, req.P)
	if err != nil {
		return nil, err
	}
	ctx = commands.WithProject(ctx, p.ID)
	b := req.Body
	actor, _ := auth.FromContext(ctx)
	in := runs.CalibrateInput{ProjectID: p.ID, Actor: actor, BaseModel: deref(b.BaseModel), Mix: strings.TrimSpace(b.Mix),
		MixRevision: deref(b.MixRevision), Compute: deref(b.Compute), Params: deref(b.Params)}
	if b.Precision != nil {
		in.Precision = string(*b.Precision)
	}
	if cp, err := s.runs.PlanCalibration(ctx, s.Pool, in); err != nil {
		ctx = spending(ctx, 0) // the command fails on the same plan; nothing to weigh
	} else {
		ctx = spending(ctx, cp.GPUHours)
	}
	cmd := command(ctx, "runs.calibrate", req.Params.IdempotencyKey, req.Params.DryRun)
	return s.run(ctx, cmd, func(ctx context.Context, tx pgx.Tx) (commands.Result, []events.Draft, error) {
		in.Actor = cmd.Actor
		cp, err := s.runs.PlanCalibration(ctx, tx, in)
		if err != nil {
			return commands.Result{}, nil, err
		}
		body := map[string]any{"key": cp.Key, "family": cp.Family.Name, "step": cp.Kind.Ref(), "mix": cp.Mix.Ref}
		if cp.Current != nil {
			body["current"] = map[string]any{"secondsPerStep": cp.Current.SecondsPerStep, "plusMinus": cp.Current.PlusMinus,
				"measuredAt": cp.Current.MeasuredAt}
		}
		if cmd.DryRun {
			body["plan"] = planView(cp.Source, cp.Plan)
			return commands.Result{Status: http.StatusOK, Body: body}, nil, nil
		}
		pr, drafts, err := s.runs.StartCalibration(ctx, tx, cp)
		if err != nil {
			return commands.Result{}, nil, err
		}
		body["pipelineRun"] = pr
		return commands.Result{Status: http.StatusCreated, Body: body}, drafts, nil
	})
}

// RunsResume implements runs.resume.
func (s *Server) RunsResume(ctx context.Context, req api.RunsResumeRequestObject) (api.RunsResumeResponseObject, error) {
	rev, err := commands.ParseIfMatch(req.Params.IfMatch)
	if err != nil {
		return nil, err
	}
	projectID, err := s.scopedRun(ctx, req.Id)
	if err != nil {
		return nil, err
	}
	ctx = commands.WithProject(ctx, projectID)
	actor, _ := auth.FromContext(ctx)
	if rp, err := s.runs.PlanResume(ctx, s.Pool, req.Id, actor.SessionID); err != nil {
		ctx = spending(ctx, 0) // the command fails on the same plan; nothing to weigh
	} else {
		ctx = spending(ctx, rp.Estimate.GPUHours.Value)
	}
	cmd := command(ctx, "runs.resume", req.Params.IdempotencyKey, req.Params.DryRun)
	return s.run(ctx, cmd, func(ctx context.Context, tx pgx.Tx) (commands.Result, []events.Draft, error) {
		v, drafts, err := s.runs.Resume(ctx, tx, req.Id, rev, cmd.Actor)
		if err != nil {
			return commands.Result{}, nil, err
		}
		return commands.Result{Status: http.StatusOK, Body: v, ETag: commands.ETag(v.Rev)}, drafts, nil
	})
}

// RunsStage implements runs.stage: a new run from a checkpoint of the parent with an explicit peak learning rate.
func (s *Server) RunsStage(ctx context.Context, req api.RunsStageRequestObject) (api.RunsStageResponseObject, error) {
	rev, err := commands.ParseIfMatch(req.Params.IfMatch)
	if err != nil {
		return nil, err
	}
	projectID, err := s.scopedRun(ctx, req.Id)
	if err != nil {
		return nil, err
	}
	ctx = commands.WithProject(ctx, projectID)
	b := req.Body
	actor, _ := auth.FromContext(ctx)
	in := runs.StageInput{Checkpoint: deref(b.Checkpoint), PeakLR: b.PeakLr, Mix: refOr(b.Mix), MixRevision: deref(b.MixRevision),
		Steps: b.Steps, Seed: b.Seed, Compute: deref(b.Compute), Pipeline: deref(b.Pipeline), Ref: deref(b.Ref),
		Params: deref(b.Params), Priority: deref(b.Priority)}
	if b.Precision != nil {
		in.Precision = string(*b.Precision)
	}
	outside, err := s.runs.StageNew(ctx, s.Pool, req.Id, -1, in, actor)
	if err != nil {
		outside = runs.NewInput{} // the command reports the problem
	}
	return s.startRun(ctx, "runs.stage", req.Params.IdempotencyKey, req.Params.DryRun, func(ctx context.Context, tx pgx.Tx) (runs.NewInput, error) {
		return s.runs.StageNew(ctx, tx, req.Id, rev, in, actor)
	}, outside)
}

// ---------------------------------------------------------------- checkpoints

// CheckpointsList implements checkpoints.list.
func (s *Server) CheckpointsList(ctx context.Context, req api.CheckpointsListRequestObject) (api.CheckpointsListResponseObject, error) {
	p, err := scopedProject(ctx, s.Pool, req.P)
	if err != nil {
		return nil, err
	}
	f := runs.CheckpointFilter{ProjectID: p.ID, RunID: deref(req.Params.Run), Kept: deref(req.Params.Kept), Limit: deref(req.Params.Limit)}
	list, err := runs.ListCheckpoints(ctx, s.Pool, f)
	if err != nil {
		return nil, err
	}
	if list == nil {
		list = []runs.Checkpoint{}
	}
	out, err := convert[api.CheckpointList](map[string]any{"items": list, "keepTopK": s.defaultsDoc().Training.KeepTopK.Value})
	if err != nil {
		return nil, err
	}
	return api.CheckpointsList200JSONResponse(out), nil
}

// CheckpointsGet implements checkpoints.get.
func (s *Server) CheckpointsGet(ctx context.Context, req api.CheckpointsGetRequestObject) (api.CheckpointsGetResponseObject, error) {
	c, err := runs.GetCheckpoint(ctx, s.Pool, req.Id)
	if err != nil {
		return nil, err
	}
	if err := auth.CheckProject(ctx, c.ProjectID); err != nil {
		return nil, err
	}
	out, err := convert[api.Checkpoint](c)
	if err != nil {
		return nil, err
	}
	return api.CheckpointsGet200JSONResponse(out), nil
}

// CheckpointsAverage implements checkpoints.average: the family's average role over checkpoints of one run.
func (s *Server) CheckpointsAverage(ctx context.Context, req api.CheckpointsAverageRequestObject) (api.CheckpointsAverageResponseObject, error) {
	projectID, err := s.scopedRun(ctx, req.Id)
	if err != nil {
		return nil, err
	}
	ctx = commands.WithProject(ctx, projectID)
	actor, _ := auth.FromContext(ctx)
	ids, params := req.Body.Checkpoints, deref(req.Body.Params)
	if ap, err := s.runs.PlanAverage(ctx, s.Pool, req.Id, ids, params, actor); err != nil {
		ctx = spending(ctx, 0) // the command fails on the same plan; nothing to weigh
	} else {
		ctx = spending(ctx, ap.GPUHours)
	}
	cmd := command(ctx, "checkpoints.average", req.Params.IdempotencyKey, req.Params.DryRun)
	return s.run(ctx, cmd, func(ctx context.Context, tx pgx.Tx) (commands.Result, []events.Draft, error) {
		ap, err := s.runs.PlanAverage(ctx, tx, req.Id, ids, params, cmd.Actor)
		if err != nil {
			return commands.Result{}, nil, err
		}
		body := map[string]any{"runId": req.Id, "step": ap.Kind.Ref(), "checkpoints": ap.Checkpoints}
		if cmd.DryRun {
			body["plan"] = planView(pipelines.Source{Pipeline: *ap.Start.Pipeline, Kind: pipelines.SourceInline}, ap.Plan)
			return commands.Result{Status: http.StatusOK, Body: body}, nil, nil
		}
		pr, drafts, err := s.runs.StartAverage(ctx, tx, ap)
		if err != nil {
			return commands.Result{}, nil, err
		}
		body["pipelineRun"] = pr
		return commands.Result{Status: http.StatusCreated, Body: body}, drafts, nil
	})
}

// ---------------------------------------------------------------- metrics

// MetricsGet implements metrics.get over internal/telemetry, binned server-side (R53).
func (s *Server) MetricsGet(ctx context.Context, req api.MetricsGetRequestObject) (api.MetricsGetResponseObject, error) {
	if _, err := s.scopedRun(ctx, req.Id); err != nil {
		return nil, err
	}
	mq := runs.MetricsQuery{RunID: req.Id, Names: deref(req.Params.Names), MaxPoints: deref(req.Params.MaxPoints), AfterStep: req.Params.AfterStep}
	if req.Params.X != nil {
		mq.X = string(*req.Params.X)
	}
	set, err := runs.Metrics(ctx, s.Pool, mq)
	if err != nil {
		return nil, err
	}
	out, err := convert[api.MetricSeriesSet](set)
	if err != nil {
		return nil, err
	}
	return api.MetricsGet200JSONResponse(out), nil
}
