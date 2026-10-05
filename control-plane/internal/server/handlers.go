package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	"github.com/usunrise88/cadence/control-plane/internal/api"
	"github.com/usunrise88/cadence/control-plane/internal/auth"
	"github.com/usunrise88/cadence/control-plane/internal/commands"
	"github.com/usunrise88/cadence/control-plane/internal/events"
	"github.com/usunrise88/cadence/control-plane/internal/help"
	"github.com/usunrise88/cadence/control-plane/internal/problems"
	"github.com/usunrise88/cadence/control-plane/internal/projects"
	"github.com/usunrise88/cadence/control-plane/internal/storage"
)

// ---------------------------------------------------------------- commands

// command builds the pipeline input from the request context: actor, key, dry run, fingerprint and the route's
// path parameters (policy rules can match them, e.g. aliases.set name=baseline).
func command(ctx context.Context, op, key string, dryRun *bool) commands.Command {
	actor, _ := auth.FromContext(ctx)
	cmd := commands.Command{
		Operation: op, Actor: actor, IdempotencyKey: key, DryRun: dryRun != nil && *dryRun,
		RequestHash: commands.RequestHash(ctx),
	}
	if rc := chi.RouteContext(ctx); rc != nil && len(rc.URLParams.Keys) > 0 {
		cmd.PathParams = make(map[string]string, len(rc.URLParams.Keys))
		for i, k := range rc.URLParams.Keys {
			v, err := url.PathUnescape(rc.URLParams.Values[i])
			if err != nil {
				v = rc.URLParams.Values[i]
			}
			cmd.PathParams[k] = v
		}
	}
	return cmd
}

// withProject resolves the project slug of a project command and names it to the pipeline (commands.WithProject).
func (s *Server) withProject(ctx context.Context, slug string) (context.Context, error) {
	p, err := projects.Get(ctx, s.Pool, slug)
	if err != nil {
		return ctx, err
	}
	return commands.WithProject(ctx, p.ID), nil
}

// commandResponse writes a pipeline response verbatim; it satisfies the response interface of every command, so
// a first run and an idempotent replay produce the same bytes.
type commandResponse commands.Response

func (c commandResponse) write(w http.ResponseWriter) error {
	for k, v := range c.Header {
		w.Header()[k] = v
	}
	w.WriteHeader(c.Status)
	_, err := w.Write(c.Body)
	return err
}

func (c commandResponse) VisitWorkspacesSetResponse(w http.ResponseWriter) error { return c.write(w) }
func (c commandResponse) VisitApprovalsApproveResponse(w http.ResponseWriter) error {
	return c.write(w)
}
func (c commandResponse) VisitApprovalsDenyResponse(w http.ResponseWriter) error { return c.write(w) }
func (c commandResponse) VisitJobsCancelResponse(w http.ResponseWriter) error    { return c.write(w) }

// ---------------------------------------------------------------- projects

// ProjectsList implements projects.list.
func (s *Server) ProjectsList(ctx context.Context, req api.ProjectsListRequestObject) (api.ProjectsListResponseObject, error) {
	scope, _ := auth.ScopeFromContext(ctx)
	list, err := projects.List(ctx, s.Pool, req.Params.Archived != nil && *req.Params.Archived)
	if err != nil {
		return nil, err
	}
	items := make([]api.Project, 0, len(list))
	for _, p := range list {
		if !scope.AllowsProject(p.ID) {
			continue
		}
		items = append(items, apiProject(p))
	}
	return api.ProjectsList200JSONResponse{Items: items}, nil
}

// ProjectsGet implements projects.get.
func (s *Server) ProjectsGet(ctx context.Context, req api.ProjectsGetRequestObject) (api.ProjectsGetResponseObject, error) {
	p, err := scopedProject(ctx, s.Pool, req.P)
	if err != nil {
		return nil, err
	}
	etag := commands.ETag(p.Rev)
	return api.ProjectsGet200JSONResponse{Body: apiProject(p), Headers: api.ProjectsGet200ResponseHeaders{ETag: &etag}}, nil
}

// ---------------------------------------------------------------- me and workspaces

// MeGet implements me.get.
func (s *Server) MeGet(ctx context.Context, _ api.MeGetRequestObject) (api.MeGetResponseObject, error) {
	a, ok := auth.FromContext(ctx)
	if !ok {
		return nil, problems.Unauthenticated.New("sign in or send a Bearer token")
	}
	return api.MeGet200JSONResponse(apiActor(a)), nil
}

func apiActor(a auth.Actor) api.Actor {
	out := api.Actor{Kind: api.ActorKind(a.Kind), Id: a.ID}
	if a.Name != "" {
		out.Name = &a.Name
	}
	if a.SessionID != "" {
		out.SessionId = &a.SessionID
	}
	if a.Channel != "" {
		ch := api.ActorChannel(a.Channel)
		out.Channel = &ch
	}
	return out
}

// WorkspacesList implements workspaces.list.
func (s *Server) WorkspacesList(ctx context.Context, req api.WorkspacesListRequestObject) (api.WorkspacesListResponseObject, error) {
	actor, _ := auth.FromContext(ctx)
	p, err := scopedProject(ctx, s.Pool, req.P)
	if err != nil {
		return nil, err
	}
	list, err := projects.ListWorkspaces(ctx, s.Pool, actor.ID, p.ID)
	if err != nil {
		return nil, err
	}
	items := make([]api.WorkspaceSummary, 0, len(list))
	for _, w := range list {
		items = append(items, api.WorkspaceSummary{Name: w.Name, Rev: w.Rev, UpdatedAt: w.UpdatedAt})
	}
	return api.WorkspacesList200JSONResponse{Items: items}, nil
}

// WorkspacesGet implements workspaces.get.
func (s *Server) WorkspacesGet(ctx context.Context, req api.WorkspacesGetRequestObject) (api.WorkspacesGetResponseObject, error) {
	actor, _ := auth.FromContext(ctx)
	p, err := scopedProject(ctx, s.Pool, req.P)
	if err != nil {
		return nil, err
	}
	w, err := projects.GetWorkspace(ctx, s.Pool, actor.ID, p.ID, req.Name)
	if err != nil {
		return nil, err
	}
	body, err := apiWorkspace(w)
	if err != nil {
		return nil, err
	}
	etag := commands.ETag(w.Rev)
	return api.WorkspacesGet200JSONResponse{Body: body, Headers: api.WorkspacesGet200ResponseHeaders{ETag: &etag}}, nil
}

// WorkspacesSet implements workspaces.set.
func (s *Server) WorkspacesSet(ctx context.Context, req api.WorkspacesSetRequestObject) (api.WorkspacesSetResponseObject, error) {
	var ifMatch *int
	if req.Params.IfMatch != nil {
		rev, err := commands.ParseIfMatch(*req.Params.IfMatch)
		if err != nil {
			return nil, err
		}
		ifMatch = &rev
	}
	layout, err := json.Marshal(req.Body.Layout)
	if err != nil {
		return nil, fmt.Errorf("marshal layout: %w", err)
	}
	panels, err := json.Marshal(req.Body.Panels)
	if err != nil {
		return nil, fmt.Errorf("marshal panels: %w", err)
	}
	in := projects.WorkspaceInput{SchemaVersion: req.Body.SchemaVersion, Layout: layout, Panels: panels}
	ctx, err = s.withProject(ctx, req.P)
	if err != nil {
		return nil, err
	}
	cmd := command(ctx, "workspaces.set", req.Params.IdempotencyKey, req.Params.DryRun)
	resp, err := s.Pipeline.Run(ctx, cmd, func(ctx context.Context, tx pgx.Tx) (commands.Result, []events.Draft, error) {
		p, err := scopedProject(ctx, tx, req.P)
		if err != nil {
			return commands.Result{}, nil, err
		}
		w, drafts, err := projects.SetWorkspace(ctx, tx, cmd.Actor.ID, p.ID, req.Name, ifMatch, in)
		if err != nil {
			return commands.Result{}, nil, err
		}
		body, err := apiWorkspace(w)
		if err != nil {
			return commands.Result{}, nil, err
		}
		return commands.Result{Status: http.StatusOK, Body: body, ETag: commands.ETag(w.Rev)}, drafts, nil
	})
	if err != nil {
		return nil, err
	}
	return commandResponse(resp), nil
}

func apiWorkspace(w projects.Workspace) (api.Workspace, error) {
	out := api.Workspace{Id: w.ID, Name: w.Name, SchemaVersion: w.SchemaVersion, Rev: w.Rev, UpdatedAt: w.UpdatedAt}
	if err := json.Unmarshal(w.Layout, &out.Layout); err != nil {
		return api.Workspace{}, fmt.Errorf("decode workspace layout: %w", err)
	}
	if err := json.Unmarshal(w.Panels, &out.Panels); err != nil {
		return api.Workspace{}, fmt.Errorf("decode workspace panels: %w", err)
	}
	return out, nil
}

// ---------------------------------------------------------------- help

// HelpSearch implements help.search.
func (s *Server) HelpSearch(_ context.Context, req api.HelpSearchRequestObject) (api.HelpSearchResponseObject, error) {
	limit := 20
	if req.Params.Limit != nil {
		limit = *req.Params.Limit
	}
	found := s.Help.Search(deref(req.Params.Q), deref(req.Params.Context), limit)
	items := make([]api.HelpHit, 0, len(found))
	for _, a := range found {
		items = append(items, api.HelpHit{Id: a.ID, Title: a.Title, Section: a.Section, Summary: optional(a.Summary)})
	}
	return api.HelpSearch200JSONResponse{Items: items}, nil
}

// HelpGet implements help.get.
func (s *Server) HelpGet(_ context.Context, req api.HelpGetRequestObject) (api.HelpGetResponseObject, error) {
	a, ok := s.Help.Get(req.Id)
	if !ok {
		return nil, problems.NotFound.New("no help article %q", req.Id)
	}
	return api.HelpGet200JSONResponse(apiArticle(a)), nil
}

func apiArticle(a help.Article) api.HelpArticle {
	out := api.HelpArticle{
		Id: a.ID, Title: a.Title, Section: api.HelpArticleSection(a.Section), Summary: optional(a.Summary), Body: a.Body,
	}
	if len(a.Contexts) > 0 {
		out.Contexts = &a.Contexts
	}
	return out
}

// ---------------------------------------------------------------- helpers

func deref[T any](p *T) T {
	var zero T
	if p == nil {
		return zero
	}
	return *p
}

func optional(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// optionalFloat is v, or nil for zero.
func optionalFloat(v float64) *float64 {
	if v == 0 {
		return nil
	}
	return &v
}

func writeJSON(w http.ResponseWriter, r *http.Request, log *slog.Logger, status int, v any) {
	b, err := json.Marshal(v)
	if err != nil {
		problems.Write(w, r, log, fmt.Errorf("marshal response: %w", err))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(append(b, '\n'))
}

// scopedProject reads the project with slug and checks that the request's scope reaches it (403 otherwise).
func scopedProject(ctx context.Context, q storage.Querier, slug string) (projects.Project, error) {
	p, err := projects.Get(ctx, q, slug)
	if err != nil {
		return projects.Project{}, err
	}
	if err := auth.CheckProject(ctx, p.ID); err != nil {
		return projects.Project{}, err
	}
	return p, nil
}
