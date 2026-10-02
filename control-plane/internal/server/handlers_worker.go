package server

// The worker protocol (tag worker, docs/review/2026-09-30-phase-2-plan.md "Worker protocol"), the step queue's
// commands (jobs.pause | resume | edit, queueEntries.list), job logs and the registry reads of what workers publish
// (runtimes, model families, step kinds). The protocol itself lives in internal/workers; scheduling in
// internal/queue.

import (
	"context"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/usunrise88/cadence/control-plane/internal/api"
	"github.com/usunrise88/cadence/control-plane/internal/auth"
	"github.com/usunrise88/cadence/control-plane/internal/commands"
	"github.com/usunrise88/cadence/control-plane/internal/credentials"
	"github.com/usunrise88/cadence/control-plane/internal/events"
	"github.com/usunrise88/cadence/control-plane/internal/jobs"
	"github.com/usunrise88/cadence/control-plane/internal/policy"
	"github.com/usunrise88/cadence/control-plane/internal/problems"
	"github.com/usunrise88/cadence/control-plane/internal/registry"
	"github.com/usunrise88/cadence/control-plane/internal/steps"
	"github.com/usunrise88/cadence/control-plane/internal/storage"
	"github.com/usunrise88/cadence/control-plane/internal/telemetry"
	"github.com/usunrise88/cadence/control-plane/internal/workers"
)

// workerPathPrefix starts every worker protocol path (relative to /api).
const workerPathPrefix = "/worker-"

func isWorkerPath(p string) bool {
	return strings.HasPrefix(strings.TrimPrefix(p, APIPrefix), workerPathPrefix)
}

// workerOnly keeps the worker credential (cwk_) inside the worker protocol: any other operation refuses it.
func (s *Server) workerOnly(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if p, ok := auth.PrincipalFromContext(r.Context()); ok && p.CredentialKind == credentials.KindWorker && !isWorkerPath(r.URL.Path) {
			s.writeProblem(w, r, problems.Forbidden.New("a worker token (cwk_…) speaks the worker protocol only"))
			return
		}
		next.ServeHTTP(w, r)
	})
}

// skipForUploads bypasses mw for artifact uploads, whose bodies may be far larger than a command's (the body hash
// middleware reads and caps every body; uploads are verified against their content hash instead).
func skipForUploads(mw func(http.Handler) http.Handler) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		wrapped := mw(next)
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodPut && strings.HasPrefix(strings.TrimPrefix(r.URL.Path, APIPrefix), "/worker-artifacts/") {
				next.ServeHTTP(w, r)
				return
			}
			wrapped.ServeHTTP(w, r)
		})
	}
}

// workerCaller is the worker credential of the request and its host, or the fixed actor of tests and development
// (any host).
func (s *Server) workerCaller(ctx context.Context) (workers.Caller, error) {
	if s.Workers == nil {
		return workers.Caller{}, problems.NotImplemented.New("this control plane runs no worker protocol")
	}
	p, ok := auth.PrincipalFromContext(ctx)
	if !ok {
		return workers.Caller{}, problems.Unauthenticated.New("send the worker token (cwk_…) as a Bearer token")
	}
	if p.CredentialID == "" && s.Actor.ID != "" {
		return workers.Caller{Actor: p.Actor}, nil
	}
	if p.CredentialKind != credentials.KindWorker {
		return workers.Caller{}, problems.Forbidden.New("only a worker (a cwk_… token) speaks the worker protocol")
	}
	c, err := credentials.Get(ctx, s.Pool, p.CredentialID)
	if err != nil {
		return workers.Caller{}, err
	}
	return workers.Caller{Actor: p.Actor, CredentialID: c.ID, Host: c.Subject}, nil
}

// WorkerRegistrationsNew implements workerRegistrations.new.
func (s *Server) WorkerRegistrationsNew(ctx context.Context, req api.WorkerRegistrationsNewRequestObject) (api.WorkerRegistrationsNewResponseObject, error) {
	c, err := s.workerCaller(ctx)
	if err != nil {
		return nil, err
	}
	in, err := convert[workers.Registration](req.Body)
	if err != nil {
		return nil, err
	}
	var w workers.Worker
	// Workers that register for the first time at once race on the same registry and worker rows; the loser runs
	// again and finds the winner's rows (registration is idempotent).
	err = storage.BeginRetry(ctx, s.Pool, 3, func(tx pgx.Tx) error {
		var drafts []events.Draft
		if w, drafts, err = s.Workers.Register(ctx, tx, c, in); err != nil {
			return err
		}
		return events.Append(ctx, tx, c.Actor, nil, drafts)
	})
	if err != nil {
		return nil, err
	}
	s.Log.InfoContext(ctx, "worker registered", "worker", w.ID, "host", w.Host, "step_kinds", len(w.StepKinds))
	out, err := convert[api.Worker](w)
	return api.WorkerRegistrationsNew200JSONResponse(out), err
}

// WorkerLeasesClaim implements workerLeases.claim.
func (s *Server) WorkerLeasesClaim(ctx context.Context, req api.WorkerLeasesClaimRequestObject) (api.WorkerLeasesClaimResponseObject, error) {
	c, err := s.workerCaller(ctx)
	if err != nil {
		return nil, err
	}
	in, err := convert[workers.Claim](req.Body)
	if err != nil {
		return nil, err
	}
	g, err := s.Workers.Claim(ctx, c, in)
	if err != nil || g == nil {
		return api.WorkerLeasesClaim200JSONResponse{}, err
	}
	lease, err := convert[api.Lease](g)
	return api.WorkerLeasesClaim200JSONResponse{Lease: &lease}, err
}

// WorkerLeasesReport implements workerLeases.report.
func (s *Server) WorkerLeasesReport(ctx context.Context, req api.WorkerLeasesReportRequestObject) (api.WorkerLeasesReportResponseObject, error) {
	c, err := s.workerCaller(ctx)
	if err != nil {
		return nil, err
	}
	in, err := convert[workers.Report](req.Body)
	if err != nil {
		return nil, err
	}
	ack, err := s.Workers.Report(ctx, c, req.Id, in)
	if err != nil {
		return nil, err
	}
	out := api.WorkerLeasesReport200JSONResponse{Stop: ack.Stop}
	if ack.Reason != "" {
		r := api.WorkerReportAckReason(ack.Reason)
		out.Reason = &r
	}
	return out, nil
}

// WorkerLogsNew implements workerLogs.new.
func (s *Server) WorkerLogsNew(ctx context.Context, req api.WorkerLogsNewRequestObject) (api.WorkerLogsNewResponseObject, error) {
	c, err := s.workerCaller(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.Workers.AppendLogs(ctx, c, req.Id, req.Body); err != nil {
		return nil, err
	}
	return api.WorkerLogsNew204Response{}, nil
}

// WorkerMetricsNew implements workerMetrics.new.
func (s *Server) WorkerMetricsNew(ctx context.Context, req api.WorkerMetricsNewRequestObject) (api.WorkerMetricsNewResponseObject, error) {
	c, err := s.workerCaller(ctx)
	if err != nil {
		return nil, err
	}
	pts, err := convert[[]telemetry.Point](req.Body.Points)
	if err != nil {
		return nil, err
	}
	if err := s.Workers.AppendMetrics(ctx, c, req.Id, pts); err != nil {
		return nil, err
	}
	return api.WorkerMetricsNew204Response{}, nil
}

// WorkerOutputsNew implements workerOutputs.new.
func (s *Server) WorkerOutputsNew(ctx context.Context, req api.WorkerOutputsNewRequestObject) (api.WorkerOutputsNewResponseObject, error) {
	c, err := s.workerCaller(ctx)
	if err != nil {
		return nil, err
	}
	in, err := convert[workers.Published](req.Body)
	if err != nil {
		return nil, err
	}
	if err := s.Workers.Publish(ctx, c, req.Id, in); err != nil {
		return nil, err
	}
	return api.WorkerOutputsNew204Response{}, nil
}

// WorkerLeasesRelease implements workerLeases.release.
func (s *Server) WorkerLeasesRelease(ctx context.Context, req api.WorkerLeasesReleaseRequestObject) (api.WorkerLeasesReleaseResponseObject, error) {
	c, err := s.workerCaller(ctx)
	if err != nil {
		return nil, err
	}
	o, err := convert[steps.Outcome](req.Body)
	if err != nil {
		return nil, err
	}
	if err := s.Workers.Release(ctx, c, req.Id, o); err != nil {
		return nil, err
	}
	return api.WorkerLeasesRelease204Response{}, nil
}

// WorkerArtifactsSet implements workerArtifacts.set.
func (s *Server) WorkerArtifactsSet(ctx context.Context, req api.WorkerArtifactsSetRequestObject) (api.WorkerArtifactsSetResponseObject, error) {
	if _, err := s.workerCaller(ctx); err != nil {
		return nil, err
	}
	if err := s.Workers.PutArtifact(req.Hash, req.Body); err != nil {
		return nil, err
	}
	return api.WorkerArtifactsSet204Response{}, nil
}

// ---------------------------------------------------------------- the step queue

func (c commandResponse) VisitJobsPauseResponse(w http.ResponseWriter) error  { return c.write(w) }
func (c commandResponse) VisitJobsResumeResponse(w http.ResponseWriter) error { return c.write(w) }
func (c commandResponse) VisitJobsEditResponse(w http.ResponseWriter) error   { return c.write(w) }

// jobCommand runs a queue command on job id: project auth through the job, the command pipeline, then a wake-up
// for waiting claims.
func (s *Server) jobCommand(ctx context.Context, op, id, ifMatch, key string, dryRun *bool,
	fn func(ctx context.Context, tx pgx.Tx, rev int) (jobs.Job, []events.Draft, error)) (commandResponse, error) {
	if s.Workers == nil {
		return commandResponse{}, problems.NotImplemented.New("this control plane runs no worker protocol")
	}
	rev, err := commands.ParseIfMatch(ifMatch)
	if err != nil {
		return commandResponse{}, err
	}
	cur, err := jobs.Get(ctx, s.Pool, id)
	if err != nil {
		return commandResponse{}, err
	}
	if err := checkItemProject(ctx, cur.ProjectID); err != nil {
		return commandResponse{}, err
	}
	ctx = commands.WithProject(ctx, cur.ProjectID)
	resp, err := s.run(ctx, command(ctx, op, key, dryRun), func(ctx context.Context, tx pgx.Tx) (commands.Result, []events.Draft, error) {
		j, drafts, err := fn(ctx, tx, rev)
		if err != nil {
			return commands.Result{}, nil, err
		}
		return commands.Result{Status: http.StatusOK, Body: j.JSON(), ETag: commands.ETag(j.Rev)}, drafts, nil
	})
	if err == nil {
		s.Workers.Wake()
	}
	return resp, err
}

// JobsPause implements jobs.pause.
func (s *Server) JobsPause(ctx context.Context, req api.JobsPauseRequestObject) (api.JobsPauseResponseObject, error) {
	return s.jobCommand(ctx, "jobs.pause", req.Id, req.Params.IfMatch, req.Params.IdempotencyKey, req.Params.DryRun,
		func(ctx context.Context, tx pgx.Tx, rev int) (jobs.Job, []events.Draft, error) {
			j, drafts, err := s.Workers.Pause(ctx, tx, req.Id, rev)
			return s.withRunStatus(ctx, tx, j, drafts, err)
		})
}

// JobsResume implements jobs.resume.
func (s *Server) JobsResume(ctx context.Context, req api.JobsResumeRequestObject) (api.JobsResumeResponseObject, error) {
	// Resuming puts the job back on a card: its GPU time is weighed against the budget like a new run's.
	hours, unknown, err := workers.ResumeEstimate(ctx, s.Pool, req.Id)
	if err != nil {
		return nil, err
	}
	ctx = commands.WithEstimate(ctx, policy.Estimate{GPUHours: hours, Unknown: unknown})
	return s.jobCommand(ctx, "jobs.resume", req.Id, req.Params.IfMatch, req.Params.IdempotencyKey, req.Params.DryRun,
		func(ctx context.Context, tx pgx.Tx, rev int) (jobs.Job, []events.Draft, error) {
			j, drafts, err := s.Workers.Resume(ctx, tx, req.Id, rev)
			return s.withRunStatus(ctx, tx, j, drafts, err)
		})
}

// JobsEdit implements jobs.edit (priority).
func (s *Server) JobsEdit(ctx context.Context, req api.JobsEditRequestObject) (api.JobsEditResponseObject, error) {
	return s.jobCommand(ctx, "jobs.edit", req.Id, req.Params.IfMatch, req.Params.IdempotencyKey, req.Params.DryRun,
		func(ctx context.Context, tx pgx.Tx, rev int) (jobs.Job, []events.Draft, error) {
			return s.Workers.Prioritize(ctx, tx, req.Id, rev, req.Body.Priority)
		})
}

// QueueEntriesList implements queueEntries.list: every project's entries for a full-scope caller, a scoped
// credential's own project's otherwise.
func (s *Server) QueueEntriesList(ctx context.Context, req api.QueueEntriesListRequestObject) (api.QueueEntriesListResponseObject, error) {
	projectID, err := s.projectID(ctx, deref(req.Params.Project))
	if err != nil {
		return nil, err
	}
	scope, _ := auth.ScopeFromContext(ctx)
	switch {
	case projectID != "":
		if err := auth.CheckProject(ctx, projectID); err != nil {
			return nil, err
		}
	case !scope.All:
		if scope.ProjectID == "" {
			return nil, auth.CheckAll(ctx)
		}
		projectID = scope.ProjectID
	}
	list, err := workers.Queue(ctx, s.Pool, projectID)
	if err != nil {
		return nil, err
	}
	out, err := convert[api.QueueEntryList](map[string]any{"items": list})
	return api.QueueEntriesList200JSONResponse(out), err
}

// JobLogsList implements jobLogs.list.
func (s *Server) JobLogsList(ctx context.Context, req api.JobLogsListRequestObject) (api.JobLogsListResponseObject, error) {
	if s.Workers == nil {
		return nil, problems.NotImplemented.New("this control plane runs no worker protocol")
	}
	j, err := jobs.Get(ctx, s.Pool, req.Id)
	if err != nil {
		return nil, err
	}
	if err := checkItemProject(ctx, j.ProjectID); err != nil {
		return nil, err
	}
	f := workers.LogFilter{Text: deref(req.Params.Text), After: deref(req.Params.After), Tail: deref(req.Params.Tail),
		Limit: deref(req.Params.Limit)}
	if req.Params.Level != nil {
		f.MinLevel = string(*req.Params.Level)
	}
	lines, next, err := s.Workers.ReadLogs(j.ID, f)
	if err != nil {
		return nil, err
	}
	out, err := convert[api.JobLogPage](map[string]any{"items": lines, "nextAfter": next})
	return api.JobLogsList200JSONResponse(out), err
}

// ---------------------------------------------------------------- runtimes, model families, step kinds

func publishedVersions[T any](ctx context.Context, q storage.Querier, f registry.Filter, field string) ([]T, error) {
	common, payloads, _, err := versions[map[string]any](ctx, q, f)
	if err != nil {
		return nil, err
	}
	out := make([]T, 0, len(common))
	for i, v := range common {
		doc, err := convert[map[string]any](v)
		if err != nil {
			return nil, err
		}
		doc[field] = payloads[i]
		t, err := convert[T](doc)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, nil
}

func (s *Server) runtimeVersions(ctx context.Context, f registry.Filter) ([]api.RuntimeVersion, error) {
	list, err := publishedVersions[api.RuntimeVersion](ctx, s.Pool, f, "runtime")
	if err != nil || s.Workers == nil {
		return list, err
	}
	ids := make([]string, 0, len(list))
	for _, v := range list {
		ids = append(ids, v.Id)
	}
	byVersion, err := s.Workers.ListWorkers(ctx, s.Pool, ids)
	if err != nil {
		return nil, err
	}
	for i := range list {
		ws, err := convert[[]api.Worker](byVersion[list[i].Id])
		if err != nil {
			return nil, err
		}
		if ws == nil {
			ws = []api.Worker{}
		}
		list[i].Workers = ws
	}
	return list, nil
}

// RuntimesList implements runtimes.list.
func (s *Server) RuntimesList(ctx context.Context, req api.RuntimesListRequestObject) (api.RuntimesListResponseObject, error) {
	if err := auth.CheckRegistryRead(ctx); err != nil {
		return nil, err
	}
	items, err := s.runtimeVersions(ctx, versionFilter(registry.KindRuntime, req.Params.Collection, req.Params.State))
	return api.RuntimesList200JSONResponse{Items: items}, err
}

// RuntimesGet implements runtimes.get.
func (s *Server) RuntimesGet(ctx context.Context, req api.RuntimesGetRequestObject) (api.RuntimesGetResponseObject, error) {
	if err := auth.CheckRegistryRead(ctx); err != nil {
		return nil, err
	}
	items, err := s.runtimeVersions(ctx, registry.Filter{Kind: registry.KindRuntime, IDs: []string{req.Id}})
	if err != nil {
		return nil, err
	}
	v, err := one(items, registry.KindRuntime, req.Id)
	return api.RuntimesGet200JSONResponse(v), err
}

// ModelFamiliesList implements modelFamilies.list.
func (s *Server) ModelFamiliesList(ctx context.Context, req api.ModelFamiliesListRequestObject) (api.ModelFamiliesListResponseObject, error) {
	if err := auth.CheckRegistryRead(ctx); err != nil {
		return nil, err
	}
	items, err := publishedVersions[api.ModelFamilyVersion](ctx, s.Pool,
		versionFilter(registry.KindModelFamily, req.Params.Collection, req.Params.State), "modelFamily")
	return api.ModelFamiliesList200JSONResponse{Items: items}, err
}

// ModelFamiliesGet implements modelFamilies.get.
func (s *Server) ModelFamiliesGet(ctx context.Context, req api.ModelFamiliesGetRequestObject) (api.ModelFamiliesGetResponseObject, error) {
	if err := auth.CheckRegistryRead(ctx); err != nil {
		return nil, err
	}
	items, err := publishedVersions[api.ModelFamilyVersion](ctx, s.Pool,
		registry.Filter{Kind: registry.KindModelFamily, IDs: []string{req.Id}}, "modelFamily")
	if err != nil {
		return nil, err
	}
	v, err := one(items, registry.KindModelFamily, req.Id)
	return api.ModelFamiliesGet200JSONResponse(v), err
}

// StepKindsList implements stepKinds.list.
func (s *Server) StepKindsList(ctx context.Context, req api.StepKindsListRequestObject) (api.StepKindsListResponseObject, error) {
	if err := auth.CheckRegistryRead(ctx); err != nil {
		return nil, err
	}
	items, err := publishedVersions[api.StepKindVersion](ctx, s.Pool,
		versionFilter(registry.KindStepKind, req.Params.Collection, req.Params.State), "stepKind")
	return api.StepKindsList200JSONResponse{Items: items}, err
}

// StepKindsGet implements stepKinds.get.
func (s *Server) StepKindsGet(ctx context.Context, req api.StepKindsGetRequestObject) (api.StepKindsGetResponseObject, error) {
	if err := auth.CheckRegistryRead(ctx); err != nil {
		return nil, err
	}
	items, err := publishedVersions[api.StepKindVersion](ctx, s.Pool,
		registry.Filter{Kind: registry.KindStepKind, IDs: []string{req.Id}}, "stepKind")
	if err != nil {
		return nil, err
	}
	v, err := one(items, registry.KindStepKind, req.Id)
	return api.StepKindsGet200JSONResponse(v), err
}

// f32 narrows an optional float to the contract's float32 fields.
func f32(p *float64) *float32 {
	if p == nil {
		return nil
	}
	v := float32(*p)
	return &v
}
