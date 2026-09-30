package repos

import (
	"context"
	"strings"
	"testing"

	"github.com/usunrise88/cadence/control-plane/internal/diff3"
)

func TestCompareBranch(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	if err := s.Create(ctx, "demo"); err != nil {
		t.Fatal(err)
	}
	commit(t, s, "demo", "", "bootstrap", map[string]string{
		"a.txt":     "one\ntwo\nthree\n",
		"clean.txt": "keep\n",
		"gone.txt":  "g\n",
		"big.txt":   "x\n",
		"bin.dat":   "b\n",
	})
	if _, err := s.CreateBranch(ctx, "demo", "session/s1", ""); err != nil {
		t.Fatal(err)
	}

	// Before main moves: a fast-forward, every file clean, no texts.
	commit(t, s, "demo", "session/s1", "turn 1", map[string]string{"clean.txt": "keep\nmore\n"})
	c, err := s.CompareBranch(ctx, "demo", "session/s1")
	if err != nil || !c.FastForward || len(c.Files) != 1 || !c.Files[0].Clean || c.Files[0].Branch.Text != nil {
		t.Fatalf("fast-forward comparison = %+v, %v", c, err)
	}

	big := strings.Repeat("0123456789abcdef\n", MaxSideText/17+10)
	commit(t, s, "demo", "", "main edits", map[string]string{
		"a.txt":    "one\nmain\nthree\n",
		"gone.txt": "g on main\n",
		"new.txt":  "main's new\n",
		"big.txt":  big + "main\n",
		"bin.dat":  "main\x00\n",
	})
	commit(t, s, "demo", "session/s1", "turn 2", map[string]string{
		"a.txt":   "one\nsession\nthree\nfour\n",
		"new.txt": "session's new\n",
		"big.txt": big + "session\n",
		"bin.dat": "session\x00\n",
	}, "gone.txt")

	c, err = s.CompareBranch(ctx, "demo", "session/s1")
	if err != nil {
		t.Fatal(err)
	}
	if c.FastForward || c.Base == "" || c.Branch.Name != "session/s1" {
		t.Fatalf("comparison header = %+v", c)
	}
	byPath := map[string]FileCompare{}
	var order []string
	for _, f := range c.Files {
		byPath[f.Path] = f
		order = append(order, f.Path)
	}
	// Conflicting files first, then clean ones, each by path.
	want := []string{"a.txt", "big.txt", "bin.dat", "gone.txt", "new.txt", "clean.txt"}
	if strings.Join(order, ",") != strings.Join(want, ",") {
		t.Fatalf("files = %v, want %v", order, want)
	}

	tests := []struct {
		path     string
		clean    bool
		conflict string
		texts    bool
		cut      bool
	}{
		{"a.txt", false, "content", true, false},
		{"big.txt", false, "content", false, true},
		{"bin.dat", false, "binary", false, false},
		{"gone.txt", false, "modify/delete", true, false},
		{"new.txt", false, "add/add", true, false},
		{"clean.txt", true, "", false, false},
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			f := byPath[tt.path]
			if f.Clean != tt.clean || f.Conflict != tt.conflict {
				t.Fatalf("%s: clean %v conflict %q, want %v %q", tt.path, f.Clean, f.Conflict, tt.clean, tt.conflict)
			}
			hasText := f.Main.Text != nil || f.Branch.Text != nil
			if hasText != tt.texts || (len(f.Hunks) > 0) != tt.texts {
				t.Fatalf("%s: texts %v hunks %d, want texts %v", tt.path, hasText, len(f.Hunks), tt.texts)
			}
			if cut := f.Base.Cut || f.Main.Cut || f.Branch.Cut; cut != tt.cut {
				t.Fatalf("%s: cut %v, want %v", tt.path, cut, tt.cut)
			}
		})
	}

	a := byPath["a.txt"]
	if *a.Base.Text != "one\ntwo\nthree\n" || *a.Main.Text != "one\nmain\nthree\n" || *a.Branch.Text != "one\nsession\nthree\nfour\n" {
		t.Fatalf("a.txt texts = %q / %q / %q", *a.Base.Text, *a.Main.Text, *a.Branch.Text)
	}
	if diff3.Conflicts(a.Hunks) != 1 || a.Hunks[1].Kind != diff3.Conflict || a.Hunks[1].Main != (diff3.Range{Start: 1, Count: 1}) ||
		a.Hunks[len(a.Hunks)-1].Kind != diff3.Branch {
		t.Fatalf("a.txt hunks = %+v", a.Hunks)
	}
	gone := byPath["gone.txt"]
	if !gone.Base.Exists || !gone.Main.Exists || gone.Branch.Exists || gone.Branch.Text != nil {
		t.Fatalf("gone.txt sides = %+v", gone)
	}
	if nw := byPath["new.txt"]; nw.Base.Exists || nw.Base.Text != nil || len(nw.Hunks) != 1 || nw.Hunks[0].Kind != diff3.Conflict {
		t.Fatalf("new.txt = %+v", nw)
	}
	if !c.Cut {
		t.Fatal("a file over the text limit did not mark the comparison cut")
	}
	if cl := byPath["clean.txt"]; cl.Base.Blob == "" || cl.Branch.Blob == "" || cl.Base.Blob == cl.Branch.Blob || cl.Main.Blob != cl.Base.Blob {
		t.Fatalf("clean.txt blobs = %+v", cl)
	}
}
