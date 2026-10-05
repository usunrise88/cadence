package server

import (
	"context"
	"errors"
	"net/http"

	"github.com/jackc/pgx/v5"

	"github.com/usunrise88/cadence/control-plane/internal/api"
	"github.com/usunrise88/cadence/control-plane/internal/auth"
	"github.com/usunrise88/cadence/control-plane/internal/bundles"
	"github.com/usunrise88/cadence/control-plane/internal/commands"
	"github.com/usunrise88/cadence/control-plane/internal/events"
	"github.com/usunrise88/cadence/control-plane/internal/exports"
	"github.com/usunrise88/cadence/control-plane/internal/problems"
	"github.com/usunrise88/cadence/control-plane/internal/projects"
)

// Project bundles (phase 4 tail): projects.export writes a project bundle to a mount (a control-plane job, an export
// row of format cadence-project-bundle); bundles.adopt imports one into an existing project and projects.new with
// bundle makes a project from one (handlers_projects.go). Both imports register registry versions, so they are an
// approval for everyone (preset rule bundle-import, which matches from=bundle).

func (c commandResponse) VisitProjectsExportResponse(w http.ResponseWriter) error { return c.write(w) }
func (c commandResponse) VisitBundlesAdoptResponse(w http.ResponseWriter) error   { return c.write(w) }

func (s *Server) newBundles() *bundles.Service {
	b := &bundles.Service{Pool: s.Pool, CAS: s.CAS, Jobs: s.Jobs, Defaults: s.defaultsDoc, Log: s.Log}
	if s.Secrets != nil {
		b.Secrets = s.Secrets
	}
	if s.Projects != nil {
		b.Facts = s.Projects
	}
	return b
}

func (s *Server) projectWriter() *exports.ProjectWriter {
	w := &exports.ProjectWriter{Pool: s.Pool, CAS: s.CAS, Defaults: s.defaultsDoc}
	if s.Projects != nil {
		w.Repos = s.Projects.Repos()
	}
	return w
}

func apiProjectExportPlan(pl exports.ProjectPlan) api.ProjectExportPlan {
	out := api.ProjectExportPlan{ProjectId: pl.Project.ID, Project: pl.Project.Slug, Ref: pl.Ref, Commit: pl.Commit, Target: pl.Target,
		Blobs: pl.Blobs, Bytes: pl.Bytes, Sources: pl.Sources, Versions: make([]api.BundleVersion, 0, len(pl.Versions)),
		Aliases: make([]api.BundleAlias, 0, len(pl.Aliases))}
	for _, v := range pl.Versions {
		bv := api.BundleVersion{Id: v.Version.ID, Kind: api.RegistryKind(v.Version.Kind), Collection: v.Version.Name,
			Version: v.Version.Version, Adopted: v.Adopted, Blobs: v.Blobs, Bytes: v.Bytes, Path: optional(v.Path)}
		if v.Published {
			bv.Published = &v.Published
		}
		out.Versions = append(out.Versions, bv)
	}
	for _, a := range pl.Aliases {
		out.Aliases = append(out.Aliases, api.BundleAlias{Name: a.Name, VersionId: a.VersionID})
	}
	return out
}

// ProjectsExport implements projects.export: the plan (a dry run answers it), then the export row and its job.
func (s *Server) ProjectsExport(ctx context.Context, req api.ProjectsExportRequestObject) (api.ProjectsExportResponseObject, error) {
	rev, err := commands.ParseIfMatch(req.Params.IfMatch)
	if err != nil {
		return nil, err
	}
	p, err := scopedProject(ctx, s.Pool, req.P)
	if err != nil {
		return nil, err
	}
	if s.Projects == nil {
		return nil, errors.New("the project repository service is not configured")
	}
	in := exports.ProjectRequest{Ref: deref(req.Body.Ref), Target: deref(req.Body.Target)}
	ctx = commands.WithProject(ctx, p.ID)
	cmd := command(ctx, exports.ProjectOperation, req.Params.IdempotencyKey, req.Params.DryRun)
	return s.run(ctx, cmd, func(ctx context.Context, tx pgx.Tx) (commands.Result, []events.Draft, error) {
		cur, err := projects.Get(ctx, tx, req.P)
		if err != nil {
			return commands.Result{}, nil, err
		}
		if err := commands.CheckRev(projects.Kind, rev, cur.Rev); err != nil {
			return commands.Result{}, nil, err
		}
		plan, err := exports.PlanProject(ctx, tx, s.CAS, s.Projects.Repos(), s.defaultsDoc(), cur, in)
		if err != nil {
			return commands.Result{}, nil, err
		}
		if cmd.DryRun {
			return commands.Result{Status: http.StatusOK, Body: apiProjectExportPlan(plan)}, nil, nil
		}
		x, drafts, err := exports.StartProject(ctx, tx, s.Jobs, plan, cmd.Actor)
		if err != nil {
			return commands.Result{}, nil, err
		}
		return commands.Result{Status: http.StatusCreated, Body: apiExport(x), ETag: commands.ETag(x.Rev)}, drafts, nil
	})
}

func apiBundlePlan(pl bundles.Plan) api.BundlePlan {
	d := pl.Bundle.Doc
	out := api.BundlePlan{Bundle: pl.Bundle.URI, Commit: d.Commit, Register: pl.Register, Reuse: pl.Reuse, Blobs: pl.Blobs,
		Bytes: pl.Bytes, Sources: pl.Sources, Versions: make([]api.BundleImportVersion, 0, len(pl.Versions)),
		Aliases: make([]api.BundleAlias, 0, len(pl.Aliases))}
	if !d.CreatedAt.IsZero() {
		t := d.CreatedAt
		out.CreatedAt = &t
	}
	out.Project.Name, out.Project.Slug, out.Project.Description = d.Project.Name, d.Project.Slug, optional(d.Project.Description)
	out.Project.Locales, out.Project.Domain = d.Project.Locales, d.Project.Domain
	if out.Project.Locales == nil {
		out.Project.Locales = []string{}
	}
	for _, v := range pl.Versions {
		r := v.Entry.Record
		iv := api.BundleImportVersion{Id: r.VersionID, Kind: api.RegistryKind(r.Kind), Collection: r.Collection, Version: r.Version,
			Adopted: v.Entry.Adopted, Action: api.BundleImportVersionAction(v.Action)}
		if v.Local != nil {
			iv.LocalId = &v.Local.ID
		}
		if v.Blobs > 0 {
			n, b := v.Blobs, v.Bytes
			iv.Blobs, iv.Bytes = &n, &b
		}
		if r.VersionID == d.Project.BaseModel {
			bm := r.Collection + "@" + r.Version
			out.Project.BaseModel = &bm
		}
		out.Versions = append(out.Versions, iv)
	}
	for _, a := range pl.Aliases {
		action := api.BundleAliasAction(a.Action)
		out.Aliases = append(out.Aliases, api.BundleAlias{Name: a.Name, VersionId: a.VersionID, LocalVersionId: optional(a.LocalVersionID),
			Action: &action})
	}
	return out
}

// BundlesAdopt implements bundles.adopt: the plan (a dry run answers it without asking), then — the bundle-import
// approval granted — the import job.
func (s *Server) BundlesAdopt(ctx context.Context, req api.BundlesAdoptRequestObject) (api.BundlesAdoptResponseObject, error) {
	rev, err := commands.ParseIfMatch(req.Params.IfMatch)
	if err != nil {
		return nil, err
	}
	p, err := scopedProject(ctx, s.Pool, req.P)
	if err != nil {
		return nil, err
	}
	if err := auth.CheckRegistryRead(ctx); err != nil {
		return nil, err
	}
	aliases := req.Body.Aliases == nil || *req.Body.Aliases
	svc := s.bundles
	// Checked before the policy asks anyone: the admin never approves a bundle that cannot be read.
	b, err := svc.Open(ctx, s.Pool, req.Body.Bundle)
	if err != nil {
		return nil, err
	}
	if _, err := svc.PlanImport(ctx, s.Pool, b, p.ID, aliases); err != nil {
		return nil, err
	}
	// A registry change for every project: the approval is the admin's (registry scope, no project).
	cmd := command(ctx, bundles.Operation, req.Params.IdempotencyKey, req.Params.DryRun)
	if cmd.PathParams == nil {
		cmd.PathParams = map[string]string{}
	}
	cmd.PathParams[bundles.GateParam] = bundles.GateValue
	approval := commands.ReplayedApproval(ctx)
	return s.run(ctx, cmd, func(ctx context.Context, tx pgx.Tx) (commands.Result, []events.Draft, error) {
		cur, err := projects.Get(ctx, tx, req.P)
		if err != nil {
			return commands.Result{}, nil, err
		}
		if err := commands.CheckRev(projects.Kind, rev, cur.Rev); err != nil {
			return commands.Result{}, nil, err
		}
		if cur.Archived() {
			return commands.Result{}, nil, problems.Conflict.New("project %q is archived; nothing can be adopted into it", cur.Slug)
		}
		plan, err := svc.PlanImport(ctx, tx, b, cur.ID, aliases)
		if err != nil {
			return commands.Result{}, nil, err
		}
		if cmd.DryRun {
			return commands.Result{Status: http.StatusOK, Body: apiBundlePlan(plan)}, nil, nil
		}
		job, drafts, err := svc.EnqueueAdopt(ctx, tx, cur.ID, b.URI, aliases, approval)
		if err != nil {
			return commands.Result{}, nil, err
		}
		return commands.Result{Status: http.StatusCreated, Body: api.BundleImport{JobId: job.ID, Plan: apiBundlePlan(plan)}}, drafts, nil
	})
}
