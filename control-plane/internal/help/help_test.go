package help

import (
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/usunrise88/cadence/control-plane/internal/problems"
)

// docsHelp is the source the bundled copy mirrors.
const docsHelp = "../../../docs/help"

func TestBundledCopyIsInSync(t *testing.T) {
	src, dst := files(t, docsHelp), files(t, "content")
	for name, data := range src {
		if got, ok := dst[name]; !ok || !bytes.Equal(got, data) {
			t.Errorf("internal/help/content/%s is missing or stale; run `go run ./cmd/helpsync` in control-plane", name)
		}
	}
	for name := range dst {
		if _, ok := src[name]; !ok {
			t.Errorf("internal/help/content/%s has no source in docs/help; run `go run ./cmd/helpsync`", name)
		}
	}
}

func files(t *testing.T, root string) map[string][]byte {
	t.Helper()
	out := map[string][]byte{}
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		out[filepath.ToSlash(rel)], err = os.ReadFile(p)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestEveryErrorTypeHasItsArticle(t *testing.T) {
	lib, err := Bundled()
	if err != nil {
		t.Fatal(err)
	}
	slugs := map[string]bool{}
	for _, ty := range problems.Types() {
		slugs[ty.Slug] = true
		a, ok := lib.Get("errors." + ty.Slug)
		if !ok {
			t.Errorf("error type %s has no docs/help/errors/%s.md", ty.Slug, ty.Slug)
			continue
		}
		if !strings.Contains(strings.Join(a.Contexts, ","), "error:"+ty.Slug) {
			t.Errorf("errors.%s does not list context error:%s", ty.Slug, ty.Slug)
		}
	}
	for _, a := range lib.All() {
		if a.Section == "errors" && !slugs[strings.TrimPrefix(a.ID, "errors.")] {
			t.Errorf("article %s documents no registered error type", a.ID)
		}
	}
}

// requiredSections is the shape every article follows (docs/spec/11-ui-panels.md, Help).
var requiredSections = []string{
	"## What this is", "## Place in the loop", "## Fields and defaults", "## Commands", "## Playbooks", "## Sources",
}

func TestArticlesHaveTheStandardShape(t *testing.T) {
	lib, err := Bundled()
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range lib.All() {
		at := -1
		for _, h := range requiredSections {
			i := strings.Index(a.Body, "\n"+h+"\n")
			if strings.HasPrefix(a.Body, h+"\n") {
				i = 0
			}
			if i <= at {
				t.Errorf("%s: section %q missing or out of order", a.ID, h)
				break
			}
			at = i
		}
	}
}

func TestLoadAndSearch(t *testing.T) {
	art := func(title, summary, ctx, body string) *fstest.MapFile {
		return &fstest.MapFile{Data: []byte("---\ntitle: " + title + "\nsummary: " + summary + "\ncontexts: [" + ctx + "]\n---\n\n" + body)}
	}
	lib, err := Load(fstest.MapFS{
		"README.md":                 {Data: []byte("ignored")},
		"panels/library.md":         art("Library", "Browse registry versions", "panel:library", "Lists every kind."),
		"panels/inspector.md":       art("Inspector", "Fields of the selection", "panel:inspector", "Shows the library item."),
		"errors/not-found.md":       art("Not found", "Missing thing", "error:not-found", "Nothing here."),
		"guides/getting-started.md": art("Getting started", "First project", "", "Open the library, then create."),
	})
	if err != nil {
		t.Fatal(err)
	}
	if a, ok := lib.Get("panels.library"); !ok || a.Section != "panels" || a.Title != "Library" || !strings.HasPrefix(a.Body, "Lists") {
		t.Fatalf("Get = %+v, %v", a, ok)
	}
	ids := func(as []Article) string {
		var s []string
		for _, a := range as {
			s = append(s, a.ID)
		}
		return strings.Join(s, ",")
	}
	tests := []struct {
		q, context, want string
		limit            int
	}{
		{"", "panel:library", "panels.library", 20},
		{"", "panel:lib", "", 20},
		{"LIBRARY", "", "panels.library,guides.getting-started,panels.inspector", 20},
		{"library", "", "panels.library", 1},
		{"library create", "", "guides.getting-started", 20},
		{"", "", "errors.not-found,guides.getting-started,panels.inspector,panels.library", 20},
	}
	for _, tt := range tests {
		t.Run(tt.q+"|"+tt.context, func(t *testing.T) {
			if got := ids(lib.Search(tt.q, tt.context, tt.limit)); got != tt.want {
				t.Errorf("Search(%q, %q) = %s, want %s", tt.q, tt.context, got, tt.want)
			}
		})
	}
}

func TestLoadRejects(t *testing.T) {
	tests := map[string]fstest.MapFS{
		"unknown section": {"misc/x.md": {Data: []byte("---\ntitle: X\n---\n")}},
		"bad slug":        {"panels/Big_Name.md": {Data: []byte("---\ntitle: X\n---\n")}},
		"no front matter": {"panels/x.md": {Data: []byte("# X\n")}},
		"no title":        {"panels/x.md": {Data: []byte("---\nsummary: s\n---\n")}},
		"unclosed":        {"panels/x.md": {Data: []byte("---\ntitle: X\n")}},
	}
	for name, fsys := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := Load(fsys); err == nil {
				t.Error("Load succeeded, want error")
			}
		})
	}
}
