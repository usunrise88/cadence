package mounts

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestFnmatchRegexp(t *testing.T) {
	for _, tc := range []struct {
		glob, path string
		want       bool
	}{
		{"test/*", "test/a.wav", true},
		{"test/*", "test/sub/a.wav", true}, // * crosses / (fnmatch)
		{"*/test/*", "fleurs/test/a.wav", true},
		{"*/test/*", "fleurs/train/a.wav", false},
		{"*.txt", "a/b.txt", true},
		{"a?.wav", "ab.wav", true},
		{"[!t]*", "test/a", false},
		{"[ab].wav", "b.wav", true},
		{"a.wav", "a_wav", false},
	} {
		got, err := excludeGlobs([]any{tc.glob})
		if err != nil {
			t.Fatal(err)
		}
		if m := got[0].MatchString(tc.path); m != tc.want {
			t.Errorf("%q matches %q = %v, want %v", tc.glob, tc.path, m, tc.want)
		}
	}
}

func TestMountURIs(t *testing.T) {
	params := map[string]any{
		"path":    "mount://corpora/fleurs/rev",
		"exclude": []any{"test/*"},
		"nested":  map[string]any{"more": []any{"mount://b/x#t=1,2", "mount://corpora/fleurs/rev"}},
		"other":   "cas",
	}
	if got := mountURIs(params, nil); !slices.Equal(got, []string{"mount://b/x#t=1,2", "mount://corpora/fleurs/rev"}) {
		t.Fatalf("uris %v", got)
	}
	if n, p := splitURI("mount://corpora/fleurs/rev/"); n != "corpora" || p != "fleurs/rev" {
		t.Fatalf("split %s %s", n, p)
	}
}

// The path reader's stamps change with a file's modification time.
func TestPathReaderStamps(t *testing.T) {
	root := t.TempDir()
	f := filepath.Join(root, "d", "a.wav")
	if err := os.MkdirAll(filepath.Dir(f), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	stamp := func() string {
		var s string
		if err := (pathReader{root: root}).WalkStamped(context.Background(), "d", func(rel string, size int64, st string) error {
			if rel != "d/a.wav" || size != 1 {
				t.Errorf("listed %s (%d)", rel, size)
			}
			s = st
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		return s
	}
	before := stamp()
	info, err := os.Stat(f)
	if err != nil {
		t.Fatal(err)
	}
	later := info.ModTime().Add(1e9)
	if err := os.Chtimes(f, later, later); err != nil {
		t.Fatal(err)
	}
	if after := stamp(); after == before || after == "" {
		t.Fatalf("stamp %q → %q", before, after)
	}
}
