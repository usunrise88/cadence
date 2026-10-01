//go:build integration

package server

import (
	"context"
	"encoding/json"
	"net/url"
	"strings"
	"testing"

	"github.com/usunrise88/cadence/control-plane/internal/pipelines/pipelinestest"
)

// recipes.new and recipes.edit (phase 2 · stream U): the Recipe document's form commits one file to main; an edit
// names the commit that last changed the file, a create refuses an existing path, agent-profile files are refused,
// a dry run commits nothing, and agents (default preset) are denied.
func TestRecipeWrites(t *testing.T) {
	e := start(t)
	e.newProject("demo")
	const path = "augment/telephony.yaml"
	esc := url.PathEscape(path)
	body := func(v map[string]any) string {
		b, _ := json.Marshal(v)
		return string(b)
	}

	// A dry run of a create answers the content and commits nothing.
	var r recipe
	e.ok(e.do("POST", "/api/projects/demo/recipes?dryRun=true", body(map[string]any{"path": path, "content": "seed: 1\n"}), "Idempotency-Key", e.key()), 200, &r) // dry runs answer 200
	if r.Content != "seed: 1\n" || len(r.History) != 0 {
		t.Fatalf("dry-run create %+v", r)
	}
	expectProblem(t, e.do("GET", "/api/projects/demo/recipes/"+esc, ""), 404, "not-found")

	e.ok(e.do("POST", "/api/projects/demo/recipes", body(map[string]any{"path": path, "content": "seed: 1\n"}), "Idempotency-Key", e.key()), 201, &r)
	if r.Content != "seed: 1\n" || len(r.History) != 1 || r.History[0].Message != "add "+path {
		t.Fatalf("create %+v", r)
	}
	first := r.History[0].Sha
	expectProblem(t, e.do("POST", "/api/projects/demo/recipes", body(map[string]any{"path": path, "content": "x"}), "Idempotency-Key", e.key()), 409, "conflict")

	// An edit on the last commit lands; an edit on a stale one is refused with 412.
	e.ok(e.do("PATCH", "/api/projects/demo/recipes/"+esc, body(map[string]any{"content": "seed: 2\n", "message": "augment: seed 2"}), "Idempotency-Key", e.key(), "If-Match", `"`+first+`"`), 200, &r)
	if r.Content != "seed: 2\n" || len(r.History) != 2 || r.History[0].Message != "augment: seed 2" || e.recipe("demo", path, "").Content != "seed: 2\n" {
		t.Fatalf("edit %+v", r)
	}
	expectProblem(t, e.do("PATCH", "/api/projects/demo/recipes/"+esc, body(map[string]any{"content": "seed: 3\n"}), "Idempotency-Key", e.key(), "If-Match", `"`+first+`"`), 412, "precondition-failed")
	// A dry run of an edit commits nothing.
	e.ok(e.do("PATCH", "/api/projects/demo/recipes/"+esc+"?dryRun=true", body(map[string]any{"content": "seed: 4\n"}), "Idempotency-Key", e.key(), "If-Match", `"`+r.History[0].Sha+`"`), 200, &r)
	if r.Content != "seed: 4\n" || e.recipe("demo", path, "").Content != "seed: 2\n" {
		t.Fatalf("dry-run edit %+v", r)
	}

	// Files the agent profile renders change only through agentProfile.edit; a missing file is created with new.
	agents := e.recipe("demo", "AGENTS.md", "")
	expectProblem(t, e.do("PATCH", "/api/projects/demo/recipes/AGENTS.md", body(map[string]any{"content": "# x\n"}), "Idempotency-Key", e.key(), "If-Match", `"`+agents.History[0].Sha+`"`), 409, "conflict")
	expectProblem(t, e.do("PATCH", "/api/projects/demo/recipes/"+url.PathEscape("augment/none.yaml"), body(map[string]any{"content": "x"}), "Idempotency-Key", e.key(), "If-Match", `"`+first+`"`), 404, "not-found")
	expectProblem(t, e.do("PATCH", "/api/projects/demo/recipes/"+esc, body(map[string]any{"content": "x"}), "Idempotency-Key", e.key(), "If-Match", `"3"`), 400, "bad-request")

	// Agents write in their session worktree: the default preset has no rule for these tools, so they are denied.
	resp := e.agent("POST", "/api/projects/demo/recipes", body(map[string]any{"path": "augment/agent.yaml", "content": "x"}), "Idempotency-Key", e.key())
	if resp.StatusCode != 403 {
		t.Fatalf("agent recipes.new: %d", resp.StatusCode)
	}
	if strings.Contains(e.recipe("demo", path, "").Content, "agent") {
		t.Fatal("agent write landed")
	}
}

// A pipeline file is planned before it is committed: one that would not plan never reaches main (the Recipe
// document's editor saves through recipes.edit), missing inputs aside — nobody has artifacts for a file yet.
func TestRecipeWritesValidatePipelines(t *testing.T) {
	e := start(t)
	e.newProject("demo")
	if err := pipelinestest.RegisterKinds(context.Background(), e.pool); err != nil {
		t.Fatal(err)
	}
	body := func(v map[string]any) string {
		b, _ := json.Marshal(v)
		return string(b)
	}
	const ok = "name: copy\ninputs: { text: text }\nsteps:\n  - { id: a, kind: echo@1, in: { text: $inputs.text } }\n"
	var r recipe
	e.ok(e.do("POST", "/api/projects/demo/recipes", body(map[string]any{"path": "pipelines/copy.yaml", "content": ok}), "Idempotency-Key", e.key()), 201, &r)
	esc := url.PathEscape("pipelines/copy.yaml")
	for name, content := range map[string]string{
		"an unknown kind":               strings.Replace(ok, "echo@1", "nosuch@1", 1),
		"a parameter not allowed":       strings.Replace(ok, "in: { text: $inputs.text } }", "in: { text: $inputs.text }, params: { nope: 1 } }", 1),
		"a name that is not the file's": strings.Replace(ok, "name: copy", "name: other", 1),
		"yaml that does not parse":      "name: copy\nsteps: [",
	} {
		t.Run(name, func(t *testing.T) {
			expectProblem(t, e.do("PATCH", "/api/projects/demo/recipes/"+esc, body(map[string]any{"content": content}),
				"Idempotency-Key", e.key(), "If-Match", `"`+r.History[0].Sha+`"`), 422, "pipeline-invalid")
		})
	}
	if got := e.recipe("demo", "pipelines/copy.yaml", "").Content; got != ok {
		t.Fatalf("a refused edit landed: %q", got)
	}
	expectProblem(t, e.do("POST", "/api/projects/demo/recipes", body(map[string]any{"path": "pipelines/bad.yaml", "content": "name: bad\nsteps: ["}),
		"Idempotency-Key", e.key()), 422, "pipeline-invalid")
	// Other files are not pipelines: anything goes.
	e.ok(e.do("POST", "/api/projects/demo/recipes", body(map[string]any{"path": "notes/plan.yaml", "content": "steps: ["}), "Idempotency-Key", e.key()), 201, nil)
}
