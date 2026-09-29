//go:build integration

package server

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/usunrise88/cadence/control-plane/internal/events"
	"github.com/usunrise88/cadence/control-plane/internal/obs"
	"github.com/usunrise88/cadence/control-plane/internal/storage"
	"github.com/usunrise88/cadence/control-plane/internal/testdb"
	"github.com/usunrise88/cadence/control-plane/migrations"
)

func TestMain(m *testing.M) { testdb.Main(m) }

type env struct {
	t       *testing.T
	url     string
	pool    *pgxpool.Pool
	metrics *obs.Metrics
	keys    atomic.Int64
}

// start runs the whole control plane on a fresh database: migrations, dispatcher, HTTP server.
func start(t *testing.T) *env {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	pool, err := storage.Open(ctx, testdb.New(t))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := storage.Migrate(ctx, pool, migrations.FS); err != nil {
		t.Fatal(err)
	}
	hub, metrics := events.NewHub(64), obs.NewMetrics()
	d := events.NewDispatcher(pool, hub, slog.New(slog.NewTextHandler(io.Discard, nil)), metrics.EventsDispatched)
	d.PollInterval = 200 * time.Millisecond
	done := make(chan struct{})
	go func() { defer close(done); _ = d.Run(ctx) }()
	srv := httptest.NewServer(newTestServer(t, pool, hub, metrics).Handler())
	t.Cleanup(func() {
		hub.Close()
		srv.Close()
		cancel()
		<-done
		pool.Close()
	})
	return &env{t: t, url: srv.URL, pool: pool, metrics: metrics}
}

func (e *env) key() string { return fmt.Sprintf("test-key-%08d", e.keys.Add(1)) }

// do sends a request; headers come as name, value pairs.
func (e *env) do(method, path, body string, hdr ...string) *http.Response {
	e.t.Helper()
	var r io.Reader
	if body != "" {
		r = strings.NewReader(body)
	}
	req, err := http.NewRequestWithContext(e.t.Context(), method, e.url+path, r)
	if err != nil {
		e.t.Fatal(err)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	return resp
}

// ok expects status and decodes the JSON body into v (if not nil); it returns the response.
func (e *env) ok(resp *http.Response, status int, v any) *http.Response {
	e.t.Helper()
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != status {
		e.t.Fatalf("%s %s = %d, want %d: %s", resp.Request.Method, resp.Request.URL.Path, resp.StatusCode, status, body)
	}
	if v != nil {
		if err := json.Unmarshal(body, v); err != nil {
			e.t.Fatalf("decode %s: %v", body, err)
		}
	}
	return resp
}

func (e *env) count(sql string) int {
	e.t.Helper()
	var n int
	if err := e.pool.QueryRow(context.Background(), sql).Scan(&n); err != nil {
		e.t.Fatal(err)
	}
	return n
}

type project struct {
	ID          string `json:"id"`
	Slug        string `json:"slug"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Rev         int    `json:"rev"`
	ArchivedAt  string `json:"archivedAt"`
}

func (e *env) newProject(slug string) project {
	e.t.Helper()
	var p project
	e.ok(e.do("POST", "/api/projects", `{"slug":"`+slug+`","name":"`+slug+`"}`, "Idempotency-Key", e.key()), 201, &p)
	return p
}

func TestProjectsRevisions(t *testing.T) {
	e := start(t)
	var p project
	resp := e.ok(e.do("POST", "/api/projects", `{"slug":"demo","name":"Demo","description":"first"}`,
		"Idempotency-Key", e.key()), 201, &p)
	if p.Rev != 1 || !strings.HasPrefix(p.ID, "prj_") || resp.Header.Get("ETag") != `"1"` {
		t.Fatalf("created %+v etag %s", p, resp.Header.Get("ETag"))
	}
	if !strings.HasPrefix(resp.Header.Get("Cadence-Command-Id"), "cmd_") {
		t.Errorf("no command id header")
	}

	resp = e.ok(e.do("GET", "/api/projects/demo", ""), 200, &p)
	if resp.Header.Get("ETag") != `"1"` || p.Description != "first" {
		t.Fatalf("get: %+v %s", p, resp.Header.Get("ETag"))
	}

	for i, ifMatch := range []string{`"1"`, `2`, `W/"3"`} {
		resp = e.ok(e.do("PATCH", "/api/projects/demo", fmt.Sprintf(`{"name":"Demo %d"}`, i),
			"Idempotency-Key", e.key(), "If-Match", ifMatch), 200, &p)
		if p.Rev != i+2 || resp.Header.Get("ETag") != strconv.Quote(strconv.Itoa(i+2)) || p.Description != "first" {
			t.Fatalf("edit with If-Match %s: %+v %s", ifMatch, p, resp.Header.Get("ETag"))
		}
	}

	pr := expectProblem(t, e.do("PATCH", "/api/projects/demo", `{"name":"late"}`,
		"Idempotency-Key", e.key(), "If-Match", `"2"`), 412, "precondition-failed")
	if pr.CurrentRev == nil || *pr.CurrentRev != 4 {
		t.Fatalf("412 currentRev = %v", pr.CurrentRev)
	}
	expectProblem(t, e.do("PATCH", "/api/projects/demo", `{"name":"x"}`, "Idempotency-Key", e.key()), 428, "precondition-required")
	expectProblem(t, e.do("PATCH", "/api/projects/demo", `{"name":"x"}`, "Idempotency-Key", e.key(), "If-Match", "nope"), 400, "bad-request")
	expectProblem(t, e.do("PATCH", "/api/projects/nope", `{"name":"x"}`, "Idempotency-Key", e.key(), "If-Match", `"1"`), 404, "not-found")
	expectProblem(t, e.do("POST", "/api/projects", `{"slug":"demo","name":"Again"}`, "Idempotency-Key", e.key()), 409, "conflict")

	v := expectProblem(t, e.do("POST", "/api/projects", `{"slug":"Bad Slug","name":"","extra":1}`, "Idempotency-Key", e.key()),
		422, "validation-failed")
	paths := map[string]bool{}
	for _, fe := range v.Errors {
		paths[fe.Path] = true
	}
	if !paths["/slug"] || !paths["/name"] {
		t.Errorf("validation errors %+v should name /slug and /name", v.Errors)
	}
	expectProblem(t, e.do("POST", "/api/projects", `{"slug":`, "Idempotency-Key", e.key()), 400, "bad-request")

	e.ok(e.do("POST", "/api/projects/demo:archive", "", "Idempotency-Key", e.key(), "If-Match", `"4"`), 200, &p)
	if p.ArchivedAt == "" || p.Rev != 5 {
		t.Fatalf("archive: %+v", p)
	}
	expectProblem(t, e.do("POST", "/api/projects/demo:archive", "", "Idempotency-Key", e.key(), "If-Match", `"5"`), 409, "conflict")
	var list struct{ Items []project }
	e.ok(e.do("GET", "/api/projects", ""), 200, &list)
	if len(list.Items) != 0 {
		t.Errorf("archived project listed by default: %+v", list.Items)
	}
	e.ok(e.do("GET", "/api/projects?archived=true", ""), 200, &list)
	if len(list.Items) != 1 {
		t.Errorf("archived=true: %+v", list.Items)
	}
	// One event per successful mutation: created, 3 edits, archived.
	if n := e.count("SELECT count(*) FROM events"); n != 5 {
		t.Errorf("%d events, want 5", n)
	}
}

func TestIdempotency(t *testing.T) {
	e := start(t)
	body := `{"slug":"idem","name":"Idem"}`
	first := e.do("POST", "/api/projects", body, "Idempotency-Key", "same-key-1")
	firstBody, _ := io.ReadAll(first.Body)
	_ = first.Body.Close()
	// Same request, JSON re-serialised: replayed byte for byte.
	again := e.do("POST", "/api/projects", `{ "name": "Idem", "slug": "idem" }`, "Idempotency-Key", "same-key-1")
	againBody, _ := io.ReadAll(again.Body)
	_ = again.Body.Close()
	if first.StatusCode != 201 || again.StatusCode != 201 || string(firstBody) != string(againBody) {
		t.Fatalf("replay: %d %s / %d %s", first.StatusCode, firstBody, again.StatusCode, againBody)
	}
	for _, h := range []string{"ETag", "Cadence-Command-Id", "Content-Type"} {
		if first.Header.Get(h) != again.Header.Get(h) {
			t.Errorf("header %s: %q vs %q", h, first.Header.Get(h), again.Header.Get(h))
		}
	}
	if again.Header.Get("Idempotent-Replayed") != "true" || first.Header.Get("Idempotent-Replayed") != "" {
		t.Errorf("Idempotent-Replayed marks only the replay")
	}
	if n := e.count("SELECT count(*) FROM projects"); n != 1 {
		t.Fatalf("%d projects after a replay", n)
	}
	if n := e.count("SELECT count(*) FROM events"); n != 1 {
		t.Fatalf("%d events after a replay", n)
	}

	expectProblem(t, e.do("POST", "/api/projects", `{"slug":"other","name":"Other"}`, "Idempotency-Key", "same-key-1"),
		422, "idempotency-key-reused")
	expectProblem(t, e.do("PATCH", "/api/projects/idem", `{"name":"x"}`, "Idempotency-Key", "same-key-1", "If-Match", `"1"`),
		422, "idempotency-key-reused")

	// A failed command stores nothing: the same key works once the conflict is gone.
	expectProblem(t, e.do("POST", "/api/projects", body, "Idempotency-Key", "retry-key-1"), 409, "conflict")
	if n := e.count("SELECT count(*) FROM idempotency_keys WHERE key = 'retry-key-1'"); n != 0 {
		t.Fatalf("failed command stored its key")
	}
	if got := testutil.ToFloat64(e.metrics.Commands.WithLabelValues("projects.new", "replayed")); got != 1 {
		t.Errorf("replayed counter = %v", got)
	}

	// Concurrent repeats of one request: the first runs, the others wait on the key's lock and replay it.
	const n = 6
	bodies := make(chan string, n)
	var wg sync.WaitGroup
	for range n {
		wg.Go(func() {
			resp := e.do("POST", "/api/projects", `{"slug":"race","name":"Race"}`, "Idempotency-Key", "race-key-1")
			b, _ := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			bodies <- fmt.Sprintf("%d %s", resp.StatusCode, b)
		})
	}
	wg.Wait()
	close(bodies)
	want := <-bodies
	for b := range bodies {
		if b != want || !strings.HasPrefix(b, "201 ") {
			t.Fatalf("concurrent repeats differ: %q vs %q", b, want)
		}
	}
	if c := e.count("SELECT count(*) FROM projects WHERE slug = 'race'"); c != 1 {
		t.Fatalf("%d projects from concurrent repeats", c)
	}
}

func TestDryRunWritesNothing(t *testing.T) {
	e := start(t)
	var p project
	resp := e.ok(e.do("POST", "/api/projects?dryRun=true", `{"slug":"dry","name":"Dry"}`, "Idempotency-Key", e.key()), 200, &p)
	if p.Slug != "dry" || p.Rev != 1 || resp.Header.Get("ETag") != `"1"` {
		t.Fatalf("dry run result %+v", p)
	}
	e.newProject("real")
	e.ok(e.do("PATCH", "/api/projects/real?dryRun=true", `{"name":"Renamed"}`, "Idempotency-Key", e.key(), "If-Match", `"1"`), 200, &p)
	if p.Name != "Renamed" || p.Rev != 2 {
		t.Fatalf("dry edit %+v", p)
	}
	// Dry runs still validate against the state: a taken slug is reported.
	expectProblem(t, e.do("POST", "/api/projects?dryRun=true", `{"slug":"real","name":"x"}`, "Idempotency-Key", e.key()), 409, "conflict")

	e.ok(e.do("GET", "/api/projects/real", ""), 200, &p)
	if p.Name != "real" || p.Rev != 1 {
		t.Fatalf("dry edit changed the project: %+v", p)
	}
	expectProblem(t, e.do("GET", "/api/projects/dry", ""), 404, "not-found")
	if n := e.count("SELECT count(*) FROM events"); n != 1 {
		t.Fatalf("%d events, want only real's creation", n)
	}
	if n := e.count("SELECT count(*) FROM idempotency_keys"); n != 1 {
		t.Fatalf("dry runs stored idempotency keys")
	}
}

func TestWorkspaces(t *testing.T) {
	e := start(t)
	p := e.newProject("wsp")
	path := "/api/me/projects/wsp/workspaces/Training%20set"
	layout := `{"schemaVersion":1,"layout":{"grid":{"root":1}},"panels":{"library":{"pinnedTo":"run:1","viewState":{"sort":"name"}}}}`

	expectProblem(t, e.do("GET", path, ""), 404, "not-found")
	expectProblem(t, e.do("PUT", path, layout, "Idempotency-Key", e.key(), "If-Match", `"1"`), 412, "precondition-failed")

	var w struct {
		ID     string
		Name   string
		Rev    int
		Layout map[string]any
		Panels map[string]struct {
			PinnedTo string `json:"pinnedTo"`
		}
	}
	resp := e.ok(e.do("PUT", path, layout, "Idempotency-Key", e.key()), 200, &w)
	if w.Rev != 1 || w.Name != "Training set" || w.Panels["library"].PinnedTo != "run:1" || resp.Header.Get("ETag") != `"1"` {
		t.Fatalf("created %+v", w)
	}
	expectProblem(t, e.do("PUT", path, layout, "Idempotency-Key", e.key()), 428, "precondition-required")
	e.ok(e.do("PUT", path, `{"schemaVersion":2,"layout":{},"panels":{}}`, "Idempotency-Key", e.key(), "If-Match", `"1"`), 200, &w)
	if w.Rev != 2 {
		t.Fatalf("saved %+v", w)
	}
	pr := expectProblem(t, e.do("PUT", path, layout, "Idempotency-Key", e.key(), "If-Match", `"1"`), 412, "precondition-failed")
	if *pr.CurrentRev != 2 {
		t.Fatalf("currentRev %d", *pr.CurrentRev)
	}
	w.Layout = nil // decoding merges into existing maps
	resp = e.ok(e.do("GET", path, ""), 200, &w)
	if resp.Header.Get("ETag") != `"2"` || len(w.Layout) != 0 {
		t.Fatalf("get %+v", w)
	}
	var list struct {
		Items []struct {
			Name string
			Rev  int
		}
	}
	e.ok(e.do("GET", "/api/me/projects/wsp/workspaces", ""), 200, &list)
	if len(list.Items) != 1 || list.Items[0].Name != "Training set" || list.Items[0].Rev != 2 {
		t.Fatalf("list %+v", list)
	}
	expectProblem(t, e.do("GET", "/api/me/projects/nope/workspaces", ""), 404, "not-found")
	expectProblem(t, e.do("PUT", "/api/me/projects/nope/workspaces/x", layout, "Idempotency-Key", e.key()), 404, "not-found")
	expectProblem(t, e.do("PUT", "/api/me/projects/wsp/workspaces/bad.name", layout, "Idempotency-Key", e.key()), 422, "validation-failed")

	var page struct {
		Items []struct {
			Topic     string
			Type      string
			ProjectID string `json:"projectId"`
			Payload   struct {
				Workspace map[string]any
			}
		}
	}
	e.ok(e.do("GET", "/api/events?topics=entity.workspace.*", ""), 200, &page)
	if len(page.Items) != 2 || page.Items[0].Type != "workspace.set" || page.Items[0].ProjectID != p.ID ||
		page.Items[1].Payload.Workspace["rev"] != float64(2) || page.Items[0].Payload.Workspace["layout"] != nil {
		t.Fatalf("workspace events %+v", page.Items)
	}
}

// sseFrame is one parsed server-sent event.
type sseFrame struct {
	id   int64
	data map[string]any
}

// openStream connects to the SSE endpoint and returns frames as they arrive and a func that disconnects.
func (e *env) openStream(path string, hdr ...string) (<-chan sseFrame, func()) {
	e.t.Helper()
	ctx, cancel := context.WithCancel(e.t.Context())
	req, _ := http.NewRequestWithContext(ctx, "GET", e.url+path, nil)
	req.Header.Set("Accept", "text/event-stream")
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	if resp.StatusCode != 200 || resp.Header.Get("Content-Type") != "text/event-stream" {
		e.t.Fatalf("stream: %d %s", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	frames := make(chan sseFrame, 100)
	go func() {
		defer close(frames)
		defer func() { _ = resp.Body.Close() }()
		sc := bufio.NewScanner(resp.Body)
		var f sseFrame
		for sc.Scan() {
			line := sc.Text()
			switch {
			case strings.HasPrefix(line, "event:"):
				e.t.Errorf("frames must not carry an event: line (%s)", line)
			case strings.HasPrefix(line, "id: "):
				f.id, _ = strconv.ParseInt(strings.TrimPrefix(line, "id: "), 10, 64)
			case strings.HasPrefix(line, "data: "):
				_ = json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &f.data)
			case line == "" && f.data != nil:
				frames <- f
				f = sseFrame{}
			}
		}
	}()
	return frames, func() {
		cancel()
		for range frames { //nolint:revive // drain until the reader exits
		}
	}
}

// expectFrames reads n frames and returns them; it fails on timeout.
func expectFrames(t *testing.T, frames <-chan sseFrame, n int) []sseFrame {
	t.Helper()
	var out []sseFrame
	for len(out) < n {
		select {
		case f, ok := <-frames:
			if !ok {
				t.Fatalf("stream closed after %d of %d frames", len(out), n)
			}
			out = append(out, f)
		case <-time.After(5 * time.Second):
			t.Fatalf("timed out after %d of %d frames", len(out), n)
		}
	}
	return out
}

func expectQuiet(t *testing.T, frames <-chan sseFrame) {
	t.Helper()
	select {
	case f := <-frames:
		t.Fatalf("unexpected frame %+v", f)
	case <-time.After(700 * time.Millisecond):
	}
}

func TestEventStreamFilterAndResume(t *testing.T) {
	e := start(t)
	before := e.newProject("before") // committed before anyone listens

	frames, disconnect := e.openStream("/api/events?topics=entity.project.*")
	expectQuiet(t, frames) // no after / Last-Event-ID: live only
	a := e.newProject("alpha")
	e.ok(e.do("PUT", "/api/me/projects/alpha/workspaces/Main", `{"schemaVersion":1,"layout":{},"panels":{}}`,
		"Idempotency-Key", e.key()), 200, nil) // entity.workspace.*: filtered out
	e.ok(e.do("PATCH", "/api/projects/alpha", `{"name":"Alpha"}`, "Idempotency-Key", e.key(), "If-Match", `"1"`), 200, nil)
	got := expectFrames(t, frames, 2)
	if got[0].data["type"] != "project.created" || got[1].data["type"] != "project.edited" {
		t.Fatalf("frames %+v", got)
	}
	causedBy, _ := got[0].data["causedBy"].(map[string]any)
	actor, _ := got[0].data["actor"].(map[string]any)
	payload, _ := got[1].data["payload"].(map[string]any)
	proj, _ := payload["project"].(map[string]any)
	if got[0].data["projectId"] != a.ID || actor["id"] != "usr_admin" || !strings.HasPrefix(fmt.Sprint(causedBy["commandId"]), "cmd_") ||
		proj["name"] != "Alpha" || proj["rev"] != float64(2) || float64(got[1].id) != got[1].data["seq"] {
		t.Fatalf("event shape %+v", got)
	}
	expectQuiet(t, frames)
	lastSeen := got[1].id
	disconnect()

	// While disconnected: two project events and one workspace event.
	e.newProject("gamma")
	e.ok(e.do("PUT", "/api/me/projects/gamma/workspaces/Main", `{"schemaVersion":1,"layout":{},"panels":{}}`,
		"Idempotency-Key", e.key()), 200, nil)
	e.ok(e.do("PATCH", "/api/projects/alpha", `{"description":"d"}`, "Idempotency-Key", e.key(), "If-Match", `"2"`), 200, nil)

	// Reconnect with Last-Event-ID (it wins over ?after): exactly the missed matching events, in order, then live.
	frames, disconnect = e.openStream("/api/events?topics=entity.project.*&after=0", "Last-Event-ID", strconv.FormatInt(lastSeen, 10))
	defer disconnect()
	got = expectFrames(t, frames, 2)
	if got[0].data["type"] != "project.created" || got[1].data["type"] != "project.edited" ||
		got[0].id <= lastSeen || got[1].id <= got[0].id {
		t.Fatalf("resumed frames %+v", got)
	}
	expectQuiet(t, frames)
	e.newProject("delta")
	if f := expectFrames(t, frames, 1)[0]; f.id <= got[1].id || f.data["type"] != "project.created" {
		t.Fatalf("live after replay %+v", f)
	}

	// Project filter keeps that project's events (and events without a projectId).
	byProject, stop := e.openStream("/api/events?project=before&after=0")
	defer stop()
	if f := expectFrames(t, byProject, 1)[0]; f.data["projectId"] != before.ID {
		t.Fatalf("project filter %+v", f)
	}
	expectQuiet(t, byProject)

	// JSON form: paging with lastSeq.
	var page struct {
		Items   []struct{ Seq int64 }
		LastSeq int64
	}
	e.ok(e.do("GET", "/api/events?limit=2", ""), 200, &page)
	if len(page.Items) != 2 || page.LastSeq != page.Items[1].Seq {
		t.Fatalf("page %+v", page)
	}
	e.ok(e.do("GET", fmt.Sprintf("/api/events?after=%d&limit=1000", page.LastSeq), ""), 200, &page)
	if total := e.count("SELECT count(*) FROM events"); len(page.Items) != total-2 {
		t.Fatalf("second page has %d of %d", len(page.Items), total-2)
	}
	if got := testutil.ToFloat64(e.metrics.SSEClients); got != 2 {
		t.Errorf("sse clients gauge = %v, want 2", got)
	}
	if testutil.ToFloat64(e.metrics.EventsDispatched) < 5 {
		t.Errorf("dispatched counter did not move")
	}
}

func TestHealthz(t *testing.T) {
	e := start(t)
	var h health
	e.ok(e.do("GET", "/healthz", ""), 200, &h)
	if h.Status != "ok" || h.DB != "ok" || h.Version != "test" {
		t.Fatalf("healthz %+v", h)
	}
}

// The error articles ship with the binary and resolve through help.get.
func TestHelpArticlesResolve(t *testing.T) {
	e := start(t)
	var a struct{ ID, Title, Body string }
	e.ok(e.do("GET", "/api/help/errors.precondition-failed", ""), 200, &a)
	if a.Title == "" || !strings.Contains(a.Body, "## What this is") {
		t.Fatalf("article %+v", a)
	}
}
