package server

import (
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/usunrise88/cadence/control-plane/internal/api"
	"github.com/usunrise88/cadence/control-plane/internal/events"
	"github.com/usunrise88/cadence/control-plane/internal/obs"
)

var pathParam = regexp.MustCompile(`\{[^}]+\}`)

// TestEveryOperationIsRouted is the contract test for the generated server: each method+path of the embedded
// spec reaches its own route (no chi 404/405), and nothing else is routed under /api.
func TestEveryOperationIsRouted(t *testing.T) {
	s := newTestServer(t, nil, events.NewHub(1), obs.NewMetrics())
	mux, ok := s.APIHandler().(chi.Routes)
	if !ok {
		t.Fatal("API handler is not a chi router")
	}
	spec, err := api.GetSpec()
	if err != nil {
		t.Fatal(err)
	}
	ops := 0
	for path, item := range spec.Paths.Map() {
		for method, op := range item.Operations() {
			ops++
			concrete := pathParam.ReplaceAllString(path, "demo-1")
			rctx := chi.NewRouteContext()
			if !mux.Match(rctx, method, concrete) {
				t.Errorf("%s (%s %s) is not routed", op.OperationID, method, path)
				continue
			}
			if got := rctx.RoutePattern(); got != path {
				t.Errorf("%s %s routes to %s", method, concrete, got)
			}
		}
	}
	routes := 0
	_ = chi.Walk(mux, func(string, string, http.Handler, ...func(http.Handler) http.Handler) error { routes++; return nil })
	if routes != ops {
		t.Errorf("router has %d routes, spec has %d operations", routes, ops)
	}
}

func TestPlannedOperationsAnswer501(t *testing.T) {
	srv := httptest.NewServer(newTestServer(t, nil, events.NewHub(1), obs.NewMetrics()).Handler())
	defer srv.Close()
	tests := []struct{ method, path string }{
		{http.MethodPost, "/api/projects/demo/mixes"},
		{http.MethodPost, "/api/projects/demo/runs"},
		{http.MethodPost, "/api/projects/demo/agent-sessions"},
		{http.MethodGet, "/api/mounts"},
	}
	for _, tt := range tests {
		t.Run(tt.method+" "+tt.path, func(t *testing.T) {
			req, _ := http.NewRequestWithContext(t.Context(), tt.method, srv.URL+tt.path, strings.NewReader("{}"))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Idempotency-Key", "key-12345678")
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			p := expectProblem(t, resp, http.StatusNotImplemented, "not-implemented")
			if !strings.Contains(p.Detail, "phase") {
				t.Errorf("detail %q should name the phase", p.Detail)
			}
		})
	}
}

func TestRoutingOutsideOperations(t *testing.T) {
	srv := httptest.NewServer(newTestServer(t, nil, events.NewHub(1), obs.NewMetrics()).Handler())
	defer srv.Close()
	do := func(method, path string, hdr ...string) *http.Response {
		t.Helper()
		req, _ := http.NewRequestWithContext(t.Context(), method, srv.URL+path, nil)
		for i := 0; i+1 < len(hdr); i += 2 {
			req.Header.Set(hdr[i], hdr[i+1])
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return resp
	}
	expectProblem(t, do(http.MethodGet, "/api/nope"), http.StatusNotFound, "not-found")
	expectProblem(t, do(http.MethodDelete, "/api/projects/demo"), http.StatusMethodNotAllowed, "method-not-allowed")
	expectProblem(t, do(http.MethodGet, "/api/help/errors.nope"), http.StatusNotFound, "not-found")
	expectProblem(t, do(http.MethodGet, "/api/events?topics=run.*.x", "Accept", "text/event-stream"), http.StatusBadRequest, "bad-request")
	expectProblem(t, do(http.MethodGet, "/api/events?limit=5000"), http.StatusUnprocessableEntity, "validation-failed")
	expectProblem(t, do(http.MethodPatch, "/api/projects/demo", "Idempotency-Key", "key-12345678"), http.StatusPreconditionRequired, "precondition-required")
	expectProblem(t, do(http.MethodPost, "/api/projects"), http.StatusBadRequest, "bad-request") // no Idempotency-Key

	for _, tt := range []struct {
		path, cache string
		status      int
	}{
		{"/", "no-cache", 200},
		{"/p/demo/w/Training", "no-cache", 200},
		{"/assets/missing.js", "", 404},
	} {
		resp := do(http.MethodGet, tt.path)
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode != tt.status || resp.Header.Get("Cache-Control") != tt.cache {
			t.Errorf("GET %s = %d cache %q", tt.path, resp.StatusCode, resp.Header.Get("Cache-Control"))
		}
		if tt.status == 200 && !strings.Contains(string(body), "<title>") {
			t.Errorf("GET %s did not serve index.html", tt.path)
		}
	}

	resp := do(http.MethodGet, "/api/help?context=error:conflict")
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != 200 || !strings.Contains(string(body), `"id":"errors.conflict"`) {
		t.Errorf("help.search by context = %d %s", resp.StatusCode, body)
	}
	resp = do(http.MethodGet, "/api/registry")
	body, _ = io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if strings.TrimSpace(string(body)) != `{"items":[],"kinds":[]}` {
		t.Errorf("registry.search = %s", body)
	}
	resp = do(http.MethodGet, "/api/me")
	body, _ = io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if strings.TrimSpace(string(body)) != `{"id":"usr_admin","kind":"user","name":"admin"}` {
		t.Errorf("me.get = %s", body)
	}
	resp = do(http.MethodGet, "/metrics")
	body, _ = io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if !strings.Contains(string(body), `cadence_http_request_duration_seconds_count{method="GET",route="/api/me",status="200"} 1`) {
		t.Errorf("metrics lack the request histogram:\n%s", body)
	}
}
