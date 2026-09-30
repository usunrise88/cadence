//go:build integration

package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/usunrise88/cadence/control-plane/internal/credentials"
	"github.com/usunrise88/cadence/control-plane/internal/projects/bootstrap"
)

// Agent credentials (Settings → Agents) against the real control plane: a value set by the admin travels through the
// transit store to the agent host (claim → write → report) and is deleted after the acknowledgement; it never shows
// up in a response, an event, an audit row, an idempotency record or any other Postgres row. Verification, opencode's
// model list and default model, custom base URLs on the egress allowlist, disconnect, supersede and the sweeper, and
// who may do what.

type agentCredView struct {
	ID           string   `json:"id"`
	Agent        string   `json:"agent"`
	Provider     string   `json:"provider"`
	CatalogueID  string   `json:"catalogueId"`
	Name         string   `json:"name"`
	BaseURL      string   `json:"baseUrl"`
	HasValue     bool     `json:"hasValue"`
	Hint         string   `json:"hint"`
	ExpiresAt    string   `json:"expiresAt"`
	Models       []string `json:"models"`
	DefaultModel string   `json:"defaultModel"`
	Hosts        []string `json:"hosts"`
	ArchivedAt   string   `json:"archivedAt"`
	Rev          int      `json:"rev"`
	Delivery     struct{ State, Detail string }
	Verification struct{ State, Detail, Model string }
}

type agentCredList struct {
	Items []agentCredView `json:"items"`
	Host  struct {
		Connected bool   `json:"connected"`
		SeenAt    string `json:"seenAt"`
	} `json:"host"`
	OpencodeDefault struct{ Model, Source string } `json:"opencodeDefault"`
}

type credTask struct {
	ID, Action, CredentialID, Agent, Provider, CatalogueID, Name, Value, BaseURL, VerifyModel string
	Models                                                                                    []string
}

func TestAgentCredentials(t *testing.T) {
	h := startHost(t)
	e := h.env
	const secret = "sk-ant-oat01-THE-SECRET-VALUE-4f9c2a7e1d-WXYZ"
	const minimaxKey = "mmx-KEY-VALUE-ab12cd34ef56gh78-QRST"
	// noValue fails the test when a response body carries a value.
	noValue := func(resp *http.Response) []byte {
		t.Helper()
		defer func() { _ = resp.Body.Close() }()
		b, _ := io.ReadAll(resp.Body)
		for _, v := range []string{secret, minimaxKey} {
			if strings.Contains(string(b), v) {
				t.Fatalf("%s %s returned a value: %s", resp.Request.Method, resp.Request.URL.Path, b)
			}
		}
		resp.Body = io.NopCloser(strings.NewReader(string(b)))
		return b
	}
	list := func() agentCredList {
		t.Helper()
		var l agentCredList
		e.ok(e.do("GET", "/api/agent-credentials", ""), 200, &l)
		return l
	}
	get := func(id string) agentCredView {
		t.Helper()
		var c agentCredView
		resp := e.do("GET", "/api/agent-credentials/"+id, "")
		noValue(resp)
		e.ok(resp, 200, &c)
		return c
	}
	set := func(id, body string, hdr ...string) agentCredView {
		t.Helper()
		var c agentCredView
		resp := e.do("PUT", "/api/agent-credentials/"+id, body, append([]string{"Idempotency-Key", e.key()}, hdr...)...)
		noValue(resp)
		e.ok(resp, 200, &c)
		return c
	}
	action := func(id, verb string, rev int) agentCredView {
		t.Helper()
		var c agentCredView
		e.ok(e.do("POST", "/api/agent-credentials/"+id+":"+verb, "", "Idempotency-Key", e.key(), "If-Match", ifMatch(rev)), 200, &c)
		return c
	}
	claim := func() []credTask {
		t.Helper()
		var w struct{ Tasks []credTask }
		h.ok(h.host("POST", "/api/host-credentials:claim", map[string]any{"hostId": "host-a", "wait": 0}), 200, &w)
		return w.Tasks
	}
	report := func(id string, body map[string]any) string {
		t.Helper()
		body["hostId"] = "host-a"
		var ack struct{ ID, State string }
		h.ok(h.host("POST", "/api/host-credentials/"+id+":report", body), 200, &ack)
		return ack.State
	}
	transitFiles := func() []string {
		t.Helper()
		entries, err := os.ReadDir(filepath.Join(e.admin.Secrets.Dir(), "transit"))
		if err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
		var out []string
		for _, x := range entries {
			out = append(out, x.Name())
		}
		return out
	}

	// Nothing configured, no host yet: opencode defaults from defaults.yaml.
	l := list()
	if len(l.Items) != 0 || l.Host.Connected || l.OpencodeDefault.Model != "minimax/MiniMax-M3" || l.OpencodeDefault.Source != "defaults" {
		t.Fatalf("fresh list: %+v", l)
	}
	var providers struct{ Items []struct{ ID, Agent string } }
	e.ok(e.do("GET", "/api/agent-providers", ""), 200, &providers)
	var ids []string
	for _, p := range providers.Items {
		ids = append(ids, p.ID)
	}
	for _, want := range []string{"claude-subscription", "minimax", "anthropic", "openai", "openrouter", "deepseek", "openai-compatible"} {
		if !slices.Contains(ids, want) {
			t.Errorf("catalogue lacks %s: %v", want, ids)
		}
	}

	// Validation: a Claude token has its prefix; a first set needs the value; ids follow the pattern.
	expectProblem(t, e.do("PUT", "/api/agent-credentials/claude-code", `{"value":"sk-proj-not-a-claude-token-000000"}`, "Idempotency-Key", e.key()), 422, "validation-failed")
	expectProblem(t, e.do("PUT", "/api/agent-credentials/claude-code", `{"name":"x"}`, "Idempotency-Key", e.key()), 422, "validation-failed")
	expectProblem(t, e.do("PUT", "/api/agent-credentials/opencode.minimax", `{"baseUrl":"http://x/v1","value":"`+minimaxKey+`"}`, "Idempotency-Key", e.key()), 422, "validation-failed")

	// A dry run writes nothing, not even the transit value.
	resp := e.do("PUT", "/api/agent-credentials/claude-code?dryRun=true", `{"value":"`+secret+`"}`, "Idempotency-Key", e.key())
	noValue(resp)
	e.ok(resp, 200, nil)
	if n := e.count("SELECT count(*) FROM agent_credentials"); n != 0 || len(transitFiles()) != 0 {
		t.Fatalf("dry run left %d rows and transit %v", n, transitFiles())
	}

	// Connect Claude Code: metadata only, delivery pending, the value in transit.
	key := e.key()
	resp = e.do("PUT", "/api/agent-credentials/claude-code", `{"value":"`+secret+`"}`, "Idempotency-Key", key)
	noValue(resp)
	var claude agentCredView
	e.ok(resp, 200, &claude)
	if !claude.HasValue || claude.Hint != "…WXYZ" || claude.Delivery.State != "pending" || claude.Verification.State != "none" ||
		claude.Agent != "claude-code" || claude.CatalogueID != "claude-subscription" || !slices.Contains(claude.Hosts, "api.anthropic.com") {
		t.Fatalf("claude after set: %+v", claude)
	}
	exp, err := time.Parse(time.RFC3339, claude.ExpiresAt)
	if err != nil || time.Until(exp) < 364*24*time.Hour || time.Until(exp) > 366*24*time.Hour {
		t.Errorf("expiresAt %q is not about a year out", claude.ExpiresAt)
	}
	replay := e.do("PUT", "/api/agent-credentials/claude-code", `{"value":"`+secret+`"}`, "Idempotency-Key", key)
	noValue(replay)
	if replay.Header.Get("Idempotent-Replayed") != "true" {
		t.Error("the same key did not replay")
	}
	_ = replay.Body.Close()
	if got := transitFiles(); len(got) != 1 {
		t.Fatalf("transit after set: %v", got)
	}
	// The existing credential needs If-Match, and the right one.
	expectProblem(t, e.do("PUT", "/api/agent-credentials/claude-code", `{"value":"`+secret+`"}`, "Idempotency-Key", e.key()), 428, "precondition-required")
	expectProblem(t, e.do("PUT", "/api/agent-credentials/claude-code", `{"value":"`+secret+`"}`, "Idempotency-Key", e.key(), "If-Match", ifMatch(9)), 412, "precondition-failed")

	// The host claims the write, gets the value, and acknowledges; the transit value is gone.
	tasks := claim()
	if len(tasks) != 1 || tasks[0].Action != "write" || tasks[0].Value != secret || tasks[0].CredentialID != "claude-code" || tasks[0].Agent != "claude-code" {
		t.Fatalf("claim: %+v", tasks)
	}
	if again := claim(); len(again) != 0 {
		t.Fatalf("a claimed task was handed out again: %+v", again)
	}
	if !list().Host.Connected {
		t.Error("the host is not connected after a claim")
	}
	if st := report(tasks[0].ID, map[string]any{"ok": true, "detail": "wrote claude/oauth-token"}); st != "done" {
		t.Errorf("ack state %q", st)
	}
	if got := transitFiles(); len(got) != 0 {
		t.Errorf("transit after the ack: %v", got)
	}
	if st := report(tasks[0].ID, map[string]any{"ok": true}); st != "superseded" {
		t.Errorf("a second ack: %q", st)
	}
	claude = get("claude-code")
	if claude.Delivery.State != "written" {
		t.Fatalf("claude after the ack: %+v", claude)
	}

	// Verify: a verify task with the cheap model, no value; the result is recorded.
	claude = action("claude-code", "verify", claude.Rev)
	if claude.Verification.State != "pending" {
		t.Fatalf("after verify: %+v", claude)
	}
	tasks = claim()
	if len(tasks) != 1 || tasks[0].Action != "verify" || tasks[0].Value != "" || tasks[0].VerifyModel != "haiku" {
		t.Fatalf("verify claim: %+v", tasks)
	}
	report(tasks[0].ID, map[string]any{"ok": true, "detail": "OK", "model": "haiku"})
	claude = get("claude-code")
	if claude.Verification.State != "ok" || claude.Verification.Detail != "OK" || claude.Verification.Model != "haiku" {
		t.Fatalf("claude after verification: %+v", claude)
	}

	// Replacing the value twice before the host claims: only the newest write (and value) is handed out; the older
	// transit value is an orphan the sweeper drops.
	claude = set("claude-code", `{"value":"sk-ant-oat01-first-replacement-0000-AAAA"}`, "If-Match", ifMatch(claude.Rev))
	claude = set("claude-code", `{"value":"`+secret+`"}`, "If-Match", ifMatch(claude.Rev))
	if got := transitFiles(); len(got) != 2 {
		t.Fatalf("transit with two pending writes: %v", got)
	}
	tasks = claim()
	if len(tasks) != 1 || tasks[0].Value != secret {
		t.Fatalf("claim after two sets: %+v", tasks)
	}
	report(tasks[0].ID, map[string]any{"ok": true})
	orphans := transitFiles()
	if len(orphans) != 1 {
		t.Fatalf("transit after the newest write: %v", orphans)
	}
	old := time.Now().Add(-2 * time.Minute)
	if err := os.Chtimes(filepath.Join(e.admin.Secrets.Dir(), "transit", orphans[0]), old, old); err != nil {
		t.Fatal(err)
	}
	if err := e.admin.SweepAgentCredentials(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got := transitFiles(); len(got) != 0 {
		t.Errorf("transit after the sweep: %v", got)
	}

	// opencode with MiniMax: the verification returns the provider's models (other providers' ids are dropped), and the
	// admin picks the default model for new projects.
	mm := set("opencode.minimax", `{"value":"`+minimaxKey+`"}`)
	if mm.CatalogueID != "minimax" || mm.Name != "MiniMax" || mm.Provider != "minimax" || mm.Hint != "…QRST" {
		t.Fatalf("minimax after set: %+v", mm)
	}
	tasks = claim()
	if len(tasks) != 1 || tasks[0].Value != minimaxKey || tasks[0].Provider != "minimax" {
		t.Fatalf("minimax claim: %+v", tasks)
	}
	report(tasks[0].ID, map[string]any{"ok": true})
	mm = action("opencode.minimax", "verify", get("opencode.minimax").Rev)
	tasks = claim()
	if len(tasks) != 1 || tasks[0].VerifyModel != "minimax/MiniMax-M3" {
		t.Fatalf("minimax verify claim: %+v", tasks)
	}
	report(tasks[0].ID, map[string]any{"ok": true, "model": "minimax/MiniMax-M3",
		"models": []string{"minimax/MiniMax-M3", "minimax/MiniMax-M2.5", "openai/gpt-5"}})
	mm = get("opencode.minimax")
	if mm.Verification.State != "ok" || !slices.Equal(mm.Models, []string{"minimax/MiniMax-M3", "minimax/MiniMax-M2.5"}) {
		t.Fatalf("minimax after verification: %+v", mm)
	}
	expectProblem(t, e.do("PUT", "/api/agent-credentials/opencode.minimax", `{"defaultModel":"minimax/Nope"}`,
		"Idempotency-Key", e.key(), "If-Match", ifMatch(mm.Rev)), 422, "validation-failed")
	mm = set("opencode.minimax", `{"defaultModel":"minimax/MiniMax-M2.5"}`, "If-Match", ifMatch(mm.Rev))
	if mm.DefaultModel != "minimax/MiniMax-M2.5" || mm.Delivery.State != "written" {
		t.Fatalf("minimax after choosing the default: %+v", mm)
	}
	if l := list(); l.OpencodeDefault.Model != "minimax/MiniMax-M2.5" || l.OpencodeDefault.Source != "configured" {
		t.Errorf("opencode default: %+v", l.OpencodeDefault)
	}
	var models struct {
		Items []struct {
			Driver, Default string
			Models          []struct{ ID string }
		}
	}
	e.ok(e.do("GET", "/api/catalog/agent-models", ""), 200, &models)
	for _, m := range models.Items {
		if m.Driver != "opencode" {
			continue
		}
		var got []string
		for _, x := range m.Models {
			got = append(got, x.ID)
		}
		if m.Default != "minimax/MiniMax-M2.5" || !slices.Contains(got, "minimax/MiniMax-M2.5") || !slices.Contains(got, "minimax/MiniMax-M3") {
			t.Errorf("agentModels.list opencode: default %s, models %v", m.Default, got)
		}
	}
	if m, err := bootstrap.DefaultModel(t.Context(), e.pool, "opencode"); err != nil || m != "minimax/MiniMax-M2.5" {
		t.Errorf("new opencode projects start from %q (%v)", m, err)
	}
	if m, _ := bootstrap.DefaultModel(t.Context(), e.pool, "claude-code"); m != "sonnet" {
		t.Errorf("new Claude Code projects start from %q", m)
	}

	// A custom OpenAI-compatible provider: no key needed, the base URL's host:port joins the egress allowlist.
	expectProblem(t, e.do("PUT", "/api/agent-credentials/opencode.vllm", `{"catalogueId":"openai-compatible"}`, "Idempotency-Key", e.key()), 422, "validation-failed")
	vllm := set("opencode.vllm", `{"catalogueId":"openai-compatible","name":"Lab vLLM","baseUrl":"http://vllm.lan:8000/v1/"}`)
	if vllm.HasValue || vllm.BaseURL != "http://vllm.lan:8000/v1" || vllm.Name != "Lab vLLM" || !slices.Equal(vllm.Hosts, []string{"vllm.lan:8000"}) {
		t.Fatalf("vllm after set: %+v", vllm)
	}
	tasks = claim()
	if len(tasks) != 1 || tasks[0].Value != "" || tasks[0].BaseURL != "http://vllm.lan:8000/v1" || tasks[0].CatalogueID != "openai-compatible" || tasks[0].Name != "Lab vLLM" {
		t.Fatalf("vllm claim: %+v", tasks)
	}
	report(tasks[0].ID, map[string]any{"ok": false, "detail": "disk full"})
	if v := get("opencode.vllm"); v.Delivery.State != "failed" || v.Delivery.Detail != "disk full" {
		t.Errorf("vllm after a failed write: %+v", v)
	}
	var egress struct{ Hosts []string }
	e.ok(e.do("GET", "/api/egress-hosts", ""), 200, &egress)
	if !slices.Equal(egress.Hosts, []string{"api.anthropic.com", "api.minimax.io", "vllm.lan:8000"}) {
		t.Errorf("egress hosts: %v", egress.Hosts)
	}

	// Disconnect MiniMax: off the allowlist and no longer the default at once; the host removes it from the volume.
	mm = action("opencode.minimax", "archive", get("opencode.minimax").Rev)
	if mm.ArchivedAt == "" || mm.Delivery.State != "removing" || mm.DefaultModel != "" {
		t.Fatalf("minimax after archive: %+v", mm)
	}
	if l := list(); l.OpencodeDefault.Source != "defaults" || l.OpencodeDefault.Model != "minimax/MiniMax-M3" {
		t.Errorf("default after disconnecting its provider: %+v", l.OpencodeDefault)
	}
	e.ok(e.do("GET", "/api/egress-hosts", ""), 200, &egress)
	if slices.Contains(egress.Hosts, "api.minimax.io") {
		t.Errorf("egress hosts after archive: %v", egress.Hosts)
	}
	tasks = claim()
	if len(tasks) != 1 || tasks[0].Action != "remove" || tasks[0].CredentialID != "opencode.minimax" {
		t.Fatalf("remove claim: %+v", tasks)
	}
	report(tasks[0].ID, map[string]any{"ok": true})
	for _, c := range list().Items {
		if c.ID == "opencode.minimax" {
			t.Errorf("a removed credential is still listed: %+v", c)
		}
	}
	if st := get("opencode.minimax").Delivery.State; st != "removed" {
		t.Errorf("minimax delivery after the removal: %s", st)
	}

	// Values never reached Postgres: not a column of any row of any table (events, audit, idempotency keys included).
	tables, err := e.pool.Query(t.Context(), `SELECT tablename FROM pg_tables WHERE schemaname = 'public'`)
	if err != nil {
		t.Fatal(err)
	}
	names, err := pgx.CollectRows(tables, pgx.RowTo[string])
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range names {
		for _, v := range []string{secret, minimaxKey, "first-replacement"} {
			var n int
			if err := e.pool.QueryRow(t.Context(), `SELECT count(*) FROM "`+name+`" x WHERE x::text LIKE '%' || $1 || '%'`, v).Scan(&n); err != nil {
				t.Fatalf("scan %s: %v", name, err)
			}
			if n > 0 {
				t.Errorf("table %s holds a value in %d rows", name, n)
			}
		}
	}
	if n := e.count(`SELECT count(*) FROM events WHERE topic LIKE 'entity.agent_credential.%'`); n < 10 {
		t.Errorf("only %d agent credential events", n)
	}
	if n := e.count(`SELECT count(*) FROM audit_log WHERE operation LIKE 'agentCredentials.%'`); n < 8 {
		t.Errorf("only %d audit rows of agent credential commands", n)
	}
}

// TestAgentCredentialsAccess: admin only — an API key, an agent session token and the agent host token are refused,
// an agent actor meets the preset's admin-only rule, and the egress allowlist is the proxy's (and the admin's).
func TestAgentCredentialsAccess(t *testing.T) {
	h := startHost(t)
	e := h.env
	ctx := context.Background()
	var apiKey, egressTok string
	if err := pgx.BeginFunc(ctx, e.pool, func(tx pgx.Tx) error {
		var err error
		if apiKey, _, _, err = credentials.NewAPIKey(ctx, tx, credentials.NewAPIKeyInput{UserID: "usr_admin", Name: "ci", Registry: true}); err != nil {
			return err
		}
		egressTok, _, err = credentials.NewEgressToken(ctx, tx)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	var p project
	e.ok(e.do("POST", "/api/projects", `{"slug":"creds","name":"Creds"}`, "Idempotency-Key", e.key()), 202, nil)
	e.ok(e.do("GET", "/api/projects/creds", ""), 200, &p)
	var agentTok string
	if err := pgx.BeginFunc(ctx, e.pool, func(tx pgx.Tx) error {
		var err error
		agentTok, _, err = credentials.MintAgentToken(ctx, tx, "ses_creds", p.ID, "guardrails-default")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	for name, tok := range map[string]string{"api key": apiKey, "agent token": agentTok, "host token": h.hostToken, "egress token": egressTok} {
		expectProblem(t, h.send(h.url, "GET", "/api/agent-credentials", "", bearer(tok)...), 403, "forbidden")
		expectProblem(t, h.send(h.url, "GET", "/api/agent-providers", "", bearer(tok)...), 403, "forbidden")
		expectProblem(t, h.send(h.url, "PUT", "/api/agent-credentials/claude-code", `{"value":"sk-ant-oat01-abcdefghijklmnop"}`,
			bearer(tok, "Idempotency-Key", e.key())...), 403, "forbidden")
		_ = name
	}
	// An agent actor with full scope still meets the preset (defence in depth).
	expectProblem(t, e.agent("PUT", "/api/agent-credentials/claude-code", `{"value":"sk-ant-oat01-abcdefghijklmnop"}`,
		"Idempotency-Key", e.key()), 403, "policy-denied")
	// The host protocol is the host's; the egress list is the proxy's and the admin's.
	expectProblem(t, h.send(h.url, "POST", "/api/host-credentials:claim", `{"hostId":"x","wait":0}`, bearer(apiKey)...), 403, "forbidden")
	expectProblem(t, h.send(h.url, "POST", "/api/host-credentials:claim", `{"hostId":"x","wait":0}`, bearer(egressTok)...), 403, "forbidden")
	var egress struct{ Hosts []string }
	h.ok(h.send(h.url, "GET", "/api/egress-hosts", "", bearer(egressTok)...), 200, &egress)
	for _, tok := range []string{h.hostToken, agentTok, apiKey} {
		expectProblem(t, h.send(h.url, "GET", "/api/egress-hosts", "", bearer(tok)...), 403, "forbidden")
	}
	// The egress token reaches nothing else.
	expectProblem(t, h.send(h.url, "GET", "/api/projects/creds", "", bearer(egressTok)...), 403, "forbidden")
}

// TestAgentCredentialsNotTools: no agent credential operation is an MCP tool (the exempt tag).
func TestAgentCredentialsNotTools(t *testing.T) {
	raw, err := os.ReadFile("../mcp/tools.json")
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Tools []struct{ Name string } `json:"tools"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc.Tools) == 0 {
		t.Fatal("no tools in the manifest")
	}
	for _, tool := range doc.Tools {
		for _, prefix := range []string{"agentCredentials.", "agentProviders.", "hostCredentials.", "egressHosts."} {
			if strings.HasPrefix(tool.Name, prefix) {
				t.Errorf("%s is an MCP tool", tool.Name)
			}
		}
	}
}
