package server

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	openapi_types "github.com/oapi-codegen/runtime/types"

	"github.com/usunrise88/cadence/control-plane/internal/agentcreds"
	"github.com/usunrise88/cadence/control-plane/internal/api"
	"github.com/usunrise88/cadence/control-plane/internal/auth"
	"github.com/usunrise88/cadence/control-plane/internal/commands"
	"github.com/usunrise88/cadence/control-plane/internal/defaults"
	"github.com/usunrise88/cadence/control-plane/internal/events"
	"github.com/usunrise88/cadence/control-plane/internal/jobs"
	"github.com/usunrise88/cadence/control-plane/internal/problems"
	"github.com/usunrise88/cadence/control-plane/internal/projects"
	"github.com/usunrise88/cadence/control-plane/internal/projects/bootstrap"
	"github.com/usunrise88/cadence/control-plane/internal/projects/layout"
	"github.com/usunrise88/cadence/control-plane/internal/registry"
	"github.com/usunrise88/cadence/control-plane/internal/repos"
)

// Projects: the wizard and bootstrap, agent profile, notes, template sync, recipe reads and draft branches
// (phase 1 · wave 2), and git over smart HTTP.

func (c commandResponse) VisitProjectsNewResponse(w http.ResponseWriter) error     { return c.write(w) }
func (c commandResponse) VisitProjectsEditResponse(w http.ResponseWriter) error    { return c.write(w) }
func (c commandResponse) VisitProjectsArchiveResponse(w http.ResponseWriter) error { return c.write(w) }
func (c commandResponse) VisitProjectsNoteResponse(w http.ResponseWriter) error    { return c.write(w) }
func (c commandResponse) VisitProjectsSyncResponse(w http.ResponseWriter) error    { return c.write(w) }
func (c commandResponse) VisitAgentProfileEditResponse(w http.ResponseWriter) error {
	return c.write(w)
}
func (c commandResponse) VisitBranchesAcceptResponse(w http.ResponseWriter) error { return c.write(w) }
func (c commandResponse) VisitBranchesRevertResponse(w http.ResponseWriter) error { return c.write(w) }

// repoService returns the project repository service, or an error when the server runs without one.
func (s *Server) repoService() (*bootstrap.Service, error) {
	if s.Projects == nil {
		return nil, errors.New("the project repository service is not configured")
	}
	return s.Projects, nil
}

func dryRun(p *bool) bool { return p != nil && *p }

// ---------------------------------------------------------------- projects.new

// ProjectsNew implements projects.new: the project in state bootstrapping, its agent profile and base-model
// adoption, and the bootstrap job, in one command; 202 {jobId} (a dry run answers the would-be project).
func (s *Server) ProjectsNew(ctx context.Context, req api.ProjectsNewRequestObject) (api.ProjectsNewResponseObject, error) {
	if err := auth.CheckAll(ctx); err != nil {
		return nil, err
	}
	svc, err := s.repoService()
	if err != nil {
		return nil, err
	}
	w := wizardOf(req.Body)
	dry := dryRun(req.Params.DryRun)
	cmd := command(ctx, "projects.new", req.Params.IdempotencyKey, req.Params.DryRun)
	return s.run(ctx, cmd, func(ctx context.Context, tx pgx.Tx) (commands.Result, []events.Draft, error) {
		plan, err := svc.PlanProject(ctx, tx, w)
		if err != nil {
			return commands.Result{}, nil, err
		}
		p, drafts, err := projects.Create(ctx, tx, plan.Project)
		if err != nil {
			return commands.Result{}, nil, err
		}
		if err := svc.Created(ctx, tx, p, plan, cmd.Actor); err != nil {
			return commands.Result{}, nil, err
		}
		if dry {
			return commands.Result{Status: http.StatusOK, Body: p, ETag: commands.ETag(p.Rev)}, drafts, nil
		}
		if s.Jobs == nil {
			return commands.Result{}, nil, errors.New("the job service is not configured")
		}
		job, jd, err := s.Jobs.Enqueue(ctx, tx, jobs.Spec{Kind: bootstrap.Kind, ProjectID: p.ID,
			Args: bootstrap.Args{ProjectID: p.ID, Repository: plan.Repository}})
		if err != nil {
			return commands.Result{}, nil, err
		}
		if p, err = projects.AttachJob(ctx, tx, p.ID, job.ID); err != nil {
			return commands.Result{}, nil, err
		}
		drafts = append(projects.Event(p, "project.created", nil), jd...)
		return commands.Result{Status: http.StatusAccepted, Body: api.JobAccepted{JobId: job.ID}}, drafts, nil
	})
}

func wizardOf(b *api.ProjectNew) bootstrap.Wizard {
	w := bootstrap.Wizard{Name: b.Name, Description: deref(b.Description), Domain: deref(b.Domain), BaseModel: deref(b.BaseModel)}
	if b.Slug != nil {
		w.Slug = *b.Slug
	}
	if b.Locales != nil {
		w.Locales = append(w.Locales, *b.Locales...)
	}
	if a := b.Agent; a != nil {
		if a.Driver != nil {
			w.Driver = string(*a.Driver)
		}
		w.Model = deref(a.Model)
		w.PermissionPreset = deref(a.PermissionPreset)
	}
	w.InstructionsTemplate = deref(b.InstructionsTemplate)
	if r := b.Repository; r != nil {
		if r.Kind != nil {
			w.Repository.Kind = string(*r.Kind)
		}
		w.Repository.URL, w.Repository.Secret = deref(r.Url), deref(r.Secret)
		w.Repository.Owner, w.Repository.Name = deref(r.Owner), deref(r.Name)
		w.Private = r.Private
	}
	if bu := b.Budgets; bu != nil {
		w.GPUHoursPerDay = bu.GpuHoursPerDay
		if bu.AgentTokensPerDay != nil {
			v := int64(*bu.AgentTokensPerDay)
			w.AgentTokensPerDay = &v
		}
	}
	return w
}

// ---------------------------------------------------------------- projects.edit, archive, note, sync

// ProjectsEdit implements projects.edit; changed facts are committed to the repository (project.yaml, AGENTS.md).
func (s *Server) ProjectsEdit(ctx context.Context, req api.ProjectsEditRequestObject) (api.ProjectsEditResponseObject, error) {
	if _, err := scopedProject(ctx, s.Pool, req.P); err != nil {
		return nil, err
	}
	rev, err := commands.ParseIfMatch(req.Params.IfMatch)
	if err != nil {
		return nil, err
	}
	b := req.Body
	in := projects.EditInput{Name: b.Name, Description: b.Description, Domain: b.Domain, BaseModelVersionID: b.BaseModel}
	if b.Locales != nil {
		in.Locales = append([]string{}, *b.Locales...)
	}
	if b.Budgets != nil {
		in.Budgets = &projects.BudgetsEdit{GPUHoursPerDay: b.Budgets.GpuHoursPerDay}
		if b.Budgets.AgentTokensPerDay != nil {
			v := int64(*b.Budgets.AgentTokensPerDay)
			in.Budgets.AgentTokensPerDay = &v
		}
	}
	ctx, err = s.withProject(ctx, req.P)
	if err != nil {
		return nil, err
	}
	cmd := command(ctx, "projects.edit", req.Params.IdempotencyKey, req.Params.DryRun)
	return s.run(ctx, cmd, func(ctx context.Context, tx pgx.Tx) (commands.Result, []events.Draft, error) {
		if in.BaseModelVersionID != nil {
			v, err := registry.GetVersion(ctx, tx, registry.KindBaseModel, *in.BaseModelVersionID)
			if err != nil {
				return commands.Result{}, nil, err
			}
			if v.State != registry.StateFrozen {
				return commands.Result{}, nil, problems.Conflict.New("%s %s is %s; a project's base model is a frozen version", v.Name, v.Version, v.State)
			}
		}
		p, drafts, err := projects.Edit(ctx, tx, req.P, rev, in)
		if err != nil {
			return commands.Result{}, nil, err
		}
		if in.BaseModelVersionID != nil {
			if _, err := registry.AdoptQuietly(ctx, tx, p.ID, []string{*in.BaseModelVersionID}, cmd.Actor); err != nil {
				return commands.Result{}, nil, err
			}
		}
		if in.Facts() && s.Projects != nil {
			more, err := s.Projects.CommitFacts(ctx, tx, p, cmd.Actor, "project: edit "+editSummary(in), cmd.DryRun)
			if err != nil {
				return commands.Result{}, nil, err
			}
			drafts = append(drafts, more...)
		}
		return commands.Result{Status: http.StatusOK, Body: p, ETag: commands.ETag(p.Rev)}, drafts, nil
	})
}

func editSummary(in projects.EditInput) string {
	var parts []string
	for name, set := range map[string]bool{"name": in.Name != nil, "description": in.Description != nil,
		"locales": in.Locales != nil, "domain": in.Domain != nil, "base model": in.BaseModelVersionID != nil,
		"budgets": in.Budgets != nil} {
		if set {
			parts = append(parts, name)
		}
	}
	if len(parts) == 0 {
		return "facts"
	}
	slices.Sort(parts)
	return strings.Join(parts, ", ")
}

// ProjectsArchive implements projects.archive: the project leaves the lists; its working clone and worktrees go,
// its repository stays read-only.
func (s *Server) ProjectsArchive(ctx context.Context, req api.ProjectsArchiveRequestObject) (api.ProjectsArchiveResponseObject, error) {
	if _, err := scopedProject(ctx, s.Pool, req.P); err != nil {
		return nil, err
	}
	rev, err := commands.ParseIfMatch(req.Params.IfMatch)
	if err != nil {
		return nil, err
	}
	ctx, err = s.withProject(ctx, req.P)
	if err != nil {
		return nil, err
	}
	cmd := command(ctx, "projects.archive", req.Params.IdempotencyKey, req.Params.DryRun)
	return s.run(ctx, cmd, func(ctx context.Context, tx pgx.Tx) (commands.Result, []events.Draft, error) {
		p, drafts, err := projects.Archive(ctx, tx, req.P, rev)
		if err != nil {
			return commands.Result{}, nil, err
		}
		if !cmd.DryRun && s.Projects != nil {
			if err := s.Projects.Archived(p); err != nil {
				return commands.Result{}, nil, err
			}
		}
		return commands.Result{Status: http.StatusOK, Body: p, ETag: commands.ETag(p.Rev)}, drafts, nil
	})
}

// ProjectsNote implements projects.note.
func (s *Server) ProjectsNote(ctx context.Context, req api.ProjectsNoteRequestObject) (api.ProjectsNoteResponseObject, error) {
	svc, err := s.repoService()
	if err != nil {
		return nil, err
	}
	if _, err := scopedProject(ctx, s.Pool, req.P); err != nil {
		return nil, err
	}
	rev, err := commands.ParseIfMatch(req.Params.IfMatch)
	if err != nil {
		return nil, err
	}
	ctx, err = s.withProject(ctx, req.P)
	if err != nil {
		return nil, err
	}
	cmd := command(ctx, "projects.note", req.Params.IdempotencyKey, req.Params.DryRun)
	return s.run(ctx, cmd, func(ctx context.Context, tx pgx.Tx) (commands.Result, []events.Draft, error) {
		p, err := projects.Touch(ctx, tx, req.P, rev)
		if err != nil {
			return commands.Result{}, nil, err
		}
		n, drafts, err := svc.AddNote(ctx, tx, p, req.Body.Text, cmd.Actor, cmd.DryRun)
		if err != nil {
			return commands.Result{}, nil, err
		}
		day, err := time.Parse(time.DateOnly, n.Date)
		if err != nil {
			return commands.Result{}, nil, fmt.Errorf("note date: %w", err)
		}
		note := api.ProjectNote{Date: openapi_types.Date{Time: day}, Text: n.Text, Path: layout.NotesMD, Commit: n.Commit}
		drafts = append(projects.Event(p, "project.noted", map[string]any{"note": note}), drafts...)
		return commands.Result{Status: http.StatusOK, Body: note, ETag: commands.ETag(p.Rev)}, drafts, nil
	})
}

// ProjectsSync implements projects.sync.
func (s *Server) ProjectsSync(ctx context.Context, req api.ProjectsSyncRequestObject) (api.ProjectsSyncResponseObject, error) {
	svc, err := s.repoService()
	if err != nil {
		return nil, err
	}
	if _, err := scopedProject(ctx, s.Pool, req.P); err != nil {
		return nil, err
	}
	rev, err := commands.ParseIfMatch(req.Params.IfMatch)
	if err != nil {
		return nil, err
	}
	ctx, err = s.withProject(ctx, req.P)
	if err != nil {
		return nil, err
	}
	cmd := command(ctx, "projects.sync", req.Params.IdempotencyKey, req.Params.DryRun)
	return s.run(ctx, cmd, func(ctx context.Context, tx pgx.Tx) (commands.Result, []events.Draft, error) {
		p, err := projects.Touch(ctx, tx, req.P, rev)
		if err != nil {
			return commands.Result{}, nil, err
		}
		res, err := svc.Sync(ctx, tx, p, cmd.Actor, cmd.DryRun)
		if err != nil {
			return commands.Result{}, nil, err
		}
		body := api.ProjectSync{UpToDate: res.UpToDate, Base: res.Base, Changes: apiChanges(res.Changes),
			Branch: optional(res.Branch), Commit: optional(res.Commit)}
		drafts := projects.Event(p, "project.synced", map[string]any{"sync": body})
		if res.Commit != "" {
			drafts = append(drafts, bootstrap.RecipeEvents(p.ID, res.Branch, res.Commit, res.Changes)...)
		}
		return commands.Result{Status: http.StatusOK, Body: body, ETag: commands.ETag(p.Rev)}, drafts, nil
	})
}

func apiChanges(cs []repos.FileChange) []api.RecipeChange {
	out := make([]api.RecipeChange, 0, len(cs))
	for _, c := range cs {
		out = append(out, api.RecipeChange{Path: c.Path, Status: api.RecipeChangeStatus(c.Status)})
	}
	return out
}

// ---------------------------------------------------------------- agent profile and catalogue

// AgentProfileGet implements agentProfile.get.
func (s *Server) AgentProfileGet(ctx context.Context, req api.AgentProfileGetRequestObject) (api.AgentProfileGetResponseObject, error) {
	svc, err := s.repoService()
	if err != nil {
		return nil, err
	}
	p, err := scopedProject(ctx, s.Pool, req.P)
	if err != nil {
		return nil, err
	}
	a, err := projects.GetAgentProfile(ctx, s.Pool, p.ID)
	if err != nil {
		return nil, err
	}
	files, err := svc.Files(ctx, s.Pool, p, a)
	if err != nil {
		return nil, err
	}
	etag := commands.ETag(a.Rev)
	return api.AgentProfileGet200JSONResponse{Body: apiProfile(a, files), Headers: api.AgentProfileGet200ResponseHeaders{ETag: &etag}}, nil
}

// AgentProfileEdit implements agentProfile.edit.
func (s *Server) AgentProfileEdit(ctx context.Context, req api.AgentProfileEditRequestObject) (api.AgentProfileEditResponseObject, error) {
	svc, err := s.repoService()
	if err != nil {
		return nil, err
	}
	if _, err := scopedProject(ctx, s.Pool, req.P); err != nil {
		return nil, err
	}
	rev, err := commands.ParseIfMatch(req.Params.IfMatch)
	if err != nil {
		return nil, err
	}
	b := req.Body
	var edit projects.ProfileEdit
	str := func(v *string) *string { return v }
	if b.Driver != nil {
		d := string(*b.Driver)
		edit.Driver = &d
	}
	edit.Model = str(b.Model)
	edit.PermissionPreset = str(b.PermissionPreset)
	edit.InstructionsTemplate = str(b.InstructionsTemplate)
	if b.AutoMerge != nil {
		am := string(*b.AutoMerge)
		edit.AutoMerge = &am
	}
	if dp := b.DraftPolicy; dp != nil {
		edit.DraftPolicy = map[string]string{}
		for k, v := range map[string]*api.DraftMode{"mix": dp.Mix, "gate": dp.Gate, "note": dp.Note, "language_pack": dp.LanguagePack} {
			if v != nil {
				edit.DraftPolicy[k] = string(*v)
			}
		}
	}
	ctx, err = s.withProject(ctx, req.P)
	if err != nil {
		return nil, err
	}
	cmd := command(ctx, "agentProfile.edit", req.Params.IdempotencyKey, req.Params.DryRun)
	return s.run(ctx, cmd, func(ctx context.Context, tx pgx.Tx) (commands.Result, []events.Draft, error) {
		p, err := projects.Get(ctx, tx, req.P)
		if err != nil {
			return commands.Result{}, nil, err
		}
		a, files, drafts, err := svc.EditProfile(ctx, tx, p, rev, edit, b.AgentsMd, cmd.Actor, cmd.DryRun)
		if err != nil {
			return commands.Result{}, nil, err
		}
		return commands.Result{Status: http.StatusOK, Body: apiProfile(a, files), ETag: commands.ETag(a.Rev)}, drafts, nil
	})
}

func apiProfile(a projects.AgentProfile, files layout.Files) api.AgentProfile {
	out := api.AgentProfile{
		Driver: api.AgentDriver(a.Driver), Model: a.Model, PermissionPreset: a.PermissionPreset,
		InstructionsTemplate: a.InstructionsTemplate, AutoMerge: api.AutoMerge(a.AutoMerge), Rev: a.Rev,
		UpdatedAt: a.UpdatedAt, Commit: optional(a.Commit), Files: []api.RenderedFile{},
		DraftPolicy: api.DraftPolicy{Mix: api.DraftMode(a.DraftPolicy["mix"]), Gate: api.DraftMode(a.DraftPolicy["gate"]),
			Note: api.DraftMode(a.DraftPolicy["note"]), LanguagePack: api.DraftMode(a.DraftPolicy["language_pack"])},
	}
	for _, path := range bootstrap.ProfileFiles {
		if b, ok := files[path]; ok {
			out.Files = append(out.Files, api.RenderedFile{Path: path, Content: string(b)})
		}
	}
	return out
}

// AgentModelsList implements agentModels.list from defaults.yaml.
func (s *Server) AgentModelsList(ctx context.Context, _ api.AgentModelsListRequestObject) (api.AgentModelsListResponseObject, error) {
	if _, ok := auth.ScopeFromContext(ctx); !ok {
		return nil, problems.Unauthenticated.New("sign in or send a Bearer token")
	}
	w := s.defaultsDoc().Wizard
	entry := func(driver string, p defaults.Param[string], free bool, names map[string]string) api.AgentModels {
		m := api.AgentModels{Driver: api.AgentDriver(driver), Default: p.Value, FreeForm: free, Description: p.Description,
			Source: p.Source, Models: []api.AgentModel{}}
		values := []string{p.Value}
		if p.Range != nil && len(p.Range.Values) > 0 {
			values = p.Range.Values
		}
		for _, v := range values {
			name := names[v]
			if name == "" {
				name = v
			}
			m.Models = append(m.Models, api.AgentModel{Id: v, Name: name})
		}
		return m
	}
	opencode := entry(projects.DriverOpencode, w.OpencodeModel, true, map[string]string{"minimax/MiniMax-M3": "MiniMax M3 (Token Plan)"})
	// Settings → Agents: the admin's default and the models of the configured, verified providers.
	if m, ok, err := agentcreds.DefaultModel(ctx, s.Pool, agentcreds.AgentOpencode); err != nil {
		return nil, err
	} else if ok {
		opencode.Default = m
		opencode.Source = "Settings → Agents (the admin's choice); defaults.yaml wizard.opencode_model otherwise"
	}
	configured, err := agentcreds.Models(ctx, s.Pool, agentcreds.AgentOpencode)
	if err != nil {
		return nil, err
	}
	for _, id := range append(configured, opencode.Default) {
		if !slices.ContainsFunc(opencode.Models, func(m api.AgentModel) bool { return m.Id == id }) {
			opencode.Models = append(opencode.Models, api.AgentModel{Id: id, Name: id})
		}
	}
	return api.AgentModelsList200JSONResponse{Items: []api.AgentModels{
		entry(projects.DriverClaudeCode, w.ClaudeCodeModel, false, map[string]string{
			"sonnet": "Claude Sonnet (latest)", "opus": "Claude Opus (latest)", "haiku": "Claude Haiku (latest)"}),
		opencode,
	}}, nil
}

// ---------------------------------------------------------------- recipes

// projectRepo reads a project the request may reach and checks it has a repository.
func (s *Server) projectRepo(ctx context.Context, slug string) (projects.Project, *bootstrap.Service, error) {
	svc, err := s.repoService()
	if err != nil {
		return projects.Project{}, nil, err
	}
	p, err := scopedProject(ctx, s.Pool, slug)
	if err != nil {
		return projects.Project{}, nil, err
	}
	if p.Repository == nil || !svc.Repos().Exists(p.Slug) {
		return projects.Project{}, nil, problems.NotFound.New("project %q has no repository yet", slug)
	}
	return p, svc, nil
}

func refOf(r *string) string {
	if r == nil || *r == "" {
		return repos.Main
	}
	return *r
}

func notFoundOr(err error) error {
	if errors.Is(err, repos.ErrNotFound) {
		return problems.NotFound.New("%v", err)
	}
	return err
}

// RecipesList implements recipes.list.
func (s *Server) RecipesList(ctx context.Context, req api.RecipesListRequestObject) (api.RecipesListResponseObject, error) {
	p, svc, err := s.projectRepo(ctx, req.P)
	if err != nil {
		return nil, err
	}
	ref := refOf(req.Params.Ref)
	commit, files, err := svc.Repos().ListFiles(ctx, p.Slug, ref, deref(req.Params.Prefix))
	if err != nil {
		return nil, notFoundOr(err)
	}
	out := api.RecipesList200JSONResponse{Ref: ref, Commit: commit, Items: make([]api.RecipeFile, 0, len(files))}
	for _, f := range files {
		out.Items = append(out.Items, api.RecipeFile{Path: f.Path, Bytes: f.Bytes, Blob: f.Blob})
	}
	return out, nil
}

// RecipesGet implements recipes.get.
func (s *Server) RecipesGet(ctx context.Context, req api.RecipesGetRequestObject) (api.RecipesGetResponseObject, error) {
	p, svc, err := s.projectRepo(ctx, req.P)
	if err != nil {
		return nil, err
	}
	ref := refOf(req.Params.Ref)
	b, commit, err := svc.Repos().ReadFile(ctx, p.Slug, ref, req.Path)
	if err != nil {
		return nil, notFoundOr(err)
	}
	hist, err := svc.Repos().History(ctx, p.Slug, commit, req.Path, 50)
	if err != nil {
		return nil, err
	}
	out := api.RecipesGet200JSONResponse{Path: req.Path, Ref: ref, Commit: commit, Bytes: len(b), History: make([]api.RecipeCommit, 0, len(hist))}
	if utf8.Valid(b) {
		out.Encoding, out.Content = api.Utf8, string(b)
	} else {
		out.Encoding, out.Content = api.Base64, base64.StdEncoding.EncodeToString(b)
	}
	for _, h := range hist {
		out.History = append(out.History, api.RecipeCommit{Sha: h.SHA, Message: h.Subject, Author: h.Author, At: h.At})
	}
	return out, nil
}

// ---------------------------------------------------------------- branches

func apiBranch(b repos.Branch) api.Branch {
	out := api.Branch{Name: b.Name, Kind: api.BranchKind(b.Kind()), Head: b.Head, UpdatedAt: b.UpdatedAt, Ahead: b.Ahead,
		Behind: b.Behind, Subject: optional(b.Subject)}
	if id, ok := strings.CutPrefix(b.Name, repos.SessionPrefix); ok {
		out.SessionId = &id
	}
	return out
}

// BranchesList implements branches.list.
func (s *Server) BranchesList(ctx context.Context, req api.BranchesListRequestObject) (api.BranchesListResponseObject, error) {
	p, svc, err := s.projectRepo(ctx, req.P)
	if err != nil {
		return nil, err
	}
	main, err := svc.Repos().Head(ctx, p.Slug)
	if err != nil {
		return nil, err
	}
	list, err := svc.Repos().Branches(ctx, p.Slug, false)
	if err != nil {
		return nil, err
	}
	out := api.BranchesList200JSONResponse{Main: main, Items: make([]api.Branch, 0, len(list))}
	for _, b := range list {
		out.Items = append(out.Items, apiBranch(b))
	}
	return out, nil
}

// BranchesGet implements branches.get.
func (s *Server) BranchesGet(ctx context.Context, req api.BranchesGetRequestObject) (api.BranchesGetResponseObject, error) {
	p, svc, err := s.projectRepo(ctx, req.P)
	if err != nil {
		return nil, err
	}
	d, err := svc.Repos().DiffBranch(ctx, p.Slug, req.Name)
	if err != nil {
		return nil, notFoundOr(err)
	}
	b := apiBranch(d.Branch)
	body := api.BranchDiff{Name: b.Name, Kind: api.BranchDiffKind(b.Kind), SessionId: b.SessionId, Head: b.Head, Subject: b.Subject,
		UpdatedAt: b.UpdatedAt, Ahead: b.Ahead, Behind: b.Behind, Main: d.Main, Base: d.Base, FastForward: d.FastForward,
		Conflicts: d.Conflicts, Patch: d.Patch, Truncated: d.Truncated, Files: make([]api.BranchFileChange, 0, len(d.Files))}
	for _, f := range d.Files {
		body.Files = append(body.Files, api.BranchFileChange{Path: f.Path, Status: api.BranchFileChangeStatus(f.Status),
			Additions: f.Additions, Deletions: f.Deletions, Binary: f.Binary})
	}
	etag := `"` + d.Branch.Head + `"`
	return api.BranchesGet200JSONResponse{Body: body, Headers: api.BranchesGet200ResponseHeaders{ETag: &etag}}, nil
}

// headOf reads an If-Match that names a branch head (the ETag of branches.get).
func headOf(ifMatch string) (string, error) {
	v := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(ifMatch), "W/"))
	v = strings.Trim(v, `"`)
	if len(v) < 7 || strings.Trim(v, "0123456789abcdef") != "" {
		return "", problems.BadRequest.New("If-Match must be the branch head from branches.get (a commit sha), not %q", ifMatch)
	}
	return v, nil
}

// BranchesAccept implements branches.accept.
func (s *Server) BranchesAccept(ctx context.Context, req api.BranchesAcceptRequestObject) (api.BranchesAcceptResponseObject, error) {
	if _, _, err := s.projectRepo(ctx, req.P); err != nil {
		return nil, err
	}
	expect, err := headOf(req.Params.IfMatch)
	if err != nil {
		return nil, err
	}
	if err := bootstrap.CheckDraftBranch(req.Name); err != nil {
		return nil, err
	}
	ctx, err = s.withProject(ctx, req.P)
	if err != nil {
		return nil, err
	}
	cmd := command(ctx, "branches.accept", req.Params.IdempotencyKey, req.Params.DryRun)
	return s.run(ctx, cmd, func(ctx context.Context, tx pgx.Tx) (commands.Result, []events.Draft, error) {
		p, err := projects.Get(ctx, tx, req.P)
		if err != nil {
			return commands.Result{}, nil, err
		}
		if cmd.DryRun {
			d, err := s.Projects.Repos().DiffBranch(ctx, p.Slug, req.Name)
			if err != nil {
				return commands.Result{}, nil, notFoundOr(err)
			}
			if d.Branch.Head != expect {
				return commands.Result{}, nil, problems.PreconditionFailed.New("branch %s is at %s, not %s; re-read it (branches.get)", req.Name, d.Branch.Head, expect)
			}
			if len(d.Conflicts) > 0 {
				return commands.Result{}, nil, problems.MergeConflict.New("merging %s into main conflicts on %s", req.Name, strings.Join(d.Conflicts, ", "))
			}
			changes := make([]repos.FileChange, 0, len(d.Files))
			for _, f := range d.Files {
				changes = append(changes, repos.FileChange{Path: f.Path, Status: f.Status})
			}
			return commands.Result{Status: http.StatusOK, Body: api.BranchMerge{Branch: req.Name, Head: d.Branch.Head, Main: d.Main,
				FastForward: d.FastForward, Changes: apiChanges(changes)}}, nil, nil
		}
		m, drafts, err := s.Projects.Merge(ctx, tx, p, req.Name, expect, cmd.Actor)
		if err != nil {
			return commands.Result{}, nil, err
		}
		body := api.BranchMerge{Branch: m.Branch, Head: m.Head, Main: m.Main, FastForward: m.FastForward, Changes: apiChanges(m.Changes)}
		drafts = append(projects.Event(p, "project.branch_accepted", map[string]any{"merge": body}), drafts...)
		return commands.Result{Status: http.StatusOK, Body: body}, drafts, nil
	})
}

// BranchesRevert implements branches.revert.
func (s *Server) BranchesRevert(ctx context.Context, req api.BranchesRevertRequestObject) (api.BranchesRevertResponseObject, error) {
	if _, _, err := s.projectRepo(ctx, req.P); err != nil {
		return nil, err
	}
	expect, err := headOf(req.Params.IfMatch)
	if err != nil {
		return nil, err
	}
	if err := bootstrap.CheckDraftBranch(req.Name); err != nil {
		return nil, err
	}
	ctx, err = s.withProject(ctx, req.P)
	if err != nil {
		return nil, err
	}
	cmd := command(ctx, "branches.revert", req.Params.IdempotencyKey, req.Params.DryRun)
	return s.run(ctx, cmd, func(ctx context.Context, tx pgx.Tx) (commands.Result, []events.Draft, error) {
		p, err := projects.Get(ctx, tx, req.P)
		if err != nil {
			return commands.Result{}, nil, err
		}
		var b repos.Branch
		if cmd.DryRun {
			if b, err = s.Projects.Repos().Branch(ctx, p.Slug, req.Name); err != nil {
				return commands.Result{}, nil, notFoundOr(err)
			}
			if b.Head != expect {
				return commands.Result{}, nil, problems.PreconditionFailed.New("branch %s is at %s, not %s; re-read it (branches.get)", req.Name, b.Head, expect)
			}
		} else if b, err = s.Projects.Discard(ctx, p, req.Name, expect); err != nil {
			return commands.Result{}, nil, err
		}
		body := apiBranch(b)
		return commands.Result{Status: http.StatusOK, Body: body},
			projects.Event(p, "project.branch_reverted", map[string]any{"branch": body}), nil
	})
}

// ---------------------------------------------------------------- git over smart HTTP

// gitHandler serves the project repositories at /git/<slug>.git to API keys and agent session tokens.
func (s *Server) gitHandler() http.Handler {
	return s.Projects.Repos().HTTPHandler(s.gitAuthorize, func(ctx context.Context, slug string, a repos.Access, moved map[string][2]string) {
		actor, _ := a.Principal.(auth.Actor)
		if err := s.Projects.Pushed(ctx, slug, actor, moved); err != nil {
			s.Log.ErrorContext(ctx, "record a push", "project", slug, "err", err)
		}
	})
}

// gitAuthorize resolves the token of a git request: an API key or an agent session token whose scope reaches the
// project (the fixed actor of tests and development reaches everything). Agent tokens push only their session
// branch; read-only presets and archived projects push nothing.
func (s *Server) gitAuthorize(ctx context.Context, slug, token string, write bool) (repos.Access, error) {
	var pr auth.Principal
	switch {
	case s.Actor.ID != "":
		pr = auth.Principal{Actor: s.Actor, Scope: auth.FullScope()}
	case token == "":
		return repos.Access{}, repos.ErrUnauthorized
	default:
		var err error
		pr, _, err = s.Credentials.Resolve(ctx, token, auth.ViaBearer)
		if errors.Is(err, auth.ErrUnknownToken) {
			return repos.Access{}, repos.ErrUnauthorized
		}
		if err != nil {
			return repos.Access{}, err
		}
	}
	p, err := projects.Get(ctx, s.Pool, slug)
	var pe *problems.Error
	if errors.As(err, &pe) && pe.Type == problems.NotFound {
		return repos.Access{}, repos.ErrNotFound
	}
	if err != nil {
		return repos.Access{}, err
	}
	if !pr.Scope.AllowsProject(p.ID) {
		return repos.Access{}, repos.ErrForbidden
	}
	if p.Repository == nil {
		return repos.Access{}, repos.ErrNotFound
	}
	a := repos.Access{User: pr.Actor.ID, Write: true, Principal: pr.Actor, ReadOnly: p.Archived()}
	if pr.Actor.Kind == auth.KindAgent {
		a.PushRefs = "refs/heads/" + repos.SessionBranch(pr.Actor.SessionID)
		a.Write = pr.Actor.SessionID != "" && pr.Scope.Preset != "read-only"
	}
	_ = write
	return a, nil
}

// apiProject renders a project as the contract's Project (its JSON form is the same).
func apiProject(p projects.Project) api.Project {
	var out api.Project
	b, err := json.Marshal(p)
	if err == nil {
		err = json.Unmarshal(b, &out)
	}
	if err != nil {
		panic("projects.Project no longer matches api.Project: " + err.Error())
	}
	return out
}
