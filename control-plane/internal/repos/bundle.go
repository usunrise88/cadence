package repos

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
)

// Project bundles (projects.export, bundles.adopt; phase 4 tail): a repository travels as a git bundle whose one
// branch is main at the exported commit, so `git clone repository.bundle` works too.

var shaRe = regexp.MustCompile(`^[0-9a-f]{40}([0-9a-f]{24})?$`)

// Bundle writes a git bundle of the repository at commit to dst, with refs/heads/main (and HEAD) at that commit.
// The commit is copied into a scratch repository first: the bare repository hides refs/cadence from upload-pack, and
// a bundle names refs, not bare commits.
func (s *Store) Bundle(ctx context.Context, slug, commit, dst string) error {
	if !s.Exists(slug) {
		return fmt.Errorf("repository %s: %w", slug, ErrNotFound)
	}
	if !shaRe.MatchString(commit) {
		return fmt.Errorf("bundle %s: %q is not a commit id", slug, commit)
	}
	tmp, err := os.MkdirTemp(s.data, "bundle-*")
	if err != nil {
		return fmt.Errorf("bundle %s: %w", slug, err)
	}
	defer func() { _ = os.RemoveAll(tmp) }()
	scratch := filepath.Join(tmp, "repo.git")
	if _, err := s.run(ctx, run{dir: tmp}, "init", "--quiet", "--bare", "--initial-branch="+Main, scratch); err != nil {
		return err
	}
	if _, err := s.bare(ctx, slug, "push", "--quiet", "--no-verify", "--", scratch, commit+":refs/heads/"+Main); err != nil {
		return err
	}
	abs, err := filepath.Abs(dst)
	if err != nil {
		return fmt.Errorf("bundle %s: %w", slug, err)
	}
	if _, err := s.run(ctx, run{dir: scratch}, "bundle", "create", "--quiet", abs, "HEAD", Main); err != nil {
		return err
	}
	return nil
}

// CreateFromBundle makes the project's bare repository from a git bundle's main (projects.new with a bundle) and the
// working clone, like Create. The bundle is verified first; a repository that exists already is an error.
func (s *Store) CreateFromBundle(ctx context.Context, slug, file string) error {
	if err := checkSlug(slug); err != nil {
		return err
	}
	abs, err := filepath.Abs(file)
	if err != nil {
		return fmt.Errorf("repository %s: %w", slug, err)
	}
	defer s.locked(slug)()
	if s.Exists(slug) {
		return fmt.Errorf("repository %s: %w", slug, ErrExists)
	}
	if _, err := s.run(ctx, run{dir: s.reposDir()}, "init", "--quiet", "--bare", "--initial-branch="+Main, slug+".git"); err != nil {
		return err
	}
	if _, err := s.bare(ctx, slug, "bundle", "verify", "--quiet", abs); err != nil {
		_ = os.RemoveAll(s.BarePath(slug))
		return fmt.Errorf("repository bundle: %w", err)
	}
	if _, err := s.bare(ctx, slug, "fetch", "--quiet", "--no-tags", "--", abs, "+refs/heads/"+Main+":refs/heads/"+Main); err != nil {
		_ = os.RemoveAll(s.BarePath(slug))
		return fmt.Errorf("fetch the repository bundle's %s: %w", Main, err)
	}
	return s.setup(ctx, slug)
}
