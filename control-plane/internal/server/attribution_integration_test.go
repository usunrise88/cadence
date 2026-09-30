//go:build integration

package server

import (
	"strings"
	"testing"
)

// opencode-style attribution: a command caused by an MCP call without a tool-use id carries the MCP server's
// synthetic id until the agent host reports the agent's own tool call; then the draft, the revision's cause, the
// audit log and the outbox events name the tool call, and open panels hear draft.updated.

func TestOpencodeToolCallAttribution(t *testing.T) {
	h := startHost(t)
	h.newProject("hebrew")
	mix := h.newMix(heMix)

	var s sessionView
	h.ok(h.do("POST", "/api/projects/hebrew/agent-sessions", `{"prompt":"Warmer"}`, "Idempotency-Key", h.key()), 201, &s)
	w := h.claim("host-a")
	if len(w.Start) != 1 {
		t.Fatalf("claim %+v", w)
	}
	token := w.Start[0].Token
	h.report(s.ID, map[string]any{"hostId": "host-a", "state": map[string]any{"state": "running", "busy": true, "turn": 1}})

	// Two edits through MCP without a tool-use id: the MCP server stands in with its session and JSON-RPC ids.
	edit := func(body, synthetic string) {
		t.Helper()
		var er editResult
		h.ok(h.send(h.url, "PATCH", "/api/mixes/"+mix.ID, body, "Authorization", "Bearer "+token,
			"Idempotency-Key", h.key(), "If-Match", `"1"`, "Cadence-Tool-Call-Id", synthetic), 200, reset(&er))
		if er.Draft == nil || er.Draft.ToolCallID != synthetic {
			t.Fatalf("draft of %s: %+v", synthetic, er.Draft)
		}
	}
	edit(`{"temperature":2}`, "mcp:oc-1/7")
	edit(`{"temperature":2.5}`, "mcp:oc-1/8")
	since := len(h.events("entity.mix." + mix.ID))

	// The host reports opencode's first tool call: the first edit takes its id.
	call := func(id, status string) map[string]any {
		return map[string]any{"key": "tool:" + id, "kind": "tool_call", "turn": 1, "toolCall": map[string]any{"id": id,
			"title": "cadence.mixes_edit", "class": "mcp", "status": status, "operation": "mixes.edit", "server": "cadence"}}
	}
	h.report(s.ID, map[string]any{"hostId": "host-a", "entries": []any{call("call_oc1", "in_progress")}})
	if got := h.drafts(mix.ID); got[0].ToolCallID != "mcp:oc-1/8" {
		t.Fatalf("attributed before the call completed: %+v", got)
	}
	h.report(s.ID, map[string]any{"hostId": "host-a", "entries": []any{call("call_oc1", "completed")}})
	var ids []string
	if err := pgxCollect(h, `SELECT tool_call_id FROM audit_log WHERE actor->>'sessionId' = $1 AND operation = 'mixes.edit' ORDER BY id`, s.ID, &ids); err != nil {
		t.Fatal(err)
	}
	if strings.Join(ids, ",") != "call_oc1,mcp:oc-1/8" {
		t.Fatalf("audit tool call ids %v", ids)
	}
	// The same call reported again (its output changed) does not take the second edit's id.
	again := call("call_oc1", "completed")
	again["toolCall"].(map[string]any)["output"] = map[string]any{"status": 200}
	h.report(s.ID, map[string]any{"hostId": "host-a", "entries": []any{again}})
	if got := h.drafts(mix.ID); got[0].ToolCallID != "mcp:oc-1/8" {
		t.Fatalf("the second edit's id was taken by the first call: %+v", got)
	}

	// The second call: the draft (written by it) now names it, and open panels hear draft.updated.
	h.report(s.ID, map[string]any{"hostId": "host-a", "entries": []any{call("call_oc2", "completed")}})
	got := h.drafts(mix.ID)
	if len(got) != 1 || got[0].ToolCallID != "call_oc2" || got[0].Rev != 2 {
		t.Fatalf("draft after attribution %+v", got)
	}
	if p := h.mix(mix.ID).Presence; len(p) != 1 || p[0].ToolCallID != "call_oc2" {
		t.Fatalf("presence %+v", p)
	}
	evs := h.events("entity.mix." + mix.ID)
	causes := map[string]bool{}
	for _, e := range evs[:since] {
		if e.CausedBy != nil && e.CausedBy.ToolCallID != "" {
			causes[e.CausedBy.ToolCallID] = true
		}
		if strings.Contains(string(e.Payload), "mcp:oc-1/") {
			t.Errorf("event %d still names the synthetic id: %s", e.Seq, e.Payload)
		}
	}
	if !causes["call_oc1"] || !causes["call_oc2"] || len(causes) != 2 {
		t.Errorf("event causes %v", causes)
	}
	var updated bool
	for _, e := range evs[since:] {
		if e.Type == "draft.updated" && strings.Contains(string(e.Payload), `"toolCallId":"call_oc2"`) {
			updated = true
		}
	}
	if !updated {
		t.Error("no draft.updated with the agent's tool-call id")
	}

	// Accepted, the revision's cause keeps the agent's id; a Claude-style call (its own tool-use id) is left alone.
	h.ok(h.do("POST", "/api/drafts/"+got[0].ID+":accept", "", "Idempotency-Key", h.key(), "If-Match", `"2"`), 200, nil)
	if m := h.mix(mix.ID); m.Cause == nil || m.Cause.ToolCallID != "call_oc2" {
		t.Fatalf("accepted revision cause %+v", m.Cause)
	}
	var er editResult
	h.ok(h.send(h.url, "PATCH", "/api/mixes/"+mix.ID, `{"temperature":3}`, "Authorization", "Bearer "+token,
		"Idempotency-Key", h.key(), "If-Match", `"2"`, "Cadence-Tool-Call-Id", "toolu_x"), 200, &er)
	h.report(s.ID, map[string]any{"hostId": "host-a", "entries": []any{call("toolu_x", "completed")}})
	if got := h.drafts(mix.ID); got[0].ToolCallID != "toolu_x" {
		t.Fatalf("a call with its own id %+v", got)
	}
}

func (e *env) drafts(mixID string) []draftView {
	e.t.Helper()
	var list struct{ Items []draftView }
	e.ok(e.do("GET", "/api/drafts?entityKind=mix&entityId="+mixID, ""), 200, &list)
	return list.Items
}

func pgxCollect(h *hostEnv, sql, arg string, out *[]string) error {
	rows, err := h.pool.Query(h.t.Context(), sql, arg)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return err
		}
		*out = append(*out, s)
	}
	return rows.Err()
}
