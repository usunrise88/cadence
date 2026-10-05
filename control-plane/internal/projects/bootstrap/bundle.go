package bootstrap

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/jackc/pgx/v5"

	"github.com/usunrise88/cadence/control-plane/internal/auth"
	"github.com/usunrise88/cadence/control-plane/internal/bundles"
	"github.com/usunrise88/cadence/control-plane/internal/events"
	"github.com/usunrise88/cadence/control-plane/internal/jobs"
	"github.com/usunrise88/cadence/control-plane/internal/problems"
	"github.com/usunrise88/cadence/control-plane/internal/projects"
	"github.com/usunrise88/cadence/control-plane/internal/projects/layout"
	"github.com/usunrise88/cadence/control-plane/internal/registry"
	"github.com/usunrise88/cadence/control-plane/internal/storage"
)

// A project made from a project bundle (projects.new with bundle; phase 4 tail): the wizard's facts default to the
// bundle's, the internal repository is the bundle's history, and the bootstrap job imports the bundle's registry
// versions (internal/bundles) instead of rendering the templates, then commits the files this instance renders for
// the project's facts and agent profile.

// SetBundles lets projects.new make projects from bundles.
func (s *Service) SetBundles(b *bundles.Service) { s.bundles = b }

// bundleFiles are the files the bootstrap of a bundle's project re-renders for this instance: the facts and the
// agent configuration (the permission files follow this instance's preset, never the bundle's).
var bundleFiles = []string{layout.ProjectYAML, layout.AgentsMD, layout.ClaudeMD, layout.DataLock, layout.ClaudeSettings, layout.OpencodeJSON}

// planBundle reads the bundle a wizard names and fills the facts the wizard left empty from it: description,
// locales, domain and the base model (when this instance holds the bundle's). The repository is internal.
func (s *Service) planBundle(ctx context.Context, q storage.Querier, w *Wizard) error {
	if s.bundles == nil {
		return problems.NotImplemented.New("this control plane cannot read project bundles")
	}
	b, err := s.bundles.Open(ctx, q, w.Bundle)
	if err != nil {
		return err
	}
	if _, err := s.bundles.PlanImport(ctx, q, b, "", true); err != nil {
		return err
	}
	w.Bundle = b.URI
	d := b.Doc.Project
	if w.Description == "" {
		w.Description = d.Description
	}
	if len(w.Locales) == 0 {
		w.Locales = append([]string{}, d.Locales...)
	}
	if w.Domain == "" {
		w.Domain = d.Domain
	}
	if w.BaseModel == "" && d.BaseModel != "" {
		for _, e := range b.Doc.Versions {
			if e.Record.VersionID != d.BaseModel {
				continue
			}
			if v, ok, err := bundles.Local(ctx, q, e.Record); err != nil {
				return err
			} else if ok && v.Kind == registry.KindBaseModel {
				w.BaseModel = v.ID
			}
		}
	}
	if w.Repository.Kind != "" && w.Repository.Kind != projects.RepoInternal {
		return problems.Validation([]problems.FieldError{{Path: "/repository/kind",
			Message: "a project made from a bundle has an internal repository (link a remote later)"}})
	}
	w.Repository.Kind = projects.RepoInternal
	return nil
}

// bootstrapBundle is the bootstrap of a project made from a bundle.
func (s *Service) bootstrapBundle(ctx context.Context, run *jobs.Run, args Args, p projects.Project, profile projects.AgentProfile,
	actor auth.Actor) (Result, error) {
	if s.bundles == nil {
		return Result{}, fmt.Errorf("project %s names bundle %s and this control plane cannot read bundles", p.Slug, args.Bundle)
	}
	b, err := s.bundles.Open(ctx, s.o.Pool, args.Bundle)
	if err != nil {
		return Result{}, err
	}
	if err := run.Progress(ctx, 0.05, "creating the repository from the bundle"); err != nil {
		return Result{}, err
	}
	if !s.o.Repos.Exists(p.Slug) {
		tmp, err := os.MkdirTemp("", "cadence-bundle-*")
		if err != nil {
			return Result{}, err
		}
		defer func() { _ = os.RemoveAll(tmp) }()
		file := filepath.Join(tmp, "repository.bundle")
		if err := b.SaveRepository(ctx, file); err != nil {
			return Result{}, err
		}
		if err := s.o.Repos.CreateFromBundle(ctx, p.Slug, file); err != nil {
			return Result{}, problems.BundleInvalid.New("%s: the repository bundle: %v", b.URI, err)
		}
	}
	imported, err := s.bundles.Import(ctx, b, p, actor, bundles.Options{Aliases: true, BaseModel: true}, func(f float64, msg string) {
		_ = run.Progress(ctx, 0.1+0.75*f, msg)
	})
	if err != nil {
		return Result{}, err
	}
	if err := run.Progress(ctx, 0.9, "committing the project's files for this instance"); err != nil {
		return Result{}, err
	}
	res := Result{Repository: projects.RepoInternal, Bundle: &imported}
	err = pgx.BeginFunc(ctx, s.o.Pool, func(tx pgx.Tx) error {
		cur, err := projects.GetByID(ctx, tx, p.ID)
		if err != nil {
			return err
		}
		_, templateIDs, err := s.facts(ctx, tx, cur, profile)
		if err != nil {
			return err
		}
		if _, err := registry.AdoptQuietly(ctx, tx, cur.ID, templateIDs, actor); err != nil {
			return err
		}
		facts, _, err := s.facts(ctx, tx, cur, profile) // data.lock with the templates adopted
		if err != nil {
			return err
		}
		files, err := s.render.Render(facts)
		if err != nil {
			return err
		}
		picked := files.Pick(bundleFiles...)
		res.Files = picked.Paths()
		c, drafts, err := s.commitMain(ctx, tx, cur, actor, "import bundle "+shortSHA(b.Doc.Commit)+" ("+b.URI+")", picked)
		if err != nil {
			return err
		}
		res.Commit = c.SHA
		if err := projects.SetProfileCommit(ctx, tx, cur.ID, c.SHA); err != nil {
			return err
		}
		if actor.Kind == auth.KindUser {
			if res.Workspaces, err = projects.CreateDefaultWorkspaces(ctx, tx, actor.ID, cur.ID); err != nil {
				return err
			}
		}
		_, done, err := projects.Bootstrapped(ctx, tx, cur.ID)
		if err != nil {
			return err
		}
		return events.Append(ctx, tx, actor, nil, append(drafts, done...))
	})
	if err != nil {
		return Result{}, err
	}
	if res.Workspaces == nil {
		res.Workspaces = []string{}
	}
	return res, nil
}

func shortSHA(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}
