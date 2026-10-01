package bootstrap

import (
	"context"
	"errors"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"github.com/usunrise88/cadence/control-plane/internal/auth"
	"github.com/usunrise88/cadence/control-plane/internal/events"
	"github.com/usunrise88/cadence/control-plane/internal/problems"
	"github.com/usunrise88/cadence/control-plane/internal/projects"
	"github.com/usunrise88/cadence/control-plane/internal/projects/layout"
	"github.com/usunrise88/cadence/control-plane/internal/repos"
)

// FileWrite is one recipe file written from the Recipe document (recipes.new, recipes.edit; phase 2 · stream U).
type FileWrite struct {
	Path    string
	Content []byte
	// Expect is the commit that last changed the file (If-Match); empty creates a file that must not exist yet.
	Expect  string
	Message string
}

// WriteFile commits one UTF-8 text file to main as actor. An edit must name the commit that last changed the file
// (a file changed since answers 412); a create refuses a path that exists. Files rendered from the agent profile
// change only through agentProfile.edit. A dry run checks everything and commits nothing (the commit is empty).
func (s *Service) WriteFile(ctx context.Context, tx pgx.Tx, p projects.Project, w FileWrite, actor auth.Actor, dryRun bool) (repos.Commit, []events.Draft, error) {
	if err := writable(p); err != nil {
		return repos.Commit{}, nil, err
	}
	if slices.Contains(ProfileFiles, w.Path) {
		return repos.Commit{}, nil, problems.Conflict.New("%s is rendered from the agent profile; change it with agentProfile.edit", w.Path)
	}
	if !utf8.Valid(w.Content) {
		return repos.Commit{}, nil, problems.Validation([]problems.FieldError{{Path: "content", Message: "the content must be UTF-8 text"}})
	}
	_, _, err := s.o.Repos.ReadFile(ctx, p.Slug, repos.Main, w.Path)
	exists := err == nil
	if err != nil && !errors.Is(err, repos.ErrNotFound) {
		return repos.Commit{}, nil, repoProblem(err)
	}
	switch {
	case w.Expect == "" && exists:
		return repos.Commit{}, nil, problems.Conflict.New("%s already exists; change it with recipes.edit", w.Path)
	case w.Expect != "" && !exists:
		return repos.Commit{}, nil, problems.NotFound.New("%s is not on main; create it with recipes.new", w.Path)
	case w.Expect != "":
		hist, err := s.o.Repos.History(ctx, p.Slug, repos.Main, w.Path, 1)
		if err != nil {
			return repos.Commit{}, nil, repoProblem(err)
		}
		if len(hist) == 0 || !strings.HasPrefix(hist[0].SHA, w.Expect) {
			last := ""
			if len(hist) > 0 {
				last = hist[0].SHA
			}
			return repos.Commit{}, nil, problems.PreconditionFailed.New("%s was last changed in %.12s, not %s; re-read it (recipes.get)", w.Path, last, w.Expect)
		}
	}
	if dryRun {
		return repos.Commit{}, nil, nil
	}
	msg := w.Message
	if msg == "" {
		verb := "edit"
		if w.Expect == "" {
			verb = "add"
		}
		msg = verb + " " + w.Path
	}
	return s.commitMain(ctx, tx, p, actor, msg, layout.Files{w.Path: w.Content})
}
