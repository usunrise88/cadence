package cli

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

var update = flag.Bool("update", false, "rewrite the golden files in testdata")

// recorder is a fake control plane: it records each request and answers with the canned response for its path.
type recorder struct {
	requests []string
	answers  map[string]answer // "METHOD /path" → answer
}

type answer struct {
	status int
	body   string
}

func (r *recorder) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	body, _ := io.ReadAll(req.Body)
	var b strings.Builder
	fmt.Fprintf(&b, "%s %s\n", req.Method, req.URL.Path)
	if q := req.URL.Query(); len(q) > 0 {
		keys := make([]string, 0, len(q))
		for k := range q {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			fmt.Fprintf(&b, "  query %s=%s\n", k, strings.Join(q[k], "|"))
		}
	}
	for _, h := range []string{"Authorization", "Idempotency-Key", "If-Match", "Content-Type", "Accept"} {
		if v := req.Header.Get(h); v != "" {
			fmt.Fprintf(&b, "  header %s: %s\n", h, v)
		}
	}
	if len(body) > 0 {
		fmt.Fprintf(&b, "  body %s\n", body)
	}
	r.requests = append(r.requests, b.String())
	a, ok := r.answers[req.Method+" "+req.URL.Path]
	if !ok {
		a = answer{200, `{"ok":true}`}
	}
	ct := "application/json"
	if a.status >= 400 {
		ct = "application/problem+json"
	}
	w.Header().Set("Content-Type", ct)
	w.WriteHeader(a.status)
	_, _ = io.WriteString(w, a.body)
}

func TestGolden(t *testing.T) {
	bodyFile := filepath.Join(t.TempDir(), "project.json")
	if err := os.WriteFile(bodyFile, []byte(`{"slug": "from-file", "name": "From a file"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	notFound := `{"type":"https://cadence.local/help/errors/not-found","title":"Not found","status":404,"detail":"no project with slug \"nope\""}`
	tests := []struct {
		name  string
		args  []string
		env   map[string]string
		stdin string
	}{
		{name: "get-path-and-query", args: []string{"adoptions", "list", "--project", "alpha", "--kind", "dataset_version"}},
		{name: "search", args: []string{"projects", "search", "--project", "alpha", "--q", "fleurs kind:dataset_version", "--limit=5"}},
		{name: "new-inline-body", args: []string{"projects", "new", "--body", `{"slug":"alpha","name":"Alpha"}`}},
		{name: "new-body-file", args: []string{"projects", "new", "--body", "@" + bodyFile, "--dry-run"}},
		{name: "new-body-stdin", args: []string{"projects", "new", "--body", "@-", "--idempotency-key", "my-key-0001"}, stdin: `{"slug":"stdin","name":"S"}`},
		{name: "edit-if-match", args: []string{"projects", "edit", "--project", "alpha", "--if-match", "3", "--body", `{"name":"Alpha 2"}`}},
		{name: "url-and-token-flags", args: []string{"projects", "get", "--project", "alpha", "--url", "{server}/api/", "--token", "cdk_flag"}},
		{name: "problem", args: []string{"projects", "get", "--project", "nope"}},
		{name: "accepted", args: []string{"projects", "archive", "--project", "alpha", "--if-match", `"2"`}},
		{name: "missing-required", args: []string{"projects", "edit", "--project", "alpha"}},
		{name: "bad-integer", args: []string{"projects", "search", "--project", "alpha", "--limit", "many"}},
		{name: "bad-enum", args: []string{"adoptions", "list", "--project", "alpha", "--kind", "model"}},
		{name: "unknown-flag", args: []string{"projects", "list", "--colour", "blue"}},
		{name: "invalid-body", args: []string{"projects", "new", "--body", "{nope"}},
		{name: "unknown-verb", args: []string{"projects", "explode"}},
		{name: "help", args: []string{"help"}},
		{name: "help-projects", args: []string{"help", "projects"}},
		{name: "projects-search-help", args: []string{"projects", "search", "--help"}},
		{name: "help-entity-verb", args: []string{"help", "get", "--id", "errors.not-found"}},
		{name: "views-are-exempt", args: []string{"views", "list"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := &recorder{answers: map[string]answer{
				"GET /api/projects/nope":           {404, notFound},
				"POST /api/projects/alpha:archive": {202, `{"approvalId":"apr_1"}`},
			}}
			srv := httptest.NewServer(rec)
			defer srv.Close()
			env := map[string]string{EnvURL: srv.URL, EnvToken: "cdk_env"}
			args := make([]string, len(tt.args))
			for i, a := range tt.args {
				args[i] = strings.ReplaceAll(a, "{server}", srv.URL)
			}
			var stdout, stderr bytes.Buffer
			keys := 0
			code := Run(context.Background(), args, func(k string) string { return env[k] }, strings.NewReader(tt.stdin),
				&stdout, &stderr, Options{NewKey: func() string { keys++; return fmt.Sprintf("key-%04d", keys) }})
			got := fmt.Sprintf("$ cadence %s\nexit %d\n--- requests\n%s--- stdout\n%s--- stderr\n%s",
				strings.Join(tt.args, " "), code, strings.Join(rec.requests, ""), stdout.String(), stderr.String())
			got = strings.ReplaceAll(got, bodyFile, "<file>")
			golden(t, tt.name, got)
		})
	}
}

func golden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", name+".golden")
	if *update {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run go test ./internal/cli -update)", err)
	}
	if string(want) != got {
		t.Errorf("%s differs (go test ./internal/cli -update rewrites it)\n--- got\n%s\n--- want\n%s", path, got, want)
	}
}

func TestBaseURL(t *testing.T) {
	for in, want := range map[string]string{
		"":                           "http://127.0.0.1:8080/api",
		"http://cp:8080":             "http://cp:8080/api",
		"http://cp:8080/":            "http://cp:8080/api",
		"http://cp:8080/api":         "http://cp:8080/api",
		"https://x.example/cadence/": "https://x.example/cadence/api",
	} {
		if got := BaseURL(in); got != want {
			t.Errorf("BaseURL(%q) = %q, want %q", in, got, want)
		}
	}
}

// Every generated command is reachable, and exempt operations (auth, me) are not commands.
func TestTable(t *testing.T) {
	seen := map[string]bool{}
	for _, o := range Operations {
		if seen[o.ID] {
			t.Errorf("%s twice", o.ID)
		}
		seen[o.ID] = true
		if o.ID != o.Entity+"."+o.Verb {
			t.Errorf("%s: entity %q verb %q", o.ID, o.Entity, o.Verb)
		}
		if got, ok := find(o.Entity, o.Verb); !ok || got.ID != o.ID {
			t.Errorf("%s is not found by entity and verb", o.ID)
		}
	}
	for _, exempt := range []string{"auth.login", "me.get", "workspaces.set", "views.set"} {
		if seen[exempt] {
			t.Errorf("exempt operation %s is a CLI command", exempt)
		}
	}
	if !seen["projects.search"] || !seen["projects.new"] {
		t.Error("projects.search and projects.new must be commands")
	}
	var out bytes.Buffer
	for _, o := range Operations {
		writeOpHelp(&out, o) // must not panic on any operation
	}
}
