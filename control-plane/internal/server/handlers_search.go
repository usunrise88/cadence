package server

import (
	"context"
	"net/http"

	"github.com/jackc/pgx/v5"

	"github.com/usunrise88/cadence/control-plane/internal/api"
	"github.com/usunrise88/cadence/control-plane/internal/auth"
	"github.com/usunrise88/cadence/control-plane/internal/commands"
	"github.com/usunrise88/cadence/control-plane/internal/events"
	"github.com/usunrise88/cadence/control-plane/internal/problems"
	"github.com/usunrise88/cadence/control-plane/internal/projects"
	"github.com/usunrise88/cadence/control-plane/internal/search"
)

// ---------------------------------------------------------------- search (docs/spec/11-ui-panels.md "Search")

// searchKinds is what kind: accepts: the indexed kinds and their aliases.
var searchKinds = search.Kinds(search.Sources())

// ProjectsSearch implements projects.search (R1: search.query, bound to the project the caller works in).
func (s *Server) ProjectsSearch(ctx context.Context, req api.ProjectsSearchRequestObject) (api.ProjectsSearchResponseObject, error) {
	p, err := scopedProject(ctx, s.Pool, req.P)
	if err != nil {
		return nil, err
	}
	q, err := search.Parse(deref(req.Params.Q), searchKinds)
	if err != nil {
		return nil, err
	}
	if s.Scorers != nil { // fold the words as the index folded documents of their locale (language packs, R21)
		lang := ""
		if len(q.Langs) > 0 {
			lang = q.Langs[0]
		}
		fold, err := search.FoldFor(ctx, s.Pool, s.Scorers, p.ID, lang, q.Text)
		if err != nil {
			return nil, err
		}
		q.Refold(fold)
	}
	sreq, slugs, err := s.searchRequest(ctx, p, q)
	if err != nil {
		return nil, err
	}
	sreq.Limit = 50
	if req.Params.Limit != nil {
		sreq.Limit = *req.Params.Limit
	}
	res, err := search.Search(ctx, s.Pool, sreq)
	if err != nil {
		return nil, err
	}
	out := api.ProjectsSearch200JSONResponse{
		Q: q.Raw, Text: q.Text, Qualifiers: make([]api.SearchQualifier, 0, len(q.Qualifiers)),
		Groups: make([]api.SearchGroup, 0, len(res.Groups)), Total: res.Total, Truncated: res.Truncated,
	}
	for _, ql := range q.Qualifiers {
		out.Qualifiers = append(out.Qualifiers, api.SearchQualifier{Field: ql.Field, Op: api.SearchQualifierOp(ql.Op), Value: ql.Value, Raw: ql.Raw})
	}
	for _, g := range res.Groups {
		items := make([]api.SearchHit, 0, len(g.Hits))
		for _, h := range g.Hits {
			items = append(items, apiSearchHit(h, slugs))
		}
		out.Groups = append(out.Groups, api.SearchGroup{Kind: g.Kind, Items: items})
	}
	return out, nil
}

// searchRequest resolves what the caller may see from its credential and the query's scope qualifiers:
//
//   - no scope qualifier: the current project, the registry (with registry read) and help; instance-wide work
//     (jobs and approvals without a project) for full scope only;
//   - project:<slug>: those projects' work, each checked against the credential (403 otherwise);
//   - scope:project: the current project only; scope:registry: the registry only (403 without registry read);
//   - scope:all: every project the credential reaches — for a project-bound token that is still its own project —
//     plus the registry, instance-wide work (full scope) and help.
func (s *Server) searchRequest(ctx context.Context, cur projects.Project, q search.Query) (search.Request, map[string]string, error) {
	sc, _ := auth.ScopeFromContext(ctx)
	req := search.Request{Query: q, CurrentProjectID: cur.ID}
	slugs := map[string]string{cur.ID: cur.Slug}
	v := &req.Visible
	switch {
	case len(q.Projects) > 0:
		if q.Scope == search.ScopeAll || q.Scope == search.ScopeProject {
			return req, nil, problems.InvalidQuery.New("project: already names the projects; drop scope:%s", q.Scope)
		}
		for _, slug := range q.Projects {
			p, err := scopedProject(ctx, s.Pool, slug)
			if err != nil {
				return req, nil, err
			}
			v.ProjectIDs = append(v.ProjectIDs, p.ID)
			slugs[p.ID] = p.Slug
		}
		req.AliasProjectIDs = v.ProjectIDs
		if q.Scope == search.ScopeRegistry {
			if err := auth.CheckRegistryRead(ctx); err != nil {
				return req, nil, err
			}
			v.Registry = true
		}
	case q.Scope == search.ScopeProject:
		v.ProjectIDs = []string{cur.ID}
	case q.Scope == search.ScopeRegistry:
		if err := auth.CheckRegistryRead(ctx); err != nil {
			return req, nil, err
		}
		v.Registry = true
	case q.Scope == search.ScopeAll:
		if sc.All {
			v.AllProjects, v.Instance = true, true
		} else {
			v.ProjectIDs = []string{cur.ID}
		}
		v.Registry, v.Help = sc.AllowsRegistryRead(), true
	default:
		v.ProjectIDs = []string{cur.ID}
		v.Registry, v.Instance, v.Help = sc.AllowsRegistryRead(), sc.All, true
	}
	if req.AliasProjectIDs == nil && !v.AllProjects {
		req.AliasProjectIDs = []string{cur.ID}
	}
	return req, slugs, nil
}

func apiSearchHit(h search.Hit, slugs map[string]string) api.SearchHit {
	out := api.SearchHit{
		Kind: h.Kind, Id: h.ID, Title: h.Title, Ref: h.Ref, Scope: api.SearchHitScope(h.Scope), Tags: h.Tags,
		UpdatedAt: h.UpdatedAt, Snippet: optional(h.Snippet), Status: optional(h.Status), Lang: optional(h.Lang),
		ProjectId: optional(h.ProjectID),
	}
	if out.Tags == nil {
		out.Tags = []string{}
	}
	slug := h.ProjectSlug
	if slug == "" {
		slug = slugs[h.ProjectID]
	}
	out.Project = optional(slug)
	if h.Actor != nil {
		a := apiActor(*h.Actor)
		out.Actor = &a
	}
	if len(h.Numbers) > 0 {
		n := h.Numbers
		out.Numbers = &n
	}
	return out
}

// ---------------------------------------------------------------- saved searches (me)

// ViewsList implements views.list.
func (s *Server) ViewsList(ctx context.Context, req api.ViewsListRequestObject) (api.ViewsListResponseObject, error) {
	actor, _ := auth.FromContext(ctx)
	p, err := scopedProject(ctx, s.Pool, req.P)
	if err != nil {
		return nil, err
	}
	list, err := search.ListViews(ctx, s.Pool, actor.ID, p.ID)
	if err != nil {
		return nil, err
	}
	items := make([]api.SavedView, 0, len(list))
	for _, v := range list {
		items = append(items, apiView(v))
	}
	return api.ViewsList200JSONResponse{Items: items}, nil
}

// ViewsGet implements views.get.
func (s *Server) ViewsGet(ctx context.Context, req api.ViewsGetRequestObject) (api.ViewsGetResponseObject, error) {
	actor, _ := auth.FromContext(ctx)
	p, err := scopedProject(ctx, s.Pool, req.P)
	if err != nil {
		return nil, err
	}
	v, err := search.GetView(ctx, s.Pool, actor.ID, p.ID, req.Name)
	if err != nil {
		return nil, err
	}
	etag := commands.ETag(v.Rev)
	return api.ViewsGet200JSONResponse{Body: apiView(v), Headers: api.ViewsGet200ResponseHeaders{ETag: &etag}}, nil
}

// ViewsSet implements views.set: the query is parsed first, so a saved search always runs.
func (s *Server) ViewsSet(ctx context.Context, req api.ViewsSetRequestObject) (api.ViewsSetResponseObject, error) {
	var ifMatch *int
	if req.Params.IfMatch != nil {
		rev, err := commands.ParseIfMatch(*req.Params.IfMatch)
		if err != nil {
			return nil, err
		}
		ifMatch = &rev
	}
	if _, err := search.Parse(req.Body.Query, searchKinds); err != nil {
		return nil, err
	}
	in := search.ViewInput{Query: req.Body.Query, Description: deref(req.Body.Description)}
	ctx, err := s.withProject(ctx, req.P)
	if err != nil {
		return nil, err
	}
	cmd := command(ctx, "views.set", req.Params.IdempotencyKey, req.Params.DryRun)
	resp, err := s.Pipeline.Run(ctx, cmd, func(ctx context.Context, tx pgx.Tx) (commands.Result, []events.Draft, error) {
		p, err := scopedProject(ctx, tx, req.P)
		if err != nil {
			return commands.Result{}, nil, err
		}
		v, drafts, err := search.SetView(ctx, tx, cmd.Actor.ID, p.ID, req.Name, ifMatch, in)
		if err != nil {
			return commands.Result{}, nil, err
		}
		return commands.Result{Status: http.StatusOK, Body: apiView(v), ETag: commands.ETag(v.Rev)}, drafts, nil
	})
	if err != nil {
		return nil, err
	}
	return commandResponse(resp), nil
}

func (c commandResponse) VisitViewsSetResponse(w http.ResponseWriter) error { return c.write(w) }

func apiView(v search.View) api.SavedView {
	return api.SavedView{
		Id: v.ID, Name: v.Name, Query: v.Query, Description: optional(v.Description), Rev: v.Rev,
		CreatedAt: v.CreatedAt, UpdatedAt: v.UpdatedAt,
	}
}
