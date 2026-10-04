package repos

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
)

// A project bundle's repository: a git bundle of an older commit carries main at that commit, and a new repository
// made from it has that history and accepts commits on top.
func TestBundleAndCreateFromBundle(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	if err := s.Create(ctx, "demo"); err != nil {
		t.Fatal(err)
	}
	first := commit(t, s, "demo", "", "bootstrap", map[string]string{"project.yaml": "name: demo\n"})
	commit(t, s, "demo", "", "later", map[string]string{"NOTES.md": "later\n"})
	file := filepath.Join(t.TempDir(), "repository.bundle")
	if err := s.Bundle(ctx, "demo", "not-a-sha", file); err == nil {
		t.Fatal("a bundle of a ref name, not a commit id, was made")
	}
	if err := s.Bundle(ctx, "demo", first.SHA, file); err != nil {
		t.Fatal(err)
	}

	other := newStore(t)
	if err := other.CreateFromBundle(ctx, "copy", file); err != nil {
		t.Fatal(err)
	}
	if head, err := other.Head(ctx, "copy"); err != nil || head != first.SHA {
		t.Fatalf("head %q, %v; want %s", head, err, first.SHA)
	}
	if _, _, err := other.ReadFile(ctx, "copy", Main, "NOTES.md"); !errors.Is(err, ErrNotFound) {
		t.Errorf("a file of a later commit is in the bundle: %v", err)
	}
	next := commit(t, other, "copy", "", "import", map[string]string{"data.lock": "version: 1\n"})
	if next.Parent != first.SHA {
		t.Errorf("commit on the imported main: parent %s, want %s", next.Parent, first.SHA)
	}
	if err := other.CreateFromBundle(ctx, "copy", file); !errors.Is(err, ErrExists) {
		t.Errorf("second create: %v", err)
	}
	if err := other.CreateFromBundle(ctx, "broken", filepath.Join(t.TempDir(), "missing.bundle")); err == nil || other.Exists("broken") {
		t.Errorf("a missing bundle: %v (exists %v)", err, other.Exists("broken"))
	}
}
