//go:build integration

package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/usunrise88/cadence/control-plane/internal/auth"
	"github.com/usunrise88/cadence/control-plane/internal/credentials"
	"github.com/usunrise88/cadence/control-plane/internal/events"
	"github.com/usunrise88/cadence/control-plane/internal/obs"
)

// Agent sessions against the real control plane: the session entity and its branch, the host protocol (claim,
// report, ask) with its own credential, the session token over git and the API, permission requests and gated
// commands as approvals, the transcript events, the end (token revoked, auto-merge) and accept/revert with a
// conflict.

type sessionView struct {
	ID             string `json:"id"`
	Number         int    `json:"number"`
	Project        string `json:"project"`
	Kind           string `json:"kind"`
	Driver         string `json:"driver"`
	Model          string `json:"model"`
	Preset         string `json:"preset"`
	State          string `json:"state"`
	Busy           bool   `json:"busy"`
	Branch         string `json:"branch"`
	PendingControl string `json:"pendingControl"`
	Rev            int    `json:"rev"`
	Merge          struct {
		State     string   `json:"state"`
		Commit    string   `json:"commit"`
		Conflicts []string `json:"conflicts"`
	} `json:"merge"`
	PauseReason *struct{ Code, Message string } `json:"pauseReason"`
	Budget      struct {
		Turns         int   `json:"turns"`
		Tokens        int64 `json:"tokens"`
		TokensPerTurn int64 `json:"tokensPerTurn"`
	} `json:"budget"`
	Use struct {
		Turns       int   `json:"turns"`
		InputTokens int64 `json:"inputTokens"`
	} `json:"use"`
}

type messageView struct {
	ID         string         `json:"id"`
	Seq        int64          `json:"seq"`
	Kind       string         `json:"kind"`
	Turn       int            `json:"turn"`
	Rev        int            `json:"rev"`
	Text       string         `json:"text"`
	Context    string         `json:"context"`
	Delivery   string         `json:"delivery"`
	Actor      auth.Actor     `json:"actor"`
	ToolCall   map[string]any `json:"toolCall"`
	Permission map[string]any `json:"permission"`
}

type hostWork struct {
	Start []struct {
		Session  sessionView `json:"session"`
		Token    string      `json:"token"`
		CloneURL string      `json:"cloneUrl"`
		MCPURL   string      `json:"mcpUrl"`
		Clocks   struct {
			StuckTurnSeconds int `json:"stuckTurnSeconds"`
			IdenticalCalls   int `json:"identicalCalls"`
		} `json:"clocks"`
		Resume *struct {
			ACPSessionID string `json:"acpSessionId"`
			Summary      string `json:"summary"`
		} `json:"resume"`
	} `json:"start"`
	Messages []struct {
		ID, SessionID, Kind, Text, Context string
	} `json:"messages"`
	Controls []struct {
		ID, SessionID, Action string
	} `json:"controls"`
	Decisions []struct {
		SessionID, ApprovalID, ToolCallID, Outcome string
	} `json:"decisions"`
}

// hostEnv is an authenticating server over the env's database with an agent host token.
type hostEnv struct {
	*env
	url       string // authenticates (no fixed actor)
	hostToken string
}

func startHost(t *testing.T) *hostEnv {
	e := start(t)
	authed := newTestServer(t, e.pool, events.NewHub(8), obs.NewMetrics(), func(c *Config) {
		c.Actor, c.Jobs, c.Projects, c.Secrets = auth.Actor{}, e.jobs, e.repos, e.admin.Secrets
	})
	srv := httptest.NewServer(authed.Handler())
	t.Cleanup(srv.Close)
	var tok string
	if err := pgx.BeginFunc(t.Context(), e.pool, func(tx pgx.Tx) error {
		var err error
		tok, _, err = credentials.NewHostToken(t.Context(), tx, "", true)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return &hostEnv{env: e, url: srv.URL, hostToken: tok}
}

// host sends a host-protocol request with the host token.
func (h *hostEnv) host(method, path string, body any) *http.Response {
	h.t.Helper()
	b, _ := json.Marshal(body)
	s := ""
	if body != nil {
		s = string(b)
	}
	return h.send(h.url, method, path, s, "Authorization", "Bearer "+h.hostToken)
}

func (h *hostEnv) claim(hostID string) hostWork {
	h.t.Helper()
	var w hostWork
	h.ok(h.host("POST", "/api/host-sessions:claim", map[string]any{"hostId": hostID, "wait": 0, "capacity": 4}), 200, &w)
	return w
}

func (h *hostEnv) report(id string, body map[string]any) sessionView {
	h.t.Helper()
	var s sessionView
	h.ok(h.host("POST", "/api/host-sessions/"+id+":report", body), 200, &s)
	return s
}

func (e *env) session(id string) sessionView {
	e.t.Helper()
	var s sessionView
	resp := e.ok(e.do("GET", "/api/agent-sessions/"+id, ""), 200, &s)
	if resp.Header.Get("ETag") != strconv.Quote(strconv.Itoa(s.Rev)) {
		e.t.Fatalf("session ETag %q, rev %d", resp.Header.Get("ETag"), s.Rev)
	}
	return s
}

func (e *env) transcript(id string) []messageView {
	e.t.Helper()
	var list struct{ Items []messageView }
	e.ok(e.do("GET", "/api/agent-sessions/"+id+"/agent-messages?limit=500", ""), 200, &list)
	return list.Items
}

func ifMatch(rev int) string { return strconv.Quote(strconv.Itoa(rev)) }

func TestAgentSessionLifecycle(t *testing.T) {
	h := startHost(t)
	p := h.newProject("hebrew")
	mix := h.newMix(heMix)

	// agentSessions.new: driver, model and preset from the agent profile; a branch off main; the prompt with its
	// references expanded is the first message, pending for the host.
	var s sessionView
	h.ok(h.do("POST", "/api/projects/hebrew/agent-sessions",
		`{"prompt":"Make the mix hotter","references":[{"ref":"@mix:`+mix.ID+`"},{"ref":"@run:123"}]}`,
		"Idempotency-Key", h.key()), 201, &s)
	if s.State != "created" || s.Kind != "interactive" || s.Driver != "claude-code" || s.Model != "sonnet" ||
		s.Preset != "guardrails-default" || s.Branch != "session/"+s.ID || s.Number != 1 || s.Budget.Turns != 200 ||
		s.Budget.TokensPerTurn != 1000000 {
		t.Fatalf("new session %+v", s)
	}
	if b, err := h.repos.Repos().Branch(t.Context(), "hebrew", s.Branch); err != nil || b.Ahead != 0 {
		t.Fatalf("session branch %+v %v", b, err)
	}
	msgs := h.transcript(s.ID)
	if len(msgs) != 1 || msgs[0].Kind != "user_message" || msgs[0].Delivery != "pending" ||
		!strings.Contains(msgs[0].Context, `mix "he-smoke", revision 1`) || !strings.Contains(msgs[0].Context, "@run:123") ||
		!strings.Contains(msgs[0].Context, "mixes.get id="+mix.ID) {
		t.Fatalf("first message %+v", msgs)
	}
	expectProblem(t, h.do("POST", "/api/projects/hebrew/agent-sessions", `{"references":[{"ref":"run 5"}]}`,
		"Idempotency-Key", h.key()), 422, "validation-failed")

	// Only the host credential speaks the host protocol.
	expectProblem(t, h.send(h.url, "POST", "/api/host-sessions:claim", `{"hostId":"x"}`), 401, "unauthenticated")

	// Claim: the host gets the session with a fresh session token, the clone path and the MCP endpoint.
	w := h.claim("host-a")
	if len(w.Start) != 1 || w.Start[0].Session.ID != s.ID || !strings.HasPrefix(w.Start[0].Token, "cst_") ||
		w.Start[0].CloneURL != "/git/hebrew.git" || w.Start[0].MCPURL != "/mcp" || w.Start[0].Clocks.IdenticalCalls != 3 ||
		w.Start[0].Clocks.StuckTurnSeconds != 300 || w.Start[0].Resume != nil {
		t.Fatalf("claim %+v", w)
	}
	if len(w.Messages) != 0 {
		t.Fatalf("messages before the host reported running: %+v", w.Messages)
	}
	token := w.Start[0].Token
	expectProblem(t, h.send(h.url, "POST", "/api/host-sessions:claim", `{"hostId":"x"}`, "Authorization", "Bearer "+token), 403, "forbidden")
	if again := h.claim("host-b"); len(again.Start) != 0 {
		t.Fatalf("a live host's session was offered again: %+v", again.Start)
	}
	var raw map[string]json.RawMessage
	h.ok(h.host("POST", "/api/host-sessions:claim", map[string]any{"hostId": "host-b", "wait": 0, "capacity": 4}), 200, &raw)
	for _, k := range []string{"start", "messages", "controls", "decisions"} {
		if string(raw[k]) != "[]" {
			t.Errorf("an empty claim's %s is %s, want []", k, raw[k])
		}
	}
	var preset string
	if err := h.pool.QueryRow(t.Context(), `SELECT scope->>'preset' FROM credentials WHERE subject = $1 AND revoked_at IS NULL`, s.ID).Scan(&preset); err != nil || preset != "guardrails-default" {
		t.Fatalf("session token preset %q %v", preset, err)
	}

	// The token clones over git HTTP and pushes its own branch.
	dir := t.TempDir()
	remote := strings.Replace(h.url, "http://", "http://x-token:"+token+"@", 1) + "/git/hebrew.git"
	if out, err := gitCmd(t, dir, "clone", "-q", "-b", s.Branch, remote, "wt"); err != nil {
		t.Fatalf("clone the session branch: %v %s", err, out)
	}
	wt := filepath.Join(dir, "wt")
	if err := os.WriteFile(filepath.Join(wt, "pipelines", "agent.yaml"), []byte("name: agent\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", "."}, {"commit", "-q", "-m", "cadence: turn 1"}, {"push", "-q", "origin", s.Branch}} {
		if out, err := gitCmd(t, wt, args...); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}

	// Running: the first message goes to the host, once.
	busy := true
	one := 1
	s = h.report(s.ID, map[string]any{"hostId": "host-a", "acpSessionId": "acp-1", "state": map[string]any{"state": "running"}})
	if s.State != "running" {
		t.Fatalf("after running: %+v", s)
	}
	w = h.claim("host-a")
	if len(w.Messages) != 1 || w.Messages[0].Text != "Make the mix hotter" || !strings.Contains(w.Messages[0].Context, "<cadence-context>") {
		t.Fatalf("messages %+v", w.Messages)
	}
	if again := h.claim("host-a"); len(again.Messages) != 0 {
		t.Fatalf("a message was delivered twice")
	}

	// A turn's entries: coalesced text, a tool call updated in place, the turn with its usage; a retried report
	// changes nothing.
	report := map[string]any{"hostId": "host-a", "state": map[string]any{"state": "running", "busy": busy, "turn": one},
		"entries": []map[string]any{
			{"key": "t1:m1", "kind": "agent_message", "turn": 1, "text": "Looking at the mix", "final": false},
			{"key": "tool:toolu_1", "kind": "tool_call", "turn": 1, "toolCall": map[string]any{"id": "toolu_1",
				"title": "cadence.mixes.get", "class": "mcp", "status": "in_progress", "operation": "mixes.get", "server": "cadence"}},
		}}
	h.report(s.ID, report)
	h.report(s.ID, report)
	h.report(s.ID, map[string]any{"hostId": "host-a",
		"state": map[string]any{"state": "running", "busy": false, "turn": 1},
		"use":   map[string]any{"turns": 1, "inputTokens": 1200, "outputTokens": 300},
		"entries": []map[string]any{
			{"key": "t1:m1", "kind": "agent_message", "turn": 1, "text": "Looking at the mix. Done.", "final": true},
			{"key": "tool:toolu_1", "kind": "tool_call", "turn": 1, "toolCall": map[string]any{"id": "toolu_1",
				"title": "cadence.mixes.get", "class": "mcp", "status": "completed", "operation": "mixes.get", "server": "cadence"}},
			{"key": "t1:end", "kind": "turn", "turn": 1, "turnInfo": map[string]any{"state": "ended", "stopReason": "end_turn",
				"inputTokens": 1200, "outputTokens": 300}},
			{"key": "t1:commit", "kind": "commit", "turn": 1, "commit": map[string]any{"sha": "abc", "files": []string{"pipelines/agent.yaml"}}},
		}})
	msgs = h.transcript(s.ID)
	kinds := []string{}
	for _, m := range msgs {
		kinds = append(kinds, m.Kind)
	}
	if strings.Join(kinds, ",") != "user_message,notice,agent_message,tool_call,turn,commit" {
		t.Fatalf("transcript kinds %v", kinds)
	}
	if msgs[2].Text != "Looking at the mix. Done." || msgs[2].Rev != 2 || msgs[2].Actor.Kind != "agent" || msgs[2].Actor.SessionID != s.ID ||
		msgs[3].ToolCall["status"] != "completed" || msgs[3].Rev != 2 {
		t.Fatalf("coalesced entries %+v", msgs[2:4])
	}
	if s = h.session(s.ID); s.Use.Turns != 1 || s.Use.InputTokens != 1200 || s.Busy {
		t.Fatalf("use %+v", s)
	}
	var n int
	if err := h.pool.QueryRow(t.Context(), `SELECT count(*) FROM events WHERE topic = $1 AND type LIKE 'agent_message.%'`,
		"agent.session."+s.ID).Scan(&n); err != nil || n < 6 {
		t.Fatalf("transcript events on agent.session.%s: %d %v", s.ID, n, err)
	}

	// selection://current is the references of the latest message (the MCP resource reads it with the agent actor).
	sel, err := h.admin.Selection.Current(t.Context(), auth.Actor{Kind: "agent", ID: "crd_x", SessionID: s.ID})
	if err != nil || len(sel.References) != 2 || sel.References[0].Kind != "mix" || sel.References[1].ID != "123" {
		t.Fatalf("selection %+v %v", sel, err)
	}

	// Permission requests: the preset answers what it decides; the rest is an agent-permission approval.
	ask := func(tc map[string]any) (d struct{ ApprovalID, Outcome, Rule string }) {
		h.ok(h.host("POST", "/api/host-sessions/"+s.ID+":ask", map[string]any{"hostId": "host-a", "turn": 2, "toolCall": tc,
			"options": []map[string]any{{"optionId": "a1", "name": "Allow", "kind": "allow_once"},
				{"optionId": "a2", "name": "Always", "kind": "allow_always"}, {"optionId": "r1", "name": "Reject", "kind": "reject_once"}}}), 200, &d)
		return d
	}
	if d := ask(map[string]any{"id": "t2", "title": "git status", "class": "shell", "status": "pending", "shell": map[string]any{"command": "git status"}}); d.Outcome != "allow_once" || d.Rule != "shell.allow" {
		t.Errorf("git status: %+v", d)
	}
	if d := ask(map[string]any{"id": "t3", "title": "curl", "class": "shell", "status": "pending", "shell": map[string]any{"command": "curl https://x.org"}}); d.Outcome != "reject_once" {
		t.Errorf("curl: %+v", d)
	}
	if d := ask(map[string]any{"id": "t4", "title": "Edit .env", "class": "edit", "status": "pending", "locations": []string{".env"}}); d.Outcome != "reject_once" || d.Rule != "files.deny" {
		t.Errorf(".env edit: %+v", d)
	}
	d := ask(map[string]any{"id": "t5", "title": "pip install torch", "class": "shell", "status": "pending", "shell": map[string]any{"command": "pip install torch"}})
	if d.Outcome != "pending" || !strings.HasPrefix(d.ApprovalID, "apr_") {
		t.Fatalf("pip install: %+v", d)
	}
	if s = h.session(s.ID); s.State != "waiting_approval" {
		t.Fatalf("while a permission waits: %s", s.State)
	}
	a := h.approval(d.ApprovalID)
	var perm struct {
		Kind       string         `json:"kind"`
		Permission map[string]any `json:"permission"`
		Operation  string         `json:"operation"`
	}
	h.ok(h.do("GET", "/api/approvals/"+d.ApprovalID, ""), 200, &perm)
	if perm.Kind != "agent_permission" || perm.Operation != "agent.shell" || perm.Permission["command"] != "pip install torch" {
		t.Fatalf("agent permission approval %+v", perm)
	}
	// An agent never decides it; a person approves for the session — no replay, the host reads allow_always.
	h.ok(h.do("POST", "/api/approvals/"+d.ApprovalID+":approve", `{"grant":"session"}`, "Idempotency-Key", h.key(),
		"If-Match", ifMatch(a.Rev)), 200, nil)
	w = h.claim("host-a")
	if len(w.Decisions) != 1 || w.Decisions[0].Outcome != "allow_always" || w.Decisions[0].ToolCallID != "t5" {
		t.Fatalf("decisions %+v", w.Decisions)
	}
	var dec struct{ Outcome string }
	h.ok(h.host("GET", "/api/host-sessions/"+s.ID+":decision?approvalId="+d.ApprovalID+"&hostId=host-a", nil), 200, &dec)
	if dec.Outcome != "allow_always" {
		t.Errorf("decision read %+v", dec)
	}
	if s = h.session(s.ID); s.State != "running" {
		t.Fatalf("after the decision: %s", s.State)
	}
	perms := 0
	for _, m := range h.transcript(s.ID) {
		if m.Kind == "permission" {
			perms++
			if m.Permission["approvalId"] == d.ApprovalID && (m.Permission["state"] != "approved" || m.Permission["grant"] != "session") {
				t.Errorf("decided permission entry %+v", m.Permission)
			}
		}
	}
	if perms != 4 {
		t.Errorf("%d permission entries, want 4", perms)
	}

	// A gated command the agent sends with its session token becomes an approval in the same transcript; the
	// decision reaches the agent as a notice.
	var pr project
	h.ok(h.do("GET", "/api/projects/hebrew", ""), 200, &pr)
	var acc accepted
	h.ok(h.send(h.url, "POST", "/api/projects/hebrew:archive", "", "Authorization", "Bearer "+token, "Idempotency-Key", h.key(),
		"If-Match", ifMatch(pr.Rev), "Cadence-Tool-Call-Id", "toolu_9"), 202, &acc)
	if s = h.session(s.ID); s.State != "waiting_approval" {
		t.Fatalf("while a gated command waits: %s", s.State)
	}
	a = h.approval(acc.ApprovalID)
	h.ok(h.do("POST", "/api/approvals/"+acc.ApprovalID+":deny", `{"note":"keep the project"}`, "Idempotency-Key", h.key(),
		"If-Match", ifMatch(a.Rev)), 200, nil)
	w = h.claim("host-a")
	if len(w.Messages) != 1 || w.Messages[0].Kind != "notice" || !strings.Contains(w.Messages[0].Text, "projects.archive was denied") {
		t.Fatalf("the decision for the agent %+v", w.Messages)
	}
	found := false
	for _, m := range h.transcript(s.ID) {
		if m.Kind == "permission" && m.Permission["source"] == "command" {
			found = m.Permission["toolCallId"] == "toolu_9" && m.Permission["state"] == "denied" && m.Permission["operation"] == "projects.archive"
		}
	}
	if !found {
		t.Error("no denied gated-command entry with the tool call id")
	}
	// Agents cannot start sessions, message them or accept them.
	expectProblem(t, h.send(h.url, "POST", "/api/projects/hebrew/agent-sessions", `{}`, "Authorization", "Bearer "+token,
		"Idempotency-Key", h.key()), 403, "policy-denied")
	expectProblem(t, h.send(h.url, "POST", "/api/agent-sessions/"+s.ID+"/agent-messages", `{"text":"hi"}`, "Authorization", "Bearer "+token,
		"Idempotency-Key", h.key()), 403, "policy-denied")

	// A message while running is queued for the host.
	var um messageView
	h.ok(h.do("POST", "/api/agent-sessions/"+s.ID+"/agent-messages", `{"text":"And add a note","references":[{"ref":"@job:job_nope"}]}`,
		"Idempotency-Key", h.key()), 201, &um)
	if um.Delivery != "pending" || !strings.Contains(um.Context, "not found in this project") {
		t.Fatalf("message %+v", um)
	}

	// Pause and resume go through the host.
	s = h.session(s.ID)
	h.ok(h.do("POST", "/api/agent-sessions/"+s.ID+":pause", "", "Idempotency-Key", h.key(), "If-Match", ifMatch(s.Rev)), 200, &s)
	if s.PendingControl != "pause" {
		t.Fatalf("pause pending %+v", s)
	}
	w = h.claim("host-a")
	if len(w.Controls) != 1 || w.Controls[0].Action != "pause" || len(w.Messages) != 1 {
		t.Fatalf("pause control %+v", w)
	}
	s = h.report(s.ID, map[string]any{"hostId": "host-a", "note": "paused by the person",
		"state": map[string]any{"state": "paused", "reason": map[string]any{"code": "user", "message": "paused by admin"}}})
	if s.State != "paused" || s.PauseReason == nil || s.PauseReason.Code != "user" {
		t.Fatalf("paused %+v", s)
	}
	if !strings.Contains(h.recipe("hebrew", "NOTES.md", "").Content, "paused by the person") {
		t.Error("the pause note is not in NOTES.md")
	}
	h.ok(h.do("POST", "/api/agent-sessions/"+s.ID+":resume", "", "Idempotency-Key", h.key(), "If-Match", ifMatch(s.Rev)), 200, &s)
	w = h.claim("host-a")
	if len(w.Controls) != 1 || w.Controls[0].Action != "resume" {
		t.Fatalf("resume control %+v", w.Controls)
	}
	h.report(s.ID, map[string]any{"hostId": "host-a", "state": map[string]any{"state": "running"}})

	// End: through the host (it commits the last turn), then the token dies and the branch merges (when-clean).
	s = h.session(s.ID)
	h.ok(h.do("POST", "/api/agent-sessions/"+s.ID+":cancel", `{"end":true}`, "Idempotency-Key", h.key(), "If-Match", ifMatch(s.Rev)), 200, &s)
	w = h.claim("host-a")
	if len(w.Controls) != 1 || w.Controls[0].Action != "end" {
		t.Fatalf("end control %+v", w.Controls)
	}
	s = h.report(s.ID, map[string]any{"hostId": "host-a", "state": map[string]any{"state": "done"}})
	if s.State != "done" || s.Merge.State != "merged" || s.Merge.Commit == "" {
		t.Fatalf("ended %+v", s)
	}
	if h.recipe("hebrew", "pipelines/agent.yaml", "").Content != "name: agent\n" {
		t.Error("main lacks the session's file after the auto-merge")
	}
	expectProblem(t, h.send(h.url, "GET", "/api/projects/hebrew", "", "Authorization", "Bearer "+token), 401, "unauthenticated")
	if out, err := gitCmd(t, dir, "clone", "-q", remote, "late"); err == nil {
		t.Fatalf("the revoked token still clones: %s", out)
	}
	expectProblem(t, h.do("POST", "/api/agent-sessions/"+s.ID+"/agent-messages", `{"text":"hi"}`, "Idempotency-Key", h.key()), 409, "conflict")
	_ = p
}

func TestAgentSessionMergeConflictAcceptRevert(t *testing.T) {
	h := startHost(t)
	h.newProject("demo")
	// Auto-merge never: the session's changes wait for a person.
	var prof agentProfile
	h.ok(h.do("GET", "/api/projects/demo/agent-profile", ""), 200, &prof)
	h.ok(h.do("PATCH", "/api/projects/demo/agent-profile", `{"autoMerge":"never"}`, "Idempotency-Key", h.key(),
		"If-Match", ifMatch(prof.Rev)), 200, nil)

	newSession := func() (sessionView, string) {
		var s sessionView
		h.ok(h.do("POST", "/api/projects/demo/agent-sessions", `{"prompt":"go"}`, "Idempotency-Key", h.key()), 201, &s)
		w := h.claim("host-a")
		if len(w.Start) != 1 {
			t.Fatalf("claim %+v", w)
		}
		h.report(s.ID, map[string]any{"hostId": "host-a", "state": map[string]any{"state": "running"}})
		return s, w.Start[0].Token
	}
	push := func(s sessionView, token, file, content string) {
		dir := t.TempDir()
		remote := strings.Replace(h.url, "http://", "http://x-token:"+token+"@", 1) + "/git/demo.git"
		if out, err := gitCmd(t, dir, "clone", "-q", "-b", s.Branch, remote, "wt"); err != nil {
			t.Fatalf("clone: %v %s", err, out)
		}
		wt := filepath.Join(dir, "wt")
		if err := os.WriteFile(filepath.Join(wt, file), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		for _, args := range [][]string{{"add", "."}, {"commit", "-q", "-m", "turn"}, {"push", "-q", "origin", s.Branch}} {
			if out, err := gitCmd(t, wt, args...); err != nil {
				t.Fatalf("git %v: %v %s", args, err, out)
			}
		}
	}

	// Two sessions change the same file; the first is accepted, the second conflicts.
	s1, tok1 := newSession()
	s2, tok2 := newSession()
	push(s1, tok1, "AGENTS.md", "one\n")
	push(s2, tok2, "AGENTS.md", "two\n")
	for _, s := range []sessionView{s1, s2} {
		got := h.report(s.ID, map[string]any{"hostId": "host-a", "state": map[string]any{"state": "done"}})
		if got.Merge.State != "pending" {
			t.Fatalf("auto-merge never: %+v", got.Merge)
		}
	}
	s1 = h.session(s1.ID)
	// A dry run changes nothing.
	h.ok(h.do("POST", "/api/agent-sessions/"+s1.ID+":accept?dryRun=true", "", "Idempotency-Key", h.key(), "If-Match", ifMatch(s1.Rev)), 200, nil)
	if h.recipe("demo", "AGENTS.md", "").Content == "one\n" {
		t.Fatal("a dry-run accept merged")
	}
	h.ok(h.do("POST", "/api/agent-sessions/"+s1.ID+":accept", "", "Idempotency-Key", h.key(), "If-Match", ifMatch(s1.Rev)), 200, &s1)
	if s1.Merge.State != "merged" || h.recipe("demo", "AGENTS.md", "").Content != "one\n" {
		t.Fatalf("accepted %+v", s1.Merge)
	}
	s2 = h.session(s2.ID)
	p := expectProblem(t, h.do("POST", "/api/agent-sessions/"+s2.ID+":accept", "", "Idempotency-Key", h.key(), "If-Match", ifMatch(s2.Rev)), 409, "merge-conflict")
	if !strings.Contains(p.Detail, "AGENTS.md") {
		t.Errorf("conflict detail %q", p.Detail)
	}
	expectProblem(t, h.do("POST", "/api/agent-sessions/"+s2.ID+":accept", "", "Idempotency-Key", h.key(), "If-Match", ifMatch(s2.Rev-1)), 412, "precondition-failed")
	h.ok(h.do("POST", "/api/agent-sessions/"+s2.ID+":revert", "", "Idempotency-Key", h.key(), "If-Match", ifMatch(s2.Rev)), 200, &s2)
	if s2.Merge.State != "discarded" {
		t.Fatalf("reverted %+v", s2.Merge)
	}
	if _, err := h.repos.Repos().Branch(t.Context(), "demo", s2.Branch); err == nil {
		t.Error("the reverted session's branch still exists")
	}
	expectProblem(t, h.do("POST", "/api/agent-sessions/"+s2.ID+":revert", "", "Idempotency-Key", h.key(), "If-Match", ifMatch(s2.Rev)), 409, "conflict")

	// A running session is paused or ended before its branch is decided.
	s3, _ := newSession()
	s3 = h.session(s3.ID)
	expectProblem(t, h.do("POST", "/api/agent-sessions/"+s3.ID+":accept", "", "Idempotency-Key", h.key(), "If-Match", ifMatch(s3.Rev)), 409, "conflict")

	// Its host goes silent: another host resumes it with the ACP session and a summary.
	h.report(s3.ID, map[string]any{"hostId": "host-a", "acpSessionId": "acp-3"})
	if _, err := h.pool.Exec(context.Background(), `UPDATE agent_hosts SET seen_at = now() - interval '10 minutes' WHERE id = 'host-a'`); err != nil {
		t.Fatal(err)
	}
	w := h.claim("host-b")
	if len(w.Start) != 1 || w.Start[0].Session.ID != s3.ID || w.Start[0].Resume == nil || w.Start[0].Resume.ACPSessionID != "acp-3" ||
		!strings.Contains(w.Start[0].Resume.Summary, "User: go") {
		t.Fatalf("resume on another host %+v", w.Start)
	}
	expectProblem(t, h.host("POST", "/api/host-sessions/"+s3.ID+":report", map[string]any{"hostId": "host-a"}), 409, "conflict")

	// Read-only sessions: the read-only preset, no branch, one message.
	var ro sessionView
	h.ok(h.do("POST", "/api/projects/demo/agent-sessions", `{"kind":"read-only","prompt":"Explain the mix"}`, "Idempotency-Key", h.key()), 201, &ro)
	if ro.Preset != "read-only" || ro.Branch != "" || ro.Budget.Turns != 1 {
		t.Fatalf("read-only session %+v", ro)
	}
	expectProblem(t, h.do("POST", "/api/agent-sessions/"+ro.ID+"/agent-messages", `{"text":"more"}`, "Idempotency-Key", h.key()), 409, "conflict")
	expectProblem(t, h.do("POST", "/api/projects/demo/agent-sessions", `{"kind":"read-only"}`, "Idempotency-Key", h.key()), 422, "validation-failed")

	// A session nobody runs yet ends at once when cancelled with end.
	ro = h.session(ro.ID)
	h.ok(h.do("POST", "/api/agent-sessions/"+ro.ID+":cancel", `{"end":true}`, "Idempotency-Key", h.key(), "If-Match", ifMatch(ro.Rev)), 200, &ro)
	if ro.State != "cancelled" {
		t.Fatalf("cancelled before start: %+v", ro)
	}
	var list struct{ Items []sessionView }
	h.ok(h.do("GET", "/api/projects/demo/agent-sessions?state=live", ""), 200, &list)
	if len(list.Items) != 1 || list.Items[0].ID != s3.ID {
		t.Errorf("live sessions %+v", list.Items)
	}
	_ = fmt.Sprint(time.Now())
}

// TestAgentSessionIdlePause runs the idle clock: a running session without a message pauses through its host.
func TestAgentSessionIdlePause(t *testing.T) {
	h := startHost(t)
	h.newProject("demo")
	var s sessionView
	h.ok(h.do("POST", "/api/projects/demo/agent-sessions", `{}`, "Idempotency-Key", h.key()), 201, &s)
	h.claim("host-a")
	h.report(s.ID, map[string]any{"hostId": "host-a", "state": map[string]any{"state": "running"}})
	if _, err := h.pool.Exec(context.Background(), `UPDATE agent_sessions SET started_at = now() - interval '2 hours', created_at = now() - interval '2 hours' WHERE id = $1`, s.ID); err != nil {
		t.Fatal(err)
	}
	if err := h.admin.SweepSessions(t.Context()); err != nil {
		t.Fatal(err)
	}
	w := h.claim("host-a")
	if len(w.Controls) != 1 || w.Controls[0].Action != "pause" {
		t.Fatalf("idle pause %+v", w.Controls)
	}
}
