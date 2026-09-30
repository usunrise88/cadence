//go:build integration

package server

import (
	"encoding/json"
	"net/url"
	"strings"
	"testing"
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
