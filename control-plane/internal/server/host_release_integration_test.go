//go:build integration

package server

import (
	"strings"
	"testing"
	"time"

	"github.com/usunrise88/cadence/control-plane/internal/sessions"
)

// An agent host that shuts down releases its sessions (hostSessions.release): the next host takes them at once, the
// interrupted turn and its withdrawn permission request reach the agent as a note (not "declined"), messages the old
// host never gave its agent are delivered again, and a Stop pressed meanwhile is dropped. A host that goes silent is
// shown as lost by the sweep until it answers again.

type hostSession struct {
	sessionView
	HostState  string     `json:"hostState"`
	HostLeftAt *time.Time `json:"hostLeftAt"`
}

func (e *env) hostSession(id string) hostSession {
	e.t.Helper()
	var s hostSession
	e.ok(e.do("GET", "/api/agent-sessions/"+id, ""), 200, &s)
	return s
}

func TestAgentHostReleaseAndTakeover(t *testing.T) {
	h := startHost(t)
	h.newProject("hebrew")

	var s sessionView
	h.ok(h.do("POST", "/api/projects/hebrew/agent-sessions", `{"prompt":"Install torch"}`, "Idempotency-Key", h.key()), 201, &s)
	if v := h.hostSession(s.ID); v.HostState != "waiting" {
		t.Fatalf("a new session's host state %q", v.HostState)
	}
	if w := h.claim("host-a"); len(w.Start) != 1 {
		t.Fatalf("claim %+v", w)
	}
	h.report(s.ID, map[string]any{"hostId": "host-a", "acpSessionId": "acp-1", "state": map[string]any{"state": "running"}})
	if w := h.claim("host-a"); len(w.Messages) != 1 {
		t.Fatalf("first message %+v", w.Messages)
	}
	h.report(s.ID, map[string]any{"hostId": "host-a", "state": map[string]any{"state": "running", "busy": true, "turn": 1}})
	if v := h.hostSession(s.ID); v.HostState != "connected" || v.HostLeftAt != nil {
		t.Fatalf("running session's host %+v", v)
	}

	// The turn waits for a person on a permission request; a second message is taken by the host but not yet given
	// to its agent.
	var d struct{ ApprovalID, Outcome string }
	h.ok(h.host("POST", "/api/host-sessions/"+s.ID+":ask", map[string]any{"hostId": "host-a", "turn": 1,
		"toolCall": map[string]any{"id": "t1", "title": "pip install torch", "class": "shell", "status": "pending",
			"shell": map[string]any{"command": "pip install torch"}},
		"options": []map[string]any{{"optionId": "a", "name": "Allow", "kind": "allow_once"}, {"optionId": "r", "name": "Reject", "kind": "reject_once"}}}), 200, &d)
	if d.Outcome != "pending" {
		t.Fatalf("ask %+v", d)
	}
	var m2 messageView
	h.ok(h.do("POST", "/api/agent-sessions/"+s.ID+"/agent-messages", `{"text":"And add a note"}`, "Idempotency-Key", h.key()), 201, &m2)
	if w := h.claim("host-a"); len(w.Messages) != 1 || w.Messages[0].ID != m2.ID {
		t.Fatalf("second message %+v", w.Messages)
	}

	// The host shuts down and releases its sessions.
	var rel struct{ Released []string }
	h.ok(h.host("POST", "/api/host-sessions:release", map[string]any{"hostId": "host-a", "messages": []string{m2.ID}}), 200, &rel)
	if len(rel.Released) != 1 || rel.Released[0] != s.ID {
		t.Fatalf("released %+v", rel)
	}
	v := h.hostSession(s.ID)
	if v.HostState != "released" || v.HostLeftAt == nil || v.State != "running" || v.Busy {
		t.Fatalf("released session %+v", v)
	}
	var apr struct {
		State    string
		Decision struct{ Note string }
	}
	h.ok(h.do("GET", "/api/approvals/"+d.ApprovalID, ""), 200, &apr)
	if apr.State != "denied" || apr.Decision.Note != sessions.InterruptedNote {
		t.Fatalf("the withdrawn permission request %+v", apr)
	}
	var restart messageView
	for _, m := range h.transcript(s.ID) {
		if m.Kind == "notice" && strings.Contains(m.Text, "restarted during turn 1") {
			restart = m
		}
		if m.ID == m2.ID && m.Delivery != "pending" {
			t.Errorf("the message the host never gave its agent is %s", m.Delivery)
		}
	}
	if !strings.Contains(restart.Text, "“pip install torch” was withdrawn by the restart — it was not declined") {
		t.Fatalf("no restart notice in the transcript: %+v", restart)
	}
	// The old host no longer runs it.
	expectProblem(t, h.host("POST", "/api/host-sessions/"+s.ID+":report", map[string]any{"hostId": "host-a", "entries": []any{}}), 409, "conflict")

	// A Stop pressed while no host runs the session is queued, then dropped by the next host's claim.
	h.ok(h.do("POST", "/api/agent-sessions/"+s.ID+":cancel", "", "Idempotency-Key", h.key(), "If-Match", ifMatch(v.Rev)), 200, nil)
	if v = h.hostSession(s.ID); v.PendingControl != "cancel" {
		t.Fatalf("stop while released %+v", v)
	}

	// The next host takes it at once (no lapse) with the note for the agent.
	var w struct {
		Start []struct {
			Session sessionView `json:"session"`
			Resume  *struct {
				Note string `json:"note"`
			} `json:"resume"`
		} `json:"start"`
		Messages []struct{ ID string }     `json:"messages"`
		Controls []struct{ Action string } `json:"controls"`
	}
	h.ok(h.host("POST", "/api/host-sessions:claim", map[string]any{"hostId": "host-b", "wait": 0, "capacity": 4}), 200, &w)
	if len(w.Start) != 1 || w.Start[0].Session.ID != s.ID || w.Start[0].Resume == nil {
		t.Fatalf("takeover %+v", w.Start)
	}
	note := w.Start[0].Resume.Note
	for _, want := range []string{"turn 1 was running", "the person did not stop it", "“pip install torch” was withdrawn by the restart, not declined"} {
		if !strings.Contains(note, want) {
			t.Errorf("resume note lacks %q: %s", want, note)
		}
	}
	if len(w.Controls) != 0 {
		t.Errorf("a stale stop reached the new host: %+v", w.Controls)
	}
	if len(w.Messages) != 1 || w.Messages[0].ID != m2.ID {
		t.Errorf("the given-back message %+v", w.Messages)
	}
	v = h.hostSession(s.ID)
	if v.HostState != "connected" || v.HostLeftAt != nil || v.PendingControl != "" {
		t.Fatalf("after the takeover %+v", v)
	}
	dropped := false
	for _, m := range h.transcript(s.ID) {
		dropped = dropped || (m.Kind == "notice" && strings.HasPrefix(m.Text, "Stop was dropped"))
	}
	if !dropped {
		t.Error("no notice that the stale stop was dropped")
	}
	// The note is told once.
	var again string
	if err := h.pool.QueryRow(t.Context(), `SELECT coalesce(resume_note, '') FROM agent_sessions WHERE id = $1`, s.ID).Scan(&again); err != nil || again != "" {
		t.Errorf("resume note kept %q %v", again, err)
	}
	h.report(s.ID, map[string]any{"hostId": "host-b", "state": map[string]any{"state": "running"}})

	// host-b goes silent: the sweep marks the session lost; host-b's next claim clears it.
	if _, err := h.pool.Exec(t.Context(), `UPDATE agent_hosts SET seen_at = now() - interval '1 hour' WHERE id = 'host-b'`); err != nil {
		t.Fatal(err)
	}
	if err := h.admin.SweepSessions(t.Context()); err != nil {
		t.Fatal(err)
	}
	if v = h.hostSession(s.ID); v.HostState != "lost" || v.HostLeftAt == nil {
		t.Fatalf("after the sweep %+v", v)
	}
	if err := h.admin.SweepSessions(t.Context()); err != nil {
		t.Fatal(err)
	}
	lostNotices := 0
	for _, m := range h.transcript(s.ID) {
		if m.Kind == "notice" && strings.Contains(m.Text, "stopped answering") {
			lostNotices++
		}
	}
	if lostNotices != 1 {
		t.Errorf("%d lost notices, want 1", lostNotices)
	}
	h.claim("host-b")
	if v = h.hostSession(s.ID); v.HostState != "connected" || v.HostLeftAt != nil {
		t.Fatalf("after host-b answered again %+v", v)
	}
}
