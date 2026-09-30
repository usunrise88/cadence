//go:build integration

package server

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/usunrise88/cadence/control-plane/internal/cli"
)

// The generated CLI drives the real API: create, read and edit a project, dry runs and errors included.
func TestCLIAgainstServer(t *testing.T) {
	e := start(t)
	env := func(k string) string {
		if k == cli.EnvURL {
			return e.url
		}
		return ""
	}
	run := func(args ...string) (int, string, string) {
		t.Helper()
		var stdout, stderr bytes.Buffer
		code := cli.Run(context.Background(), args, env, strings.NewReader(""), &stdout, &stderr, cli.Options{})
		return code, stdout.String(), stderr.String()
	}

	if code, out, errOut := run("projects", "new", "--dry-run", "--body", `{"slug":"cli","name":"CLI"}`); code != 0 || !strings.Contains(out, `"slug": "cli"`) {
		t.Fatalf("dry run: exit %d\n%s%s", code, out, errOut)
	}
	if code, _, _ := run("projects", "get", "--project", "cli"); code != cli.ExitError {
		t.Fatalf("a dry run created the project (exit %d)", code)
	}

	code, out, errOut := run("projects", "new", "--body", `{"slug":"cli","name":"CLI"}`)
	var p project
	if code != 0 || json.Unmarshal([]byte(out), &p) != nil || p.Slug != "cli" || p.Rev != 1 {
		t.Fatalf("projects new: exit %d\n%s%s", code, out, errOut)
	}
	code, out, errOut = run("projects", "get", "--project", "cli")
	if code != 0 || json.Unmarshal([]byte(out), &p) != nil || p.Name != "CLI" {
		t.Fatalf("projects get: exit %d\n%s%s", code, out, errOut)
	}
	code, out, errOut = run("projects", "edit", "--project", "cli", "--if-match", "1", "--body", `{"name":"CLI 2"}`)
	if code != 0 || json.Unmarshal([]byte(out), &p) != nil || p.Name != "CLI 2" || p.Rev != 2 {
		t.Fatalf("projects edit: exit %d\n%s%s", code, out, errOut)
	}

	// A stale revision is the server's problem, printed to standard error with its help article; exit 1.
	code, out, errOut = run("projects", "edit", "--project", "cli", "--if-match", "1", "--body", `{"name":"CLI 3"}`)
	if code != cli.ExitError || out != "" || !strings.Contains(errOut, "HTTP 412") || !strings.Contains(errOut, "errors.precondition-failed") {
		t.Fatalf("stale edit: exit %d\nstdout %s\nstderr %s", code, out, errOut)
	}
	// Forgetting If-Match never reaches the server.
	if code, _, errOut := run("projects", "edit", "--project", "cli", "--body", `{"name":"x"}`); code != cli.ExitUsage || !strings.Contains(errOut, "missing --if-match") {
		t.Fatalf("missing if-match: exit %d\n%s", code, errOut)
	}
}
