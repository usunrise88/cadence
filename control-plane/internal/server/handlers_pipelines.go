package server

import (
	"context"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/usunrise88/cadence/control-plane/internal/api"
	"github.com/usunrise88/cadence/control-plane/internal/artifacts"
	"github.com/usunrise88/cadence/control-plane/internal/auth"
	"github.com/usunrise88/cadence/control-plane/internal/commands"
	"github.com/usunrise88/cadence/control-plane/internal/events"
	"github.com/usunrise88/cadence/control-plane/internal/pipelines"
	"github.com/usunrise88/cadence/control-plane/internal/policy"
	"github.com/usunrise88/cadence/control-plane/internal/steps"
)

// Artifacts and pipelines (phase 2 · stream P): artifacts.get, pipelines.list|run, pipelineRuns.list|get|cancel|
// retry|wait. The engine lives in internal/pipelines; these handlers only map the contract onto it.

func (c commandResponse) VisitPipelinesRunResponse(w http.ResponseWriter) error { return c.write(w) }
func (c commandResponse) VisitPipelineRunsCancelResponse(w http.ResponseWriter) error {
	return c.write(w)
}
func (c commandResponse) VisitPipelineRunsRetryResponse(w http.ResponseWriter) error {
	return c.write(w)
}

// ---------------------------------------------------------------- artifacts

// ArtifactsGet implements artifacts.get.
func (s *Server) ArtifactsGet(ctx context.Context, req api.ArtifactsGetRequestObject) (api.ArtifactsGetResponseObject, error) {
	a, err := artifacts.Get(ctx, s.Pool, req.Hash)
	if err != nil {
		return nil, err
	}
	if err := s.checkArtifact(ctx, a); err != nil {
		return nil, err
	}
	view := struct {
		artifacts.Artifact
		Files          any    `json:"files,omitempty"`
		Encoding       string `json:"encoding,omitempty"`
		Content        string `json:"content,omitempty"`
		ContentOmitted string `json:"contentOmitted,omitempty"`
	}{Artifact: a}
	files, err := artifacts.Files(ctx, s.Pool, s.CAS, a)
	if err != nil {
		return nil, err
	}
	if files != nil {
		view.Files = files
	}
	if deref(req.Params.Content) {
		c, err := artifacts.ReadContent(s.CAS, a, deref(req.Params.Path))
		if err != nil {
			return nil, err
		}
		view.Encoding, view.Content, view.ContentOmitted = c.Encoding, c.Content, c.Omitted
	}
	out, err := convert[api.Artifact](view)
	if err != nil {
		return nil, err
	}
	return api.ArtifactsGet200JSONResponse(out), nil
}

// checkArtifact authorises reading an artifact: one linked to projects needs one of them in scope; one linked to
// none (a registry artifact) needs registry read.
func (s *Server) checkArtifact(ctx context.Context, a artifacts.Artifact) error {
	if sc, ok := auth.ScopeFromContext(ctx); ok && sc.All {
		return nil
	}
	linked, err := artifacts.Projects(ctx, s.Pool, a.Hash)
	if err != nil {
		return err
	}
	if len(linked) == 0 && a.ProjectID == "" {
		return auth.CheckRegistryRead(ctx)
	}
	if sc, ok := auth.ScopeFromContext(ctx); ok && slices.Contains(linked, sc.ProjectID) {
		return nil
	}
	first := a.ProjectID
	if first == "" {
		first = linked[0]
	}
	return auth.CheckProject(ctx, first)
}

// ---------------------------------------------------------------- pipelines

type pipelineView struct {
	Name        string            `json:"name"`
	Description string            `json:"description,omitempty"`
	Source      string            `json:"source"`
	Path        string            `json:"path"`
	Version     string            `json:"version"`
	Inputs      map[string]string `json:"inputs"`
	Steps       []pipelines.Step  `json:"steps"`
	Error       string            `json:"error,omitempty"`
}

// PipelinesList implements pipelines.list.
func (s *Server) PipelinesList(ctx context.Context, req api.PipelinesListRequestObject) (api.PipelinesListResponseObject, error) {
	p, err := scopedProject(ctx, s.Pool, req.P)
	if err != nil {
		return nil, err
	}
	ref := refOf(req.Params.Ref)
	commit, list, err := s.Pipelines.List(ctx, p, ref)
	if err != nil {
		return nil, err
	}
	items := make([]pipelineView, 0, len(list))
	for _, src := range list {
		v := pipelineView{Name: src.Pipeline.Name, Description: src.Pipeline.Description, Source: src.Kind, Path: src.Path,
			Version: src.Version, Inputs: src.Pipeline.Inputs, Steps: src.Pipeline.Steps}
		if src.Err != nil {
			v.Error = src.Err.Error()
		}
		if v.Inputs == nil {
			v.Inputs = map[string]string{}
		}
		if v.Steps == nil {
			v.Steps = []pipelines.Step{}
		}
		items = append(items, v)
	}
	out, err := convert[api.PipelineList](map[string]any{"ref": ref, "commit": optional(commit), "items": items})
	if err != nil {
		return nil, err
	}
	return api.PipelinesList200JSONResponse(out), nil
}

// runBody is the contract's PipelineRunNew with inputs as step artifact references.
type runBody struct {
	Ref      string                       `json:"ref"`
	Inputs   map[string]steps.ArtifactRef `json:"inputs"`
	Params   map[string]map[string]any    `json:"params"`
	Fresh    bool                         `json:"fresh"`
	Priority int                          `json:"priority"`
}

// versionOf reads the pipeline version pipelines.run expects from If-Match ("*" accepts any).
func versionOf(ifMatch string) string {
	v := strings.TrimPrefix(strings.TrimSpace(ifMatch), "W/")
	return strings.Trim(v, `"`)
}

// PipelinesRun implements pipelines.run: a dry run validates and plans; a real run starts a pipeline run.
func (s *Server) PipelinesRun(ctx context.Context, req api.PipelinesRunRequestObject) (api.PipelinesRunResponseObject, error) {
	p, err := scopedProject(ctx, s.Pool, req.P)
	if err != nil {
		return nil, err
	}
	var body runBody
	if req.Body != nil {
		if body, err = convert[runBody](req.Body); err != nil {
			return nil, err
		}
	}
	in := pipelines.StartInput{
		ProjectID: p.ID, Name: req.Name, Ref: body.Ref, Version: versionOf(req.Params.IfMatch), Inputs: body.Inputs,
		Params: body.Params, Fresh: body.Fresh, Priority: body.Priority,
	}
	ctx = commands.WithProject(ctx, p.ID)
	// The policy weighs GPU spending against the budget: plan once outside the command to learn the estimate. A
	// broken pipeline is reported by the command itself (the same plan fails inside it), so it is not gated; a step
	// that needs a card without an estimate makes the cost unknown, which the policy does not allow without a person.
	weighed := true
	if _, plan, err := s.Pipelines.Prepare(ctx, s.Pool, in); err != nil {
		ctx, weighed = commands.WithEstimate(ctx, policy.Estimate{}), false
	} else {
		ctx = commands.WithEstimate(ctx, policy.Estimate{GPUHours: deref(plan.Estimate.GPUHours), Unknown: plan.Estimate.UnknownGPU})
	}
	cmd := command(ctx, "pipelines.run", req.Params.IdempotencyKey, req.Params.DryRun)
	return s.run(ctx, cmd, func(ctx context.Context, tx pgx.Tx) (commands.Result, []events.Draft, error) {
		in.Actor = cmd.Actor
		if cmd.DryRun || !weighed {
			src, plan, err := s.Pipelines.Prepare(ctx, tx, in)
			if err != nil {
				return commands.Result{}, nil, err
			}
			if !weighed {
				return commands.Result{}, nil, unweighed(cmd.Operation)
			}
			return commands.Result{Status: http.StatusOK, Body: planView(src, plan)}, nil, nil
		}
		r, drafts, err := s.Pipelines.Start(ctx, tx, in)
		if err != nil {
			return commands.Result{}, nil, err
		}
		return commands.Result{Status: http.StatusCreated, Body: r, ETag: commands.ETag(r.Rev)}, drafts, nil
	})
}

func planView(src pipelines.Source, plan pipelines.Plan) map[string]any {
	type stepView struct {
		pipelines.PlanStep
		Kind              string            `json:"kind"`
		KindVersion       string            `json:"kindVersion"`
		StepKindVersionID string            `json:"stepKindVersionId,omitempty"`
		Runtime           string            `json:"runtime,omitempty"`
		Produces          map[string]string `json:"produces"`
		Resources         steps.Resources   `json:"resources"`
	}
	list := make([]stepView, 0, len(plan.Steps))
	for _, ps := range plan.Steps {
		prod := ps.Kind.Produces
		if prod == nil {
			prod = map[string]string{}
		}
		list = append(list, stepView{PlanStep: ps, Kind: ps.Kind.Name, KindVersion: ps.Kind.Version,
			StepKindVersionID: ps.Kind.VersionID, Runtime: ps.Kind.Runtime, Produces: prod, Resources: ps.Kind.Resources})
	}
	return map[string]any{
		"pipeline": src.Pipeline.Name, "source": src.Kind, "ref": optional(src.Ref), "commit": optional(src.Commit),
		"version": src.Version, "steps": list, "estimate": plan.Estimate, "warnings": append([]pipelines.Warning{}, plan.Warnings...),
	}
}

// ---------------------------------------------------------------- pipeline runs

// PipelineRunsList implements pipelineRuns.list.
func (s *Server) PipelineRunsList(ctx context.Context, req api.PipelineRunsListRequestObject) (api.PipelineRunsListResponseObject, error) {
	p, err := scopedProject(ctx, s.Pool, req.P)
	if err != nil {
		return nil, err
	}
	f := pipelines.ListFilter{ProjectID: p.ID, Pipeline: deref(req.Params.Pipeline), Limit: deref(req.Params.Limit)}
	if req.Params.State != nil {
		f.State = string(*req.Params.State)
	}
	list, err := pipelines.ListRuns(ctx, s.Pool, f)
	if err != nil {
		return nil, err
	}
	out, err := convert[api.PipelineRunList](map[string]any{"items": list})
	if err != nil {
		return nil, err
	}
	return api.PipelineRunsList200JSONResponse(out), nil
}

// scopedPipelineRun reads pipeline run id with its steps and checks that the request's scope reaches its project.
func (s *Server) scopedPipelineRun(ctx context.Context, id string) (pipelines.Run, error) {
	r, err := pipelines.Get(ctx, s.Pool, id)
	if err != nil {
		return pipelines.Run{}, err
	}
	return r, auth.CheckProject(ctx, r.ProjectID)
}

// PipelineRunsGet implements pipelineRuns.get.
func (s *Server) PipelineRunsGet(ctx context.Context, req api.PipelineRunsGetRequestObject) (api.PipelineRunsGetResponseObject, error) {
	r, err := s.scopedPipelineRun(ctx, req.Id)
	if err != nil {
		return nil, err
	}
	body, err := convert[api.PipelineRun](r)
	if err != nil {
		return nil, err
	}
	etag := commands.ETag(r.Rev)
	return api.PipelineRunsGet200JSONResponse{Body: body, Headers: api.PipelineRunsGet200ResponseHeaders{ETag: &etag}}, nil
}

// PipelineRunsCancel implements pipelineRuns.cancel.
func (s *Server) PipelineRunsCancel(ctx context.Context, req api.PipelineRunsCancelRequestObject) (api.PipelineRunsCancelResponseObject, error) {
	r, err := s.scopedPipelineRun(ctx, req.Id)
	if err != nil {
		return nil, err
	}
	rev, err := commands.ParseIfMatch(req.Params.IfMatch)
	if err != nil {
		return nil, err
	}
	ctx = commands.WithProject(ctx, r.ProjectID)
	cmd := command(ctx, "pipelineRuns.cancel", req.Params.IdempotencyKey, req.Params.DryRun)
	return s.run(ctx, cmd, func(ctx context.Context, tx pgx.Tx) (commands.Result, []events.Draft, error) {
		run, drafts, err := s.Pipelines.Cancel(ctx, tx, req.Id, rev)
		if err != nil {
			return commands.Result{}, nil, err
		}
		return commands.Result{Status: http.StatusOK, Body: run, ETag: commands.ETag(run.Rev)}, drafts, nil
	})
}

// PipelineRunsRetry implements pipelineRuns.retry.
func (s *Server) PipelineRunsRetry(ctx context.Context, req api.PipelineRunsRetryRequestObject) (api.PipelineRunsRetryResponseObject, error) {
	r, err := s.scopedPipelineRun(ctx, req.Id)
	if err != nil {
		return nil, err
	}
	rev, err := commands.ParseIfMatch(req.Params.IfMatch)
	if err != nil {
		return nil, err
	}
	var in pipelines.RetryInput
	if req.Body != nil {
		in.Step, in.BatchScale = deref(req.Body.Step), req.Body.BatchScale
	}
	ctx = commands.WithProject(ctx, r.ProjectID)
	// A retry runs the steps not done yet again: their GPU time is weighed against the budget like a new run's.
	hours, unknown, err := pipelines.RetryEstimate(ctx, s.Pool, req.Id)
	if err != nil {
		return nil, err
	}
	ctx = commands.WithEstimate(ctx, policy.Estimate{GPUHours: hours, Unknown: unknown})
	ctx = commands.WithContinuation(ctx, req.Id) // the run's approval may cover the retry (approvals.Inherit)
	cmd := command(ctx, "pipelineRuns.retry", req.Params.IdempotencyKey, req.Params.DryRun)
	return s.run(ctx, cmd, func(ctx context.Context, tx pgx.Tx) (commands.Result, []events.Draft, error) {
		run, drafts, err := s.Pipelines.Retry(ctx, tx, req.Id, rev, in)
		if err != nil {
			return commands.Result{}, nil, err
		}
		return commands.Result{Status: http.StatusOK, Body: run, ETag: commands.ETag(run.Rev)}, drafts, nil
	})
}

// PipelineRunsWait implements pipelineRuns.wait: the run once it ends, or as it is when the timeout passes.
func (s *Server) PipelineRunsWait(ctx context.Context, req api.PipelineRunsWaitRequestObject) (api.PipelineRunsWaitResponseObject, error) {
	if _, err := s.scopedPipelineRun(ctx, req.Id); err != nil {
		return nil, err
	}
	timeout := 30
	if req.Params.Timeout != nil {
		timeout = *req.Params.Timeout
	}
	r, err := s.Pipelines.Wait(ctx, req.Id, time.Duration(timeout)*time.Second, waitPoll)
	if err != nil {
		return nil, err
	}
	body, err := convert[api.PipelineRun](r)
	if err != nil {
		return nil, err
	}
	etag := commands.ETag(r.Rev)
	return api.PipelineRunsWait200JSONResponse{Body: body, Headers: api.PipelineRunsWait200ResponseHeaders{ETag: &etag}}, nil
}
