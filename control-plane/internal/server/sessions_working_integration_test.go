//go:build integration

package server

import (
	"encoding/json"
	"net/url"
	"testing"
)

// TestAgentSessionWorkingChanges: the host's watcher reports uncommitted files while a turn runs; the session keeps
// the last report and each file entering, changing in or leaving the set is a recipe.{path} event (recipe.working)
// on the session branch, attributed to the agent. Ending the session clears what is left.
func TestAgentSessionWorkingChanges(t *testing.T) {
	h := startHost(t)
	h.newProject("demo")
	var s sessionView
	h.ok(h.do("POST", "/api/projects/demo/agent-sessions", `{"prompt":"go"}`, "Idempotency-Key", h.key()), 201, &s)
	if w := h.claim("host-a"); len(w.Start) != 1 {
		t.Fatalf("claim %+v", w)
	}
	h.report(s.ID, map[string]any{"hostId": "host-a", "state": map[string]any{"state": "running", "busy": true, "turn": 1}})

	type working struct {
		Turn      int  `json:"turn"`
		Truncated bool `json:"truncated"`
		Files     []struct {
			Path   string `json:"path"`
			Status string `json:"status"`
			Bytes  *int   `json:"bytes"`
		} `json:"files"`
		At string `json:"at"`
	}
	read := func() *working {
		var v struct {
			Working *working `json:"working"`
		}
		h.ok(h.do("GET", "/api/agent-sessions/"+s.ID, ""), 200, &v)
		return v.Working
	}
	type ev struct {
		Topic   string         `json:"topic"`
		Type    string         `json:"type"`
		Payload map[string]any `json:"payload"`
		Actor   struct {
			Kind      string `json:"kind"`
			SessionID string `json:"sessionId"`
		} `json:"actor"`
	}
	eventsOf := func(path string) []ev {
		var list struct{ Items []ev }
		h.ok(h.do("GET", "/api/events?topics="+url.QueryEscape("recipe."+path)+"&limit=50", ""), 200, &list)
		return list.Items
	}
	report := func(files ...map[string]any) {
		if files == nil {
			files = []map[string]any{}
		}
		h.report(s.ID, map[string]any{"hostId": "host-a", "working": map[string]any{"turn": 1, "files": files, "truncated": false}})
	}

	report(map[string]any{"path": "pipelines/a.yaml", "status": "modified", "bytes": 12, "additions": 1, "deletions": 1},
		map[string]any{"path": "NOTES-new.md", "status": "added", "bytes": 5})
	w := read()
	if w == nil || len(w.Files) != 2 || w.Files[0].Path != "NOTES-new.md" || w.At == "" || w.Turn != 1 {
		t.Fatalf("working after the first report %+v", w)
	}
	evs := eventsOf("pipelines/a.yaml")
	if len(evs) != 1 || evs[0].Type != "recipe.working" || evs[0].Payload["status"] != "modified" ||
		evs[0].Payload["branch"] != s.Branch || evs[0].Payload["sessionId"] != s.ID || evs[0].Payload["working"] != true ||
		evs[0].Actor.Kind != "agent" || evs[0].Actor.SessionID != s.ID {
		b, _ := json.Marshal(evs)
		t.Fatalf("events on recipe.pipelines/a.yaml %s", b)
	}

	// The same report again changes nothing and emits nothing.
	before := h.session(s.ID).Rev
	report(map[string]any{"path": "pipelines/a.yaml", "status": "modified", "bytes": 12, "additions": 1, "deletions": 1},
		map[string]any{"path": "NOTES-new.md", "status": "added", "bytes": 5})
	if after := h.session(s.ID).Rev; after != before || len(eventsOf("pipelines/a.yaml")) != 1 {
		t.Fatalf("a repeated report moved the session (rev %d → %d) or emitted events", before, after)
	}

	// One file grows, the other is put back: an update and a clean event.
	report(map[string]any{"path": "NOTES-new.md", "status": "added", "bytes": 9})
	if evs := eventsOf("pipelines/a.yaml"); len(evs) != 2 || evs[1].Payload["status"] != "clean" {
		t.Fatalf("the reverted file's events %+v", evs)
	}
	if evs := eventsOf("NOTES-new.md"); len(evs) != 2 || evs[1].Payload["bytes"] != float64(9) {
		t.Fatalf("the grown file's events %+v", evs)
	}

	// Ending the session clears what is left.
	h.report(s.ID, map[string]any{"hostId": "host-a", "state": map[string]any{"state": "done"}})
	if w := read(); w != nil {
		t.Fatalf("working after the end %+v", w)
	}
	if evs := eventsOf("NOTES-new.md"); len(evs) != 3 || evs[2].Payload["status"] != "clean" {
		t.Fatalf("events after the end %+v", evs)
	}
	// Reports after the end are ignored.
	late := h.host("POST", "/api/host-sessions/"+s.ID+":report", map[string]any{"hostId": "host-a",
		"working": map[string]any{"files": []map[string]any{{"path": "late.txt", "status": "added"}}, "truncated": false}})
	_ = late.Body.Close()
	if w := read(); w != nil || len(eventsOf("late.txt")) != 0 {
		t.Fatalf("a report after the end was kept: %+v", w)
	}
}
