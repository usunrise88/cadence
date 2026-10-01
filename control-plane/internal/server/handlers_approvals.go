package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/usunrise88/cadence/control-plane/internal/api"
	"github.com/usunrise88/cadence/control-plane/internal/approvals"
	"github.com/usunrise88/cadence/control-plane/internal/audit"
	"github.com/usunrise88/cadence/control-plane/internal/auth"
	"github.com/usunrise88/cadence/control-plane/internal/commands"
	"github.com/usunrise88/cadence/control-plane/internal/events"
	"github.com/usunrise88/cadence/control-plane/internal/jobs"
	"github.com/usunrise88/cadence/control-plane/internal/policy"
	"github.com/usunrise88/cadence/control-plane/internal/problems"
	"github.com/usunrise88/cadence/control-plane/internal/projects"
)

// Stream C: approvals, the audit log and jobs.

// ---------------------------------------------------------------- approvals

// ApprovalsList implements approvals.list.
func (s *Server) ApprovalsList(ctx context.Context, req api.ApprovalsListRequestObject) (api.ApprovalsListResponseObject, error) {
	projectID, err := s.projectID(ctx, deref(req.Params.Project))
	if err != nil {
		return nil, err
	}
	if projectID, err = narrowToScope(ctx, projectID); err != nil {
		return nil, err
	}
	list, err := approvals.List(ctx, s.Pool, approvals.Filter{
		State: string(deref(req.Params.State)), ProjectID: projectID, Limit: deref(req.Params.Limit),
	})
	if err != nil {
		return nil, err
	}
	out := api.ApprovalList{Items: make([]api.Approval, 0, len(list))}
	for _, a := range list {
		v, err := convert[api.Approval](approvals.JSON(a))
		if err != nil {
			return nil, err
		}
		out.Items = append(out.Items, v)
	}
	return api.ApprovalsList200JSONResponse(out), nil
}

// ApprovalsGet implements approvals.get.
func (s *Server) ApprovalsGet(ctx context.Context, req api.ApprovalsGetRequestObject) (api.ApprovalsGetResponseObject, error) {
	a, err := approvals.Get(ctx, s.Pool, req.Id)
	if err != nil {
		return nil, err
	}
	if err := checkItemProject(ctx, a.ProjectID); err != nil {
		return nil, err
	}
	v, err := convert[api.Approval](approvals.JSON(a))
	if err != nil {
		return nil, err
	}
	etag := commands.ETag(a.Rev)
	return api.ApprovalsGet200JSONResponse{Body: v, Headers: api.ApprovalsGet200ResponseHeaders{ETag: &etag}}, nil
}

// ApprovalsApprove implements approvals.approve: in one transaction it replays the stored request as its original
// actor (in a savepoint, with the approval id in the context) and records the decision with the replay's answer.
func (s *Server) ApprovalsApprove(ctx context.Context, req api.ApprovalsApproveRequestObject) (api.ApprovalsApproveResponseObject, error) {
	if err := auth.CheckAll(ctx); err != nil {
		return nil, err
	}
	rev, err := commands.ParseIfMatch(req.Params.IfMatch)
	if err != nil {
		return nil, err
	}
	grant, note := approvals.GrantOnce, ""
	if req.Body != nil {
		if req.Body.Grant != nil {
			grant = string(*req.Body.Grant)
		}
		note = deref(req.Body.Note)
	}
	ctx, err = s.withApproval(ctx, req.Id)
	if err != nil {
		return nil, err
	}
	cmd := command(ctx, "approvals.approve", req.Params.IdempotencyKey, req.Params.DryRun)
	resp, err := s.Pipeline.Run(ctx, cmd, func(ctx context.Context, tx pgx.Tx) (commands.Result, []events.Draft, error) {
		a, err := approvals.Lock(ctx, tx, req.Id, rev)
		if err != nil {
			return commands.Result{}, nil, err
		}
		if err := checkDecider(cmd.Actor, a); err != nil {
			return commands.Result{}, nil, err
		}
		if cmd.DryRun { // nothing replays in a dry run: answer the approval as it stands
			return approvalResult(a, nil)
		}
		var result *approvals.Result
		if a.Kind != approvals.KindAgentPermission { // an agent permission is answered to the agent, never replayed
			if result, err = s.replayApproved(ctx, tx, a); err != nil {
				return commands.Result{}, nil, err
			}
		}
		a, drafts, err := approvals.Decide(ctx, tx, a, approvals.Decision{
			By: cmd.Actor, Approve: true, Grant: grant, Note: note, Result: result,
		})
		if err != nil {
			return commands.Result{}, nil, err
		}
		more, err := s.sessions.ApprovalDecided(ctx, tx, a)
		return approvalResult(a, append(drafts, more...), err)
	})
	if err != nil {
		return nil, err
	}
	return commandResponse(resp), nil
}

// ApprovalsDeny implements approvals.deny.
func (s *Server) ApprovalsDeny(ctx context.Context, req api.ApprovalsDenyRequestObject) (api.ApprovalsDenyResponseObject, error) {
	if err := auth.CheckAll(ctx); err != nil {
		return nil, err
	}
	rev, err := commands.ParseIfMatch(req.Params.IfMatch)
	if err != nil {
		return nil, err
	}
	note := ""
	if req.Body != nil {
		note = deref(req.Body.Note)
	}
	ctx, err = s.withApproval(ctx, req.Id)
	if err != nil {
		return nil, err
	}
	cmd := command(ctx, "approvals.deny", req.Params.IdempotencyKey, req.Params.DryRun)
	resp, err := s.Pipeline.Run(ctx, cmd, func(ctx context.Context, tx pgx.Tx) (commands.Result, []events.Draft, error) {
		a, err := approvals.Lock(ctx, tx, req.Id, rev)
		if err != nil {
			return commands.Result{}, nil, err
		}
		if err := checkDecider(cmd.Actor, a); err != nil {
			return commands.Result{}, nil, err
		}
		a, drafts, err := approvals.Decide(ctx, tx, a, approvals.Decision{By: cmd.Actor, Note: note})
		if err != nil {
			return commands.Result{}, nil, err
		}
		more, err := s.sessions.ApprovalDecided(ctx, tx, a)
		return approvalResult(a, append(drafts, more...), err)
	})
	if err != nil {
		return nil, err
	}
	return commandResponse(resp), nil
}

// withApproval names the approval's project to the pipeline.
func (s *Server) withApproval(ctx context.Context, id string) (context.Context, error) {
	a, err := approvals.Get(ctx, s.Pool, id)
	if err != nil {
		return ctx, err
	}
	return commands.WithProject(ctx, a.ProjectID), nil
}

// checkDecider refuses decisions by anyone but a person (the policy engine already denies agents; this holds even
// for a preset that forgot the rule).
func checkDecider(by auth.Actor, a approvals.Approval) error {
	if by.Kind != auth.KindUser {
		return problems.PolicyDenied.New("approvals are decided by a person, not by %s %s", by.Kind, by.ID)
	}
	return nil
}

func approvalResult(a approvals.Approval, drafts []events.Draft, errs ...error) (commands.Result, []events.Draft, error) {
	for _, err := range errs {
		if err != nil {
			return commands.Result{}, nil, err
		}
	}
	return commands.Result{Status: http.StatusOK, Body: approvals.JSON(a), ETag: commands.ETag(a.Rev)}, drafts, nil
}

// replayApproved sends the stored request through the API as its original actor, inside tx: the handler's pipeline
// sees the approval id (commands.WithReplay), runs in a savepoint and skips the policy. The context carries only
// the actor, the scope and the replay mark, so nothing of the approve request leaks into it.
func (s *Server) replayApproved(ctx context.Context, tx pgx.Tx, a approvals.Approval) (*approvals.Result, error) {
	rctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stop := context.AfterFunc(ctx, cancel)
	defer stop()
	rctx = commands.WithReplay(rctx, tx, a.ID)
	rctx = policy.WithScope(rctx, a.Scope)
	rctx = auth.WithActor(rctx, a.Actor)
	// The replay carries the credential scope the request had: a person reaches everything, an agent or a key
	// only its project (and the registry for reading).
	replayScope := auth.Scope{ProjectID: a.Scope.ProjectID, RegistryRead: a.Scope.RegistryRead, Preset: a.Scope.Preset}
	if a.Actor.Kind == auth.KindUser || a.Scope.ProjectID == "" {
		replayScope = auth.FullScope()
	}
	rctx = auth.WithScope(rctx, replayScope)

	target := a.Request.Path
	if a.Request.Query != "" {
		target += "?" + a.Request.Query
	}
	r, err := http.NewRequestWithContext(rctx, a.Request.Method, target, bytes.NewReader(a.Request.Body))
	if err != nil {
		return nil, fmt.Errorf("rebuild approved request: %w", err)
	}
	for k, v := range a.Request.Header {
		r.Header.Set(k, v)
	}
	r.RequestURI = ""
	rec := httptest.NewRecorder()
	s.replay.ServeHTTP(rec, r)
	return &approvals.Result{
		Status: rec.Code, Body: rec.Body.Bytes(), CommandID: rec.Header().Get(commands.HeaderCommandID),
	}, nil
}

// ---------------------------------------------------------------- audit

// AuditList implements audit.list. The admin's session reads everything; a credential scoped to one project reads
// that project's rows only (the evals grade a session through its project key), like approvals.list.
func (s *Server) AuditList(ctx context.Context, req api.AuditListRequestObject) (api.AuditListResponseObject, error) {
	projectID, err := s.projectID(ctx, deref(req.Params.Project))
	if err != nil {
		return nil, err
	}
	if projectID, err = narrowToScope(ctx, projectID); err != nil {
		return nil, err
	}
	list, next, err := audit.List(ctx, s.Pool, audit.Filter{
		ActorID: deref(req.Params.Actor), ProjectID: projectID, Operation: deref(req.Params.Operation),
		Before: deref(req.Params.Before), Limit: deref(req.Params.Limit),
	})
	if err != nil {
		return nil, err
	}
	out := api.AuditList{Items: make([]api.AuditEntry, 0, len(list)), Next: optional(next)}
	for _, e := range list {
		item := api.AuditEntry{
			Id: e.ID, Operation: e.Operation, Actor: apiActor(e.Actor), Outcome: e.Outcome, Status: e.Status, At: e.At,
			CommandId: optional(e.CommandID), Preset: optional(e.Preset), ProjectId: optional(e.ProjectID),
			Rule: optional(e.Rule),
		}
		if e.CommandID != "" || e.ToolCallID != "" || e.ApprovalID != "" {
			item.CausedBy = &api.AuditCause{CommandId: optional(e.CommandID), ToolCallId: optional(e.ToolCallID),
				ApprovalId: optional(e.ApprovalID)}
		}
		if len(e.Detail) > 0 {
			item.Detail = &e.Detail
		}
		out.Items = append(out.Items, item)
	}
	return api.AuditList200JSONResponse(out), nil
}

// ---------------------------------------------------------------- jobs

// JobsList implements jobs.list.
func (s *Server) JobsList(ctx context.Context, req api.JobsListRequestObject) (api.JobsListResponseObject, error) {
	p, err := projects.Get(ctx, s.Pool, req.P)
	if err != nil {
		return nil, err
	}
	if err := auth.CheckProject(ctx, p.ID); err != nil {
		return nil, err
	}
	list, err := jobs.List(ctx, s.Pool, p.ID, string(deref(req.Params.State)), deref(req.Params.Limit))
	if err != nil {
		return nil, err
	}
	out := api.JobList{Items: make([]api.Job, 0, len(list))}
	for _, j := range list {
		v, err := convert[api.Job](j.JSON())
		if err != nil {
			return nil, err
		}
		out.Items = append(out.Items, v)
	}
	return api.JobsList200JSONResponse(out), nil
}

// JobsGet implements jobs.get.
func (s *Server) JobsGet(ctx context.Context, req api.JobsGetRequestObject) (api.JobsGetResponseObject, error) {
	j, err := jobs.Get(ctx, s.Pool, req.Id)
	if err != nil {
		return nil, err
	}
	if err := checkItemProject(ctx, j.ProjectID); err != nil {
		return nil, err
	}
	v, err := convert[api.Job](j.JSON())
	if err != nil {
		return nil, err
	}
	etag := commands.ETag(j.Rev)
	return api.JobsGet200JSONResponse{Body: v, Headers: api.JobsGet200ResponseHeaders{ETag: &etag}}, nil
}

// JobsCancel implements jobs.cancel.
func (s *Server) JobsCancel(ctx context.Context, req api.JobsCancelRequestObject) (api.JobsCancelResponseObject, error) {
	rev, err := commands.ParseIfMatch(req.Params.IfMatch)
	if err != nil {
		return nil, err
	}
	cur, err := jobs.Get(ctx, s.Pool, req.Id)
	if err != nil {
		return nil, err
	}
	if err := checkItemProject(ctx, cur.ProjectID); err != nil {
		return nil, err
	}
	ctx = commands.WithProject(ctx, cur.ProjectID)
	resp, err := s.Pipeline.Run(ctx, command(ctx, "jobs.cancel", req.Params.IdempotencyKey, req.Params.DryRun),
		func(ctx context.Context, tx pgx.Tx) (commands.Result, []events.Draft, error) {
			if s.Jobs == nil {
				return commands.Result{}, nil, fmt.Errorf("no job service configured")
			}
			j, drafts, err := s.Jobs.Cancel(ctx, tx, req.Id, rev)
			if err != nil {
				return commands.Result{}, nil, err
			}
			return commands.Result{Status: http.StatusOK, Body: j.JSON(), ETag: commands.ETag(j.Rev)}, drafts, nil
		})
	if err != nil {
		return nil, err
	}
	return commandResponse(resp), nil
}

// waitPoll is how often jobs.wait re-reads the job.
const waitPoll = 200 * time.Millisecond

// JobsWait implements jobs.wait: the job once it ends, or as it is when the timeout passes.
func (s *Server) JobsWait(ctx context.Context, req api.JobsWaitRequestObject) (api.JobsWaitResponseObject, error) {
	timeout := 30
	if req.Params.Timeout != nil {
		timeout = *req.Params.Timeout
	}
	cur, err := jobs.Get(ctx, s.Pool, req.Id)
	if err != nil {
		return nil, err
	}
	if err := checkItemProject(ctx, cur.ProjectID); err != nil {
		return nil, err
	}
	j, err := jobs.Wait(ctx, s.Pool, req.Id, time.Duration(timeout)*time.Second, waitPoll)
	if err != nil {
		return nil, err
	}
	v, err := convert[api.Job](j.JSON())
	if err != nil {
		return nil, err
	}
	etag := commands.ETag(j.Rev)
	return api.JobsWait200JSONResponse{Body: v, Headers: api.JobsWait200ResponseHeaders{ETag: &etag}}, nil
}

// ---------------------------------------------------------------- helpers

// projectID resolves a project filter given as a slug or a prj_ id; "" stays "".
func (s *Server) projectID(ctx context.Context, project string) (string, error) {
	if project == "" || strings.HasPrefix(project, "prj_") {
		return project, nil
	}
	p, err := projects.Get(ctx, s.Pool, project)
	if err != nil {
		return "", err
	}
	return p.ID, nil
}

// convert maps a domain view onto its generated contract type through JSON (their JSON forms are the same).
func convert[T any](v any) (T, error) {
	var out T
	b, err := json.Marshal(v)
	if err == nil {
		err = json.Unmarshal(b, &out)
	}
	if err != nil {
		return out, fmt.Errorf("convert %T: %w", v, err)
	}
	return out, nil
}

// narrowToScope narrows a list filter to the credential's project: a scoped credential sees only its own project,
// whatever filter it asked for; a full-scope credential keeps the filter.
func narrowToScope(ctx context.Context, projectID string) (string, error) {
	sc, ok := auth.ScopeFromContext(ctx)
	if !ok || sc.All {
		return projectID, nil
	}
	if projectID != "" && projectID != sc.ProjectID {
		return "", auth.CheckProject(ctx, projectID)
	}
	if sc.ProjectID == "" {
		return "", auth.CheckAll(ctx)
	}
	return sc.ProjectID, nil
}

// checkItemProject authorises reading an item by id: a project item needs its project in scope, an item of no
// project (instance-wide) needs full scope.
func checkItemProject(ctx context.Context, projectID string) error {
	if projectID == "" {
		return auth.CheckAll(ctx)
	}
	return auth.CheckProject(ctx, projectID)
}
