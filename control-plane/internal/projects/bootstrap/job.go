package bootstrap

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/usunrise88/cadence/control-plane/internal/auth"
	"github.com/usunrise88/cadence/control-plane/internal/bundles"
	"github.com/usunrise88/cadence/control-plane/internal/events"
	"github.com/usunrise88/cadence/control-plane/internal/jobs"
	"github.com/usunrise88/cadence/control-plane/internal/projects"
	"github.com/usunrise88/cadence/control-plane/internal/projects/layout"
	"github.com/usunrise88/cadence/control-plane/internal/registry"
	"github.com/usunrise88/cadence/control-plane/internal/repos"
)

// Kind is the job kind that bootstraps a project repository.
const Kind = "projects.bootstrap"

// RepoArgs is the repository the wizard chose.
type RepoArgs struct {
	Kind    string `json:"kind"`
	URL     string `json:"url,omitempty"`
	Secret  string `json:"secret,omitempty"`
	Owner   string `json:"owner,omitempty"`
	Name    string `json:"name,omitempty"`
	Private bool   `json:"private,omitempty"`
}

// Args are the bootstrap job's arguments.
type Args struct {
	ProjectID  string   `json:"projectId"`
	Repository RepoArgs `json:"repository"`
	// Bundle makes the project from a project bundle (projects.new with bundle): the repository from its git bundle,
	// the registry versions imported and adopted (bundle.go).
	Bundle string `json:"bundle,omitempty"`
}

// Result is what a finished bootstrap job reports.
type Result struct {
	Commit     string   `json:"commit"`
	Files      []string `json:"files"`
	Repository string   `json:"repository"`
	Workspaces []string `json:"workspaces"`
	// Bundle is what the import of a project bundle did (projects.new with bundle).
	Bundle *bundles.Result `json:"bundle,omitempty"`
}

// Register adds the bootstrap job kind and the daily merged-branch retention chore; call before jobs.Start.
func (s *Service) Register(js *jobs.Service) {
	js.Register(Kind, s.handle, jobs.KindOptions{Timeout: 10 * time.Minute})
	js.AddPeriodic("repos.prune", 24*time.Hour, func(ctx context.Context) error {
		_, err := s.PruneBranches(ctx)
		return err
	})
}

func (s *Service) handle(ctx context.Context, run *jobs.Run) (any, error) {
	var args Args
	if err := json.Unmarshal(run.Args, &args); err != nil {
		return nil, fmt.Errorf("bootstrap arguments: %w", err)
	}
	actor := run.Job.Actor
	res, err := s.bootstrap(ctx, run, args, actor)
	if err != nil {
		s.o.Log.WarnContext(ctx, "project bootstrap failed", "projectId", args.ProjectID, "err", err)
		fctx := context.WithoutCancel(ctx)
		if ferr := pgx.BeginFunc(fctx, s.o.Pool, func(tx pgx.Tx) error {
			_, drafts, merr := projects.BootstrapFailed(fctx, tx, args.ProjectID, err.Error())
			if merr != nil {
				return merr
			}
			return events.Append(fctx, tx, actor, nil, drafts)
		}); ferr != nil {
			return nil, errors.Join(err, ferr)
		}
		return nil, err
	}
	return res, nil
}

func (s *Service) bootstrap(ctx context.Context, run *jobs.Run, args Args, actor auth.Actor) (Result, error) {
	p, err := projects.GetByID(ctx, s.o.Pool, args.ProjectID)
	if err != nil {
		return Result{}, err
	}
	profile, err := projects.GetAgentProfile(ctx, s.o.Pool, p.ID)
	if err != nil {
		return Result{}, err
	}
	if args.Bundle != "" {
		return s.bootstrapBundle(ctx, run, args, p, profile, actor)
	}
	if err := run.Progress(ctx, 0.1, "creating the repository"); err != nil {
		return Result{}, err
	}
	if err := s.createRepository(ctx, &p, args.Repository); err != nil {
		return Result{}, err
	}

	if err := run.Progress(ctx, 0.4, "rendering the templates"); err != nil {
		return Result{}, err
	}
	facts, templateIDs, err := s.facts(ctx, s.o.Pool, p, profile)
	if err != nil {
		return Result{}, err
	}
	files, err := s.render.Bootstrap(facts)
	if err != nil {
		return Result{}, err
	}
	if _, _, err := s.o.Repos.ReadFile(ctx, p.Slug, repos.Main, layout.NotesMD); err == nil {
		delete(files, layout.NotesMD) // a linked repository keeps the notes it has
	}

	if err := run.Progress(ctx, 0.6, "committing bootstrap"); err != nil {
		return Result{}, err
	}
	res := Result{Files: files.Paths(), Repository: p.Repository.Kind}
	err = pgx.BeginFunc(ctx, s.o.Pool, func(tx pgx.Tx) error {
		c, drafts, err := s.commitMain(ctx, tx, p, actor, "bootstrap", files, layout.SkillsDir)
		if err != nil {
			return err
		}
		res.Commit = c.SHA
		if err := projects.SetProfileCommit(ctx, tx, p.ID, c.SHA); err != nil {
			return err
		}
		if _, err := registry.AdoptQuietly(ctx, tx, p.ID, templateIDs, actor); err != nil {
			return err
		}
		if actor.Kind == auth.KindUser {
			if res.Workspaces, err = projects.CreateDefaultWorkspaces(ctx, tx, actor.ID, p.ID); err != nil {
				return err
			}
		}
		_, done, err := projects.Bootstrapped(ctx, tx, p.ID)
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

// createRepository creates the internal repository, links a url, or creates a GitHub repository and links it. A
// repository that exists already (a job retried after the step) is kept.
func (s *Service) createRepository(ctx context.Context, p *projects.Project, r RepoArgs) error {
	if s.o.Repos.Exists(p.Slug) {
		return nil
	}
	switch r.Kind {
	case projects.RepoInternal, "":
		return s.o.Repos.Create(ctx, p.Slug)
	case projects.RepoURL:
		remote := repos.Remote{URL: r.URL}
		if r.Secret != "" {
			tok, err := s.token(ctx, r.Secret)
			if err != nil {
				return fmt.Errorf("read the token for %s: %w", r.URL, err)
			}
			remote.Token = tok
		}
		return s.o.Repos.Link(ctx, p.Slug, remote)
	case projects.RepoGitHub:
		tok, err := s.token(ctx, r.Secret)
		if err != nil {
			return fmt.Errorf("read the GitHub token %q: %w", r.Secret, err)
		}
		name := r.Name
		if name == "" {
			name = p.Slug
		}
		created, err := s.o.GitHub.CreateRepo(ctx, tok, r.Owner, name, p.Name+" — a Cadence project", r.Private)
		if err != nil {
			return err
		}
		if err := pgx.BeginFunc(ctx, s.o.Pool, func(tx pgx.Tx) error {
			return projects.SetRemote(ctx, tx, p.ID, created.CloneURL)
		}); err != nil {
			return err
		}
		p.Repository.Remote = created.CloneURL
		return s.o.Repos.Link(ctx, p.Slug, repos.Remote{URL: created.CloneURL, Token: tok})
	}
	return fmt.Errorf("unknown repository kind %q", r.Kind)
}
