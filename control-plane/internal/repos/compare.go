package repos

import (
	"bytes"
	"context"
	"sort"

	"github.com/usunrise88/cadence/control-plane/internal/diff3"
)

// ---------------------------------------------------------------- three-way comparison (branches.compare)

// MaxSideText is the size above which a version's text is left out of a comparison.
const MaxSideText = 128 << 10

// MaxCompareText is the text budget of one comparison; conflicting files past it come without texts.
const MaxCompareText = 1 << 20

// Side is one version of a file in a three-way comparison.
type Side struct {
	Exists bool
	Blob   string
	Bytes  int
	Binary bool
	Text   *string // conflicting text files only
	Cut    bool    // the text was left out (too large, or the budget ran out)
}

// FileCompare is one file of a branch compared three ways.
type FileCompare struct {
	Path     string
	Clean    bool
	Conflict string // content, add/add, modify/delete, binary; empty when clean
	Base     Side
	Main     Side
	Branch   Side
	Hunks    []diff3.Hunk
}

// Compare is a branch compared with main file by file.
type Compare struct {
	Branch      Branch
	Main        string
	Base        string
	FastForward bool
	Files       []FileCompare
	Cut         bool
}

// CompareBranch compares a branch with main in three ways: for every file the branch changed since the merge base,
// the base, main and branch versions and whether git's merge takes it cleanly; conflicting files come first, with
// their three texts (each up to MaxSideText, MaxCompareText in all) and the diff3 hunks that cover them.
func (s *Store) CompareBranch(ctx context.Context, slug, name string) (Compare, error) {
	b, err := s.Branch(ctx, slug, name)
	if err != nil {
		return Compare{}, err
	}
	main, err := s.Head(ctx, slug)
	if err != nil {
		return Compare{}, err
	}
	c := Compare{Branch: b, Main: main, Files: []FileCompare{}}
	if main != "" {
		if c.Base, err = s.bareString(ctx, slug, "merge-base", main, b.Head); err != nil && exitCode(err) != 1 {
			return Compare{}, err
		}
	}
	c.FastForward = main == "" || c.Base == main
	from := c.Base
	if from == "" {
		from = emptyTree
	}
	changed, err := s.Changes(ctx, slug, from, b.Head)
	if err != nil {
		return Compare{}, err
	}
	conflicts := map[string]bool{}
	if !c.FastForward {
		_, files, err := s.mergeTree(ctx, slug, main, b.Head)
		if err != nil {
			return Compare{}, err
		}
		for _, f := range files {
			conflicts[f] = true
		}
	}
	trees := [3]map[string]File{}
	for i, rev := range [3]string{c.Base, main, b.Head} {
		if trees[i], err = s.tree(ctx, slug, rev); err != nil {
			return Compare{}, err
		}
	}
	paths := make([]string, 0, len(changed)+len(conflicts))
	seen := map[string]bool{}
	for _, ch := range changed {
		paths = append(paths, ch.Path)
		seen[ch.Path] = true
	}
	for p := range conflicts {
		if !seen[p] {
			paths = append(paths, p)
		}
	}
	sort.SliceStable(paths, func(i, j int) bool {
		if conflicts[paths[i]] != conflicts[paths[j]] {
			return conflicts[paths[i]]
		}
		return paths[i] < paths[j]
	})
	budget := MaxCompareText
	for _, p := range paths {
		f := FileCompare{Path: p, Clean: !conflicts[p]}
		for i, side := range [3]*Side{&f.Base, &f.Main, &f.Branch} {
			if e, ok := trees[i][p]; ok {
				*side = Side{Exists: true, Blob: e.Blob, Bytes: e.Bytes}
			}
		}
		if !f.Clean {
			if err := s.conflictTexts(ctx, slug, &f, &budget); err != nil {
				return Compare{}, err
			}
			c.Cut = c.Cut || f.Base.Cut || f.Main.Cut || f.Branch.Cut
		}
		c.Files = append(c.Files, f)
	}
	return c, nil
}

// tree maps the files at rev by path (empty for no revision).
func (s *Store) tree(ctx context.Context, slug, rev string) (map[string]File, error) {
	out := map[string]File{}
	if rev == "" {
		return out, nil
	}
	_, files, err := s.ListFiles(ctx, slug, rev, "")
	if err != nil {
		return nil, err
	}
	for _, f := range files {
		out[f.Path] = f
	}
	return out, nil
}

// conflictTexts reads the three versions of a conflicting file, classifies the conflict and lays out its hunks.
func (s *Store) conflictTexts(ctx context.Context, slug string, f *FileCompare, budget *int) error {
	sides := [3]*Side{&f.Base, &f.Main, &f.Branch}
	total := 0
	for _, side := range sides {
		total += side.Bytes
	}
	fits := total <= *budget
	var texts [3][]string
	for i, side := range sides {
		if !side.Exists {
			continue
		}
		if side.Bytes > MaxSideText || !fits {
			side.Cut = true
			continue
		}
		b, err := s.bare(ctx, slug, "cat-file", "blob", side.Blob)
		if err != nil {
			return err
		}
		if isBinary(b) {
			side.Binary = true
			continue
		}
		t := string(b)
		side.Text = &t
		texts[i] = diff3.Lines(t)
	}
	switch {
	case f.Base.Binary || f.Main.Binary || f.Branch.Binary:
		f.Conflict = "binary"
	case !f.Base.Exists && f.Main.Exists && f.Branch.Exists:
		f.Conflict = "add/add"
	case !f.Main.Exists || !f.Branch.Exists:
		f.Conflict = "modify/delete"
	default:
		f.Conflict = "content"
	}
	if f.Conflict == "binary" || f.Base.Cut || f.Main.Cut || f.Branch.Cut {
		for _, side := range sides {
			side.Text = nil
		}
		return nil
	}
	*budget -= total
	f.Hunks = diff3.Compare(texts[0], texts[1], texts[2])
	return nil
}

// isBinary follows git's heuristic: a NUL byte in the first 8000 bytes.
func isBinary(b []byte) bool {
	if len(b) > 8000 {
		b = b[:8000]
	}
	return bytes.IndexByte(b, 0) >= 0
}
