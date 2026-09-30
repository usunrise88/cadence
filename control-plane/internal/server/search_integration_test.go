//go:build integration

package server

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/usunrise88/cadence/control-plane/internal/help"
	"github.com/usunrise88/cadence/control-plane/internal/registry"
	"github.com/usunrise88/cadence/control-plane/templates"
)

func mustHelp(t *testing.T) *help.Library {
	t.Helper()
	lib, err := help.Bundled()
	if err != nil {
		t.Fatal(err)
	}
	return lib
}

type searchHit struct {
	Kind      string             `json:"kind"`
	ID        string             `json:"id"`
	Title     string             `json:"title"`
	Ref       string             `json:"ref"`
	Scope     string             `json:"scope"`
	ProjectID string             `json:"projectId"`
	Project   string             `json:"project"`
	Status    string             `json:"status"`
	Lang      string             `json:"lang"`
	Tags      []string           `json:"tags"`
	Numbers   map[string]float64 `json:"numbers"`
	Snippet   string             `json:"snippet"`
	Actor     *struct{ Kind, ID string }
}

type searchResult struct {
	Q          string
	Text       string
	Qualifiers []struct{ Field, Op, Value, Raw string }
	Groups     []struct {
		Kind  string
		Items []searchHit
	}
	Total     int
	Truncated bool
}

func (r searchResult) hits() []searchHit {
	var out []searchHit
	for _, g := range r.Groups {
		out = append(out, g.Items...)
	}
	return out
}

func (r searchResult) has(kind, title string) bool {
	for _, h := range r.hits() {
		if h.Kind == kind && h.Title == title {
			return true
		}
	}
	return false
}

func (r searchResult) kinds() []string {
	var out []string
	for _, g := range r.Groups {
		out = append(out, g.Kind)
	}
	return out
}

// search runs projects.search as the admin (or with extra headers) and decodes the answer.
func (e *env) search(project, q string, hdr ...string) searchResult {
	e.t.Helper()
	var r searchResult
	e.ok(e.do("GET", "/api/projects/"+project+":search?q="+url.QueryEscape(q), "", hdr...), 200, &r)
	return r
}

// eventually polls cond until it holds: the index follows the outbox asynchronously (within seconds).
func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestSearchFollowsEvents(t *testing.T) {
	e := start(t)
	var p project
	e.ok(e.do("POST", "/api/projects", `{"slug":"alpha","name":"Hebrew telephony","description":"Call-centre audio"}`,
		"Idempotency-Key", e.key()), 201, &p)
	eventually(t, "the new project in the index", func() bool { return e.search("alpha", "telephony").has("project", "Hebrew telephony") })

	// Typos and identifiers.
	r := e.search("alpha", "telephnoy")
	if !r.has("project", "Hebrew telephony") {
		t.Errorf("typo not tolerated: %+v", r.hits())
	}
	if r := e.search("alpha", "alph"); !r.has("project", "Hebrew telephony") {
		t.Errorf("slug prefix not found: %+v", r.hits())
	}
	h := e.search("alpha", "kind:project centre").hits()
	if len(h) != 1 || h[0].Ref != "project:alpha" || h[0].Project != "alpha" || h[0].Status != "active" ||
		h[0].Actor == nil || h[0].Actor.ID != "usr_admin" || !strings.Contains(h[0].Snippet, "Call-centre") {
		t.Errorf("hit = %+v", h)
	}

	// An edit and an archive are searchable within seconds.
	e.ok(e.do("PATCH", "/api/projects/alpha", `{"name":"Hebrew call centre"}`, "Idempotency-Key", e.key(), "If-Match", `"1"`), 200, nil)
	eventually(t, "the rename", func() bool { return e.search("alpha", "kind:project").has("project", "Hebrew call centre") })
	e.ok(e.do("POST", "/api/projects/alpha:archive", "", "Idempotency-Key", e.key(), "If-Match", `"2"`), 200, nil)
	eventually(t, "the archive", func() bool { return e.search("alpha", "status:archived").has("project", "Hebrew call centre") })

	// Registry versions and collections (registered at start by Seed) join through their events.
	if _, err := registry.Seed(context.Background(), e.pool, templates.FS, time.Now()); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the seeded registry", func() bool { return e.search("alpha", "kind:dataset fleurs").Total >= 2 })
	r = e.search("alpha", "flerus")
	if kinds := strings.Join(r.kinds(), ","); !strings.Contains(kinds, "dataset_version") || !strings.Contains(kinds, "registry_collection") {
		t.Errorf("flerus should find dataset versions and collections, got groups %v", kinds)
	}
	r = e.search("alpha", "kind:dataset_version lang:he hours>=2 tag:read-speech actor:automation")
	if r.Total != 1 || r.hits()[0].Lang != "he-IL" || r.hits()[0].Numbers["hours"] != 2 || r.hits()[0].Scope != "registry" {
		t.Errorf("qualifiers: %+v", r.hits())
	}
	if len(r.Qualifiers) != 5 || r.Qualifiers[2].Field != "hours" || r.Qualifiers[2].Op != ">=" {
		t.Errorf("qualifiers echoed: %+v", r.Qualifiers)
	}
	if r := e.search("alpha", "kind:dataset updated:<2000-01-01"); r.Total != 0 {
		t.Errorf("updated:<2000 found %d hits", r.Total)
	}

	// Jobs carry numbers; project work ranks before the registry.
	j := e.enqueue(p.ID, map[string]any{})
	eventually(t, "the job", func() bool { return e.search("alpha", "kind:job progress>=0").Total == 1 })
	r = e.search("alpha", "")
	if r.Groups[0].Kind != "project" && r.Groups[0].Kind != "job" {
		t.Errorf("the current project's work should come first, groups %v", r.kinds())
	}
	if r := e.search("alpha", j.ID); len(r.hits()) == 0 || r.hits()[0].ID != j.ID {
		t.Errorf("job id not found: %+v", r.hits())
	}

	// Help articles are indexed at start.
	if r := e.search("alpha", "kind:help qualifier"); !r.has("help_article", "Invalid search query") {
		t.Errorf("help article not found: %+v", r.hits())
	}

	// Unknown qualifiers are a clear 400 with the list.
	pr := expectProblem(t, e.do("GET", "/api/projects/alpha:search?q="+url.QueryEscape("knd:job"), ""), 400, "invalid-query")
	if !strings.Contains(pr.Detail, `unknown qualifier "knd:"`) || !strings.Contains(pr.Detail, "kind:") {
		t.Errorf("detail = %q", pr.Detail)
	}
	expectProblem(t, e.do("GET", "/api/projects/alpha:search?q="+url.QueryEscape("foo<3"), ""), 400, "invalid-query")
	expectProblem(t, e.do("GET", "/api/projects/nope:search?q=x", ""), 404, "not-found")
}

func TestSearchScope(t *testing.T) {
	e, _ := startAuth(t)
	cookie := e.setup()
	var a, b project
	e.ok(e.do("POST", "/api/projects", `{"slug":"alpha","name":"Alpha voice"}`, web(cookie, "Idempotency-Key", e.key())...), 201, &a)
	e.ok(e.do("POST", "/api/projects", `{"slug":"beta","name":"Beta voice"}`, web(cookie, "Idempotency-Key", e.key())...), 201, &b)
	if _, err := registry.Seed(context.Background(), e.pool, templates.FS, time.Now()); err != nil {
		t.Fatal(err)
	}
	admin := web(cookie)
	eventually(t, "both projects and the registry", func() bool {
		r := e.search("alpha", "scope:all voice", admin...)
		return r.has("project", "Alpha voice") && r.has("project", "Beta voice") && e.search("alpha", "fleurs", admin...).Total > 0
	})
	if r := e.search("alpha", "voice", admin...); r.has("project", "Beta voice") {
		t.Errorf("default scope is the current project (and the registry): %+v", r.hits())
	}
	if r := e.search("alpha", "project:beta voice", admin...); !r.has("project", "Beta voice") || r.has("project", "Alpha voice") {
		t.Errorf("project:beta: %+v", r.hits())
	}

	var onlyAlpha, alphaAndRegistry created
	e.ok(e.do("POST", "/api/credentials", `{"name":"a","scope":{"project":"alpha"}}`, web(cookie, "Idempotency-Key", e.key())...), 201, &onlyAlpha)
	e.ok(e.do("POST", "/api/credentials", `{"name":"ar","scope":{"project":"alpha","registryRead":true}}`,
		web(cookie, "Idempotency-Key", e.key())...), 201, &alphaAndRegistry)
	tok, tokR := bearer(onlyAlpha.Token), bearer(alphaAndRegistry.Token)

	// A project token never sees other projects' work, whatever the query says.
	for _, q := range []string{"voice", "scope:all voice", "scope:all", "Beta"} {
		for _, hdr := range [][]string{tok, tokR} {
			for _, h := range e.search("alpha", q, hdr...).hits() {
				if h.ProjectID == b.ID || h.Scope == "instance" {
					t.Errorf("a token scoped to alpha found %+v for %q", h, q)
				}
			}
		}
	}
	if r := e.search("alpha", "scope:all voice", tok...); !r.has("project", "Alpha voice") {
		t.Errorf("scope:all with a project token still covers its project: %+v", r.hits())
	}
	expectProblem(t, e.do("GET", "/api/projects/beta:search?q=voice", "", tok...), 403, "forbidden")
	expectProblem(t, e.do("GET", "/api/projects/alpha:search?q="+url.QueryEscape("project:beta"), "", tok...), 403, "forbidden")

	// Registry hits need registry read.
	for _, h := range e.search("alpha", "fleurs", tok...).hits() {
		if h.Scope == "registry" {
			t.Errorf("a token without registry read found %+v", h)
		}
	}
	expectProblem(t, e.do("GET", "/api/projects/alpha:search?q="+url.QueryEscape("scope:registry"), "", tok...), 403, "forbidden")
	if r := e.search("alpha", "fleurs", tokR...); r.Total == 0 {
		t.Error("a token with registry read finds registry versions")
	}
	if r := e.search("alpha", "scope:registry voice", tokR...); r.Total != 0 {
		t.Errorf("scope:registry returned project work: %+v", r.hits())
	}
}

type savedView struct {
	ID, Name, Query, Description string
	Rev                          int
}

func TestSavedViews(t *testing.T) {
	e := start(t)
	e.newProject("alpha")
	e.newProject("beta")
	put := func(name, body string, hdr ...string) *http.Response {
		return e.do("PUT", "/api/me/projects/alpha/views/"+url.PathEscape(name), body, append([]string{"Idempotency-Key", e.key()}, hdr...)...)
	}

	// Dry run writes nothing; create needs no If-Match; a second create does.
	e.ok(e.do("PUT", "/api/me/projects/alpha/views/Failed%20jobs?dryRun=true", `{"query":"kind:job status:failed"}`, "Idempotency-Key", e.key()), 200, nil)
	if n := e.count("SELECT count(*) FROM saved_views"); n != 0 {
		t.Fatalf("dry run saved %d views", n)
	}
	var v savedView
	resp := e.ok(put("Failed jobs", `{"query":"kind:job status:failed","description":"What broke"}`), 200, &v)
	if v.Rev != 1 || !strings.HasPrefix(v.ID, "vew_") || v.Description != "What broke" || resp.Header.Get("ETag") != `"1"` {
		t.Fatalf("created %+v", v)
	}
	expectProblem(t, put("Failed jobs", `{"query":"kind:job"}`), 428, "precondition-required")
	e.ok(put("Failed jobs", `{"query":"kind:job status:failed updated:>2026-09-01"}`, "If-Match", `"1"`), 200, &v)
	if v.Rev != 2 || v.Query != "kind:job status:failed updated:>2026-09-01" {
		t.Errorf("updated %+v", v)
	}
	pr := expectProblem(t, put("Failed jobs", `{"query":"kind:job"}`, "If-Match", `"1"`), 412, "precondition-failed")
	if pr.CurrentRev == nil || *pr.CurrentRev != 2 {
		t.Errorf("stale save: %+v", pr)
	}
	expectProblem(t, put("Other", `{"query":"knd:job"}`), 400, "invalid-query")

	var list struct{ Items []savedView }
	e.ok(e.do("GET", "/api/me/projects/alpha/views", ""), 200, &list)
	if len(list.Items) != 1 || list.Items[0].Name != "Failed jobs" {
		t.Errorf("views.list = %+v", list.Items)
	}
	resp = e.ok(e.do("GET", "/api/me/projects/alpha/views/Failed%20jobs", ""), 200, &v)
	if resp.Header.Get("ETag") != `"2"` {
		t.Errorf("views.get etag %q", resp.Header.Get("ETag"))
	}
	expectProblem(t, e.do("GET", "/api/me/projects/beta/views/Failed%20jobs", ""), 404, "not-found")

	// Per user: an agent session has its own saved searches.
	var agentList struct{ Items []savedView }
	e.ok(e.agent("GET", "/api/me/projects/alpha/views", ""), 200, &agentList)
	if len(agentList.Items) != 0 {
		t.Errorf("another actor sees the admin's saved searches: %+v", agentList.Items)
	}
	if n := e.count("SELECT count(*) FROM events WHERE type = 'saved_search.set' AND topic LIKE 'entity.saved_search.vew_%' AND payload->'savedSearch'->>'query' IS NULL"); n != 2 {
		t.Errorf("saved_search.set events without the query: %d", n)
	}
}
