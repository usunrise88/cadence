//go:build integration

package server

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestAgentSessionGuardedFilesWaitForAPerson: a when-clean session whose branch touches gates.yaml (or the language
// packs, project.yaml, the agents' settings) is not auto-merged; it waits with a reason naming the files, and a
// person's accept merges it (spec 05 guardrail; audit 2026-10-02).
func TestAgentSessionGuardedFilesWaitForAPerson(t *testing.T) {
	h := startHost(t)
	h.newProject("demo")
	var s sessionView
	h.ok(h.do("POST", "/api/projects/demo/agent-sessions", `{"prompt":"loosen the gate"}`, "Idempotency-Key", h.key()), 201, &s)
	w := h.claim("host-a")
	if len(w.Start) != 1 {
		t.Fatalf("claim %+v", w)
	}
	h.report(s.ID, map[string]any{"hostId": "host-a", "state": map[string]any{"state": "running"}})
	dir := t.TempDir()
	remote := strings.Replace(h.url, "http://", "http://x-token:"+w.Start[0].Token+"@", 1) + "/git/demo.git"
	if out, err := gitCmd(t, dir, "clone", "-q", "-b", s.Branch, remote, "wt"); err != nil {
		t.Fatalf("clone: %v %s", err, out)
	}
	wt := filepath.Join(dir, "wt")
	if err := os.WriteFile(filepath.Join(wt, "gates.yaml"), []byte("version: 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wt, "pipelines", "agent.yaml"), []byte("name: agent\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", "."}, {"commit", "-q", "-m", "turn"}, {"push", "-q", "origin", s.Branch}} {
		if out, err := gitCmd(t, wt, args...); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}

	s = h.report(s.ID, map[string]any{"hostId": "host-a", "state": map[string]any{"state": "done"}})
	if s.State != "done" || s.Merge.State != "pending" {
		t.Fatalf("a branch touching gates.yaml: %+v, want merge pending", s.Merge)
	}
	if _, _, err := h.repos.Repos().ReadFile(t.Context(), "demo", "main", "pipelines/agent.yaml"); err == nil {
		t.Fatal("main has the session's files before a person accepted")
	}
	var reason string
	if err := h.pool.QueryRow(t.Context(), `SELECT payload->>'reason' FROM events WHERE type = 'branch.waiting' AND payload->>'sessionId' = $1`,
		s.ID).Scan(&reason); err != nil || !strings.Contains(reason, "need a person") || !strings.Contains(reason, "gates.yaml") {
		t.Fatalf("waiting reason %q %v", reason, err)
	}

	s = h.session(s.ID)
	h.ok(h.do("POST", "/api/agent-sessions/"+s.ID+":accept", "", "Idempotency-Key", h.key(), "If-Match", ifMatch(s.Rev)), 200, &s)
	if s.Merge.State != "merged" || h.recipe("demo", "gates.yaml", "").Content != "version: 1\n" {
		t.Fatalf("accepted %+v", s.Merge)
	}
}
