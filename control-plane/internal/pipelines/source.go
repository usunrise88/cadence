package pipelines

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strings"

	"github.com/usunrise88/cadence/control-plane/internal/problems"
	"github.com/usunrise88/cadence/control-plane/internal/projects"
	"github.com/usunrise88/cadence/control-plane/internal/repos"
)

// Sources of a pipeline.
const (
	SourceRepository = "repository" // pipelines/<name>.yaml in the project repository
	SourceTemplate   = "template"   // a bundled template (control-plane/templates/pipelines) the repository has no file for
	SourceInline     = "inline"     // handed to Start as a parsed Pipeline by a facade
)

// DefaultRef is the ref pipelines are read at when none is given.
const DefaultRef = repos.Main

// AnyVersion is the If-Match of pipelines.run that accepts whatever version is at the ref.
const AnyVersion = "*"

// Repo reads project repositories; *repos.Store implements it.
type Repo interface {
	Exists(slug string) bool
	ListFiles(ctx context.Context, slug, ref, prefix string) (string, []repos.File, error)
	ReadFile(ctx context.Context, slug, ref, path string) ([]byte, string, error)
	History(ctx context.Context, slug, ref, path string, limit int) ([]repos.LogEntry, error)
}

// Source is a pipeline as read: from the project repository at a commit, or a bundled template.
type Source struct {
	Pipeline Pipeline
	Kind     string // SourceRepository | SourceTemplate | SourceInline
	Path     string
	Ref      string
	Commit   string // the commit Ref resolved to (repository only)
	Version  string // the commit that last changed the file; template-<sha256 prefix> for a template
	Err      error  // the file does not parse (List only; Load returns the error)
}

func templateVersion(b []byte) string {
	sum := sha256.Sum256(b)
	return "template-" + hex.EncodeToString(sum[:])[:12]
}

func (e *Engine) hasRepo(p projects.Project) bool {
	return e.o.Repos != nil && p.Repository != nil && e.o.Repos.Exists(p.Slug)
}

func repoError(err error) error {
	if errors.Is(err, repos.ErrNotFound) {
		return problems.NotFound.New("%v", err)
	}
	return err
}

// Load reads the pipeline named name for project p at ref: the repository's pipelines/<name>.yaml, else the
// bundled template of that name. A file that does not parse fails with pipeline-invalid.
func (e *Engine) Load(ctx context.Context, p projects.Project, name, ref string) (Source, error) {
	if !ValidName(name) {
		return Source{}, problems.BadRequest.New("%q is not a pipeline name", name)
	}
	if ref == "" {
		ref = DefaultRef
	}
	file := Path(name)
	if e.hasRepo(p) {
		b, commit, err := e.o.Repos.ReadFile(ctx, p.Slug, ref, file)
		switch {
		case err == nil:
			src := Source{Kind: SourceRepository, Path: file, Ref: ref, Commit: commit}
			hist, err := e.o.Repos.History(ctx, p.Slug, commit, file, 1)
			if err != nil {
				return Source{}, fmt.Errorf("history of %s: %w", file, err)
			}
			src.Version = commit
			if len(hist) > 0 {
				src.Version = hist[0].SHA
			}
			if src.Pipeline, err = Parse(b, name); err != nil {
				return Source{}, err
			}
			return src, nil
		case !errors.Is(err, repos.ErrNotFound):
			return Source{}, err
		case commit == "" && ref != DefaultRef:
			return Source{}, problems.NotFound.New("ref %q names nothing in the repository of %s", ref, p.Slug)
		}
	}
	b, err := fs.ReadFile(e.o.Templates, file)
	if err != nil {
		return Source{}, problems.NotFound.New("no pipeline %q: neither %s in the repository of %s nor a bundled template", name, file, p.Slug)
	}
	pl, err := Parse(b, name)
	if err != nil {
		return Source{}, err
	}
	return Source{Pipeline: pl, Kind: SourceTemplate, Path: file, Ref: ref, Version: templateVersion(b)}, nil
}

// List reads every pipeline of project p at ref: pipelines/*.yaml of the repository plus the bundled templates
// the repository has no file for, by name. It returns the commit ref resolved to ("" without a repository).
func (e *Engine) List(ctx context.Context, p projects.Project, ref string) (string, []Source, error) {
	if ref == "" {
		ref = DefaultRef
	}
	var (
		commit string
		out    []Source
		seen   = map[string]bool{}
	)
	if e.hasRepo(p) {
		c, files, err := e.o.Repos.ListFiles(ctx, p.Slug, ref, Dir)
		if err != nil {
			return "", nil, repoError(err)
		}
		if c == "" && ref != DefaultRef {
			return "", nil, problems.NotFound.New("ref %q names nothing in the repository of %s", ref, p.Slug)
		}
		commit = c
		for _, f := range files {
			dir, base := path.Split(f.Path)
			name, ok := strings.CutSuffix(base, ".yaml")
			if dir != Dir+"/" || !ok {
				continue
			}
			src := Source{Kind: SourceRepository, Path: f.Path, Ref: ref, Commit: commit, Version: commit}
			src.Pipeline.Name = name
			b, _, err := e.o.Repos.ReadFile(ctx, p.Slug, commit, f.Path)
			if err != nil {
				return "", nil, repoError(err)
			}
			if hist, err := e.o.Repos.History(ctx, p.Slug, commit, f.Path, 1); err == nil && len(hist) > 0 {
				src.Version = hist[0].SHA
			}
			if pl, err := Parse(b, name); err != nil {
				src.Err = err
			} else {
				src.Pipeline = pl
			}
			seen[name] = true
			out = append(out, src)
		}
	}
	entries, err := fs.ReadDir(e.o.Templates, Dir)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return "", nil, fmt.Errorf("read bundled pipelines: %w", err)
	}
	for _, en := range entries {
		name, ok := strings.CutSuffix(en.Name(), ".yaml")
		if en.IsDir() || !ok || seen[name] {
			continue
		}
		b, err := fs.ReadFile(e.o.Templates, Path(name))
		if err != nil {
			return "", nil, fmt.Errorf("read bundled pipeline %s: %w", name, err)
		}
		src := Source{Kind: SourceTemplate, Path: Path(name), Ref: ref, Version: templateVersion(b)}
		src.Pipeline.Name = name
		if pl, err := Parse(b, name); err != nil {
			src.Err = err
		} else {
			src.Pipeline = pl
		}
		out = append(out, src)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Pipeline.Name < out[j].Pipeline.Name })
	return commit, out, nil
}
