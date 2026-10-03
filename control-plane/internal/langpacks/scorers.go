package langpacks

import (
	"context"
	"fmt"
	"io/fs"
	"path"
	"sync"
	"time"

	"github.com/usunrise88/cadence/control-plane/internal/problems"
	"github.com/usunrise88/cadence/control-plane/internal/projects"
	"github.com/usunrise88/cadence/control-plane/internal/repos"
	"github.com/usunrise88/cadence/control-plane/internal/storage"
	"github.com/usunrise88/cadence/control-plane/internal/textnorm"
)

// Scorers finds the scoring normalizer of a locale (R21) for code that compares text in the control plane — the
// search index. A project's own pack decides for its locales (normalizer.yaml scoring.normalizer at main); without
// one, or outside a project, the starter pack Cadence ships for the locale decides. The reference resolves through
// the registry (a pinned version, or a collection's newest frozen version). No pack, no reference or no registry
// version means no normalizer: the caller keeps its own behaviour.
//
// Answers are cached for TTL: a pack edit or a newly frozen normalizer version reaches the index within it.
type Scorers struct {
	Tree  fs.FS        // the templates tree (starter packs under lang/)
	Repos *repos.Store // project repositories; nil reads starter packs only
	TTL   time.Duration
	Now   func() time.Time

	mu    sync.Mutex
	refs  map[string]cached[string]               // projectID|locale → scoring reference ("" for none)
	norms map[string]cached[*textnorm.Normalizer] // reference → normalizer (nil for none)
}

type cached[T any] struct {
	v  T
	at time.Time
}

// NewScorers returns a resolver over the templates tree and the project repositories.
func NewScorers(tree fs.FS, st *repos.Store) *Scorers {
	return &Scorers{Tree: tree, Repos: st, TTL: 30 * time.Second, Now: time.Now}
}

// For returns the scoring normalizer for locale in project projectID ("" outside a project), or nil.
func (s *Scorers) For(ctx context.Context, q storage.Querier, projectID, locale string) (*textnorm.Normalizer, error) {
	if locale == "" {
		return nil, nil
	}
	ref, err := s.reference(ctx, q, projectID, locale)
	if err != nil || ref == "" {
		return nil, err
	}
	return s.normalizer(ctx, q, ref)
}

func (s *Scorers) fresh(at time.Time) bool { return s.Now().Sub(at) < s.TTL }

func (s *Scorers) reference(ctx context.Context, q storage.Querier, projectID, locale string) (string, error) {
	key := projectID + "|" + locale
	s.mu.Lock()
	c, ok := s.refs[key]
	s.mu.Unlock()
	if ok && s.fresh(c.at) {
		return c.v, nil
	}
	ref, err := s.lookup(ctx, q, projectID, locale)
	if err != nil {
		return "", err
	}
	s.mu.Lock()
	if s.refs == nil {
		s.refs = map[string]cached[string]{}
	}
	s.refs[key] = cached[string]{v: ref, at: s.Now()}
	s.mu.Unlock()
	return ref, nil
}

// lookup reads the scoring reference: the project's pack first, then the starter pack.
func (s *Scorers) lookup(ctx context.Context, q storage.Querier, projectID, locale string) (string, error) {
	if projectID != "" && s.Repos != nil {
		p, err := projects.GetByID(ctx, q, projectID)
		if pe, ok := problems.As(err); ok && pe.Type == problems.NotFound {
			p, err = projects.Project{}, nil
		}
		if err != nil {
			return "", err
		}
		if p.Repository != nil && s.Repos.Exists(p.Slug) {
			if dirs, _, err := Dirs(ctx, s.Repos, p.Slug, repos.Main); err == nil {
				if dir, ok := Match(dirs, locale); ok {
					if r, err := ReadPack(ctx, s.Repos, p.Slug, repos.Main, dir); err == nil {
						if n, err := r.Normalizer(); err == nil && n.Scoring.Normalizer != "" {
							return n.Scoring.Normalizer, nil
						}
					}
				}
			}
		}
	}
	if s.Tree == nil {
		return "", nil
	}
	shipped, err := Bundled(s.Tree)
	if err != nil {
		return "", err
	}
	dir, ok := Match(shipped, locale)
	if !ok {
		return "", nil
	}
	pk, err := FromTree(s.Tree, path.Join(Dir, dir), dir)
	if err != nil {
		return "", err
	}
	n, err := pk.Normalizer()
	if err != nil {
		return "", fmt.Errorf("starter pack %s: %w", dir, err)
	}
	return n.Scoring.Normalizer, nil
}

func (s *Scorers) normalizer(ctx context.Context, q storage.Querier, ref string) (*textnorm.Normalizer, error) {
	s.mu.Lock()
	c, ok := s.norms[ref]
	s.mu.Unlock()
	if ok && s.fresh(c.at) {
		return c.v, nil
	}
	var n *textnorm.Normalizer
	v, err := ResolveScoring(ctx, q, ref)
	switch pe, isProblem := problems.As(err); {
	case isProblem && pe.Type == problems.NotFound:
		// No such normalizer (yet): no folding.
	case err != nil:
		return nil, err
	default:
		spec, err := textnorm.Parse(v.Payload)
		if err != nil {
			return nil, fmt.Errorf("normalizer %s %s: %w", v.Name, v.Version, err)
		}
		if n, err = textnorm.New(spec); err != nil {
			return nil, err
		}
	}
	s.mu.Lock()
	if s.norms == nil {
		s.norms = map[string]cached[*textnorm.Normalizer]{}
	}
	s.norms[ref] = cached[*textnorm.Normalizer]{v: n, at: s.Now()}
	s.mu.Unlock()
	return n, nil
}
