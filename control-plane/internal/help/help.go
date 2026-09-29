// Package help serves the help articles bundled in the binary.
//
// The articles are written in docs/help/<section>/<slug>.md; content/ is a generated mirror of that directory
// (go run ./cmd/helpsync) because go:embed cannot reach outside the module. A test fails when the mirror is stale.
package help

import (
	"bytes"
	"embed"
	"fmt"
	"io/fs"
	"path"
	"regexp"
	"slices"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

//go:embed content
var bundled embed.FS

// Sections are the directories of docs/help, as in the contract's HelpArticle.section.
var Sections = []string{"errors", "panels", "steps", "guides", "shell"}

var slugPattern = regexp.MustCompile(`^[a-z0-9-]+$`)

// Article is one help page. Its id is <section>.<slug>.
type Article struct {
	ID       string
	Section  string
	Title    string
	Summary  string
	Contexts []string
	Body     string
}

// Library is the set of loaded articles.
type Library struct {
	articles []Article // sorted by id
}

// Bundled loads the articles embedded in the binary.
func Bundled() (*Library, error) {
	sub, err := fs.Sub(bundled, "content")
	if err != nil {
		return nil, fmt.Errorf("open bundled help: %w", err)
	}
	return Load(sub)
}

// Load reads <section>/<slug>.md files from fsys. Markdown files at the root (a README) are ignored; a file in an
// unknown section, with an invalid slug or without a title is an error.
func Load(fsys fs.FS) (*Library, error) {
	lib := &Library{}
	err := fs.WalkDir(fsys, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || path.Ext(p) != ".md" || !strings.Contains(p, "/") {
			return err
		}
		section, file := path.Split(p)
		section = strings.TrimSuffix(section, "/")
		slug := strings.TrimSuffix(file, ".md")
		if !slices.Contains(Sections, section) {
			return fmt.Errorf("help %s: unknown section %q (want one of %v)", p, section, Sections)
		}
		if !slugPattern.MatchString(slug) {
			return fmt.Errorf("help %s: slug must be lowercase letters, digits and dashes", p)
		}
		raw, err := fs.ReadFile(fsys, p)
		if err != nil {
			return fmt.Errorf("read help %s: %w", p, err)
		}
		a, err := parse(raw)
		if err != nil {
			return fmt.Errorf("help %s: %w", p, err)
		}
		a.ID, a.Section = section+"."+slug, section
		lib.articles = append(lib.articles, a)
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(lib.articles, func(i, j int) bool { return lib.articles[i].ID < lib.articles[j].ID })
	return lib, nil
}

type frontMatter struct {
	Title    string   `yaml:"title"`
	Summary  string   `yaml:"summary"`
	Contexts []string `yaml:"contexts"`
}

// parse splits YAML front matter (between --- lines) from the markdown body.
func parse(raw []byte) (Article, error) {
	raw = bytes.ReplaceAll(raw, []byte("\r\n"), []byte("\n"))
	rest, ok := bytes.CutPrefix(raw, []byte("---\n"))
	if !ok {
		return Article{}, fmt.Errorf("missing front matter (--- at the first line)")
	}
	head, body, ok := bytes.Cut(rest, []byte("\n---\n"))
	if !ok {
		return Article{}, fmt.Errorf("front matter is not closed by ---")
	}
	var fm frontMatter
	if err := yaml.Unmarshal(head, &fm); err != nil {
		return Article{}, fmt.Errorf("front matter: %w", err)
	}
	if strings.TrimSpace(fm.Title) == "" {
		return Article{}, fmt.Errorf("front matter has no title")
	}
	return Article{Title: fm.Title, Summary: fm.Summary, Contexts: fm.Contexts, Body: strings.TrimLeft(string(body), "\n")}, nil
}

// Get returns the article with id <section>.<slug>.
func (l *Library) Get(id string) (Article, bool) {
	i, found := sort.Find(len(l.articles), func(i int) int { return strings.Compare(id, l.articles[i].ID) })
	if !found {
		return Article{}, false
	}
	return l.articles[i], true
}

// All returns every article by id.
func (l *Library) All() []Article { return slices.Clone(l.articles) }

// Search returns up to limit articles. context (e.g. panel:library) keeps articles that list it exactly; q keeps
// articles containing every word of q (case-insensitive) and ranks title hits above summary hits above body hits.
// Without q, results are in id order.
func (l *Library) Search(q, context string, limit int) []Article {
	words := strings.Fields(strings.ToLower(q))
	type hit struct {
		a     Article
		score int
	}
	var hits []hit
	for _, a := range l.articles {
		if context != "" && !slices.Contains(a.Contexts, context) {
			continue
		}
		if s, ok := score(a, words); ok {
			hits = append(hits, hit{a, s})
		}
	}
	sort.SliceStable(hits, func(i, j int) bool { return hits[i].score > hits[j].score })
	out := make([]Article, 0, min(limit, len(hits)))
	for _, h := range hits[:min(limit, len(hits))] {
		out = append(out, h.a)
	}
	return out
}

func score(a Article, words []string) (int, bool) {
	title, summary, body := strings.ToLower(a.Title), strings.ToLower(a.Summary), strings.ToLower(a.Body)
	total := 0
	for _, w := range words {
		s := 0
		if strings.Contains(title, w) {
			s += 10
		}
		if strings.Contains(summary, w) {
			s += 4
		}
		s += min(strings.Count(body, w), 5)
		if s == 0 {
			return 0, false
		}
		total += s
	}
	return total, true
}
