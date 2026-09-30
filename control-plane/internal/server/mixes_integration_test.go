//go:build integration

package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/usunrise88/cadence/control-plane/internal/auth"
	"github.com/usunrise88/cadence/control-plane/internal/credentials"
	"github.com/usunrise88/cadence/control-plane/internal/defaults"
	"github.com/usunrise88/cadence/control-plane/internal/events"
	"github.com/usunrise88/cadence/control-plane/internal/mcp"
	"github.com/usunrise88/cadence/control-plane/internal/mcp/mcptest"
	"github.com/usunrise88/cadence/control-plane/internal/obs"
	"github.com/usunrise88/cadence/control-plane/internal/registry"
	"github.com/usunrise88/cadence/control-plane/templates"
)

// Mixes and drafts against the real control plane: a person's edit is a new revision, an agent's lands as a draft
// that a person accepts or reverts; stale revisions and stale drafts answer 412; events carry attribution and
// presence; the MCP tool call path with a real agent token.

type presenceView struct {
	Actor      auth.Actor `json:"actor"`
	ToolCallID string     `json:"toolCallId"`
	DraftID    string     `json:"draftId"`
	Until      *time.Time `json:"until"`
}

type mixView struct {
	ID          string  `json:"id"`
	ProjectID   string  `json:"projectId"`
	Name        string  `json:"name"`
	Rev         int     `json:"rev"`
	Temperature float64 `json:"temperature"`
	ReplayShare float64 `json:"replayShare"`
	Groups      []struct {
		Name     string   `json:"name"`
		Weight   float64  `json:"weight"`
		Replay   bool     `json:"replay"`
		Datasets []string `json:"datasets"`
	} `json:"groups"`
	UpdatedBy auth.Actor `json:"updatedBy"`
	Cause     *struct {
		DraftID     string      `json:"draftId"`
		DraftAuthor *auth.Actor `json:"draftAuthor"`
		ToolCallID  string      `json:"toolCallId"`
	} `json:"cause"`
	Presence []presenceView `json:"presence"`
	Preview  struct {
		TotalHours float64 `json:"totalHours"`
		Languages  []struct {
			Locale string  `json:"locale"`
			Hours  float64 `json:"hours"`
			Share  float64 `json:"share"`
		} `json:"languages"`
		Datasets []struct {
			ID      string `json:"id"`
			Adopted bool   `json:"adopted"`
		} `json:"datasets"`
	} `json:"preview"`
}

type draftView struct {
	ID         string     `json:"id"`
	EntityKind string     `json:"entityKind"`
	EntityID   string     `json:"entityId"`
	BaseRev    int        `json:"baseRev"`
	CurrentRev int        `json:"currentRev"`
	Rev        int        `json:"rev"`
	State      string     `json:"state"`
	Stale      bool       `json:"stale"`
	Author     auth.Actor `json:"author"`
	ToolCallID string     `json:"toolCallId"`
	AppliedRev *int       `json:"appliedRev"`
	Content    struct {
		Name        string  `json:"name"`
		Temperature float64 `json:"temperature"`
		ReplayShare float64 `json:"replayShare"`
		Groups      []any   `json:"groups"`
	} `json:"content"`
	Changes []struct {
		Path string `json:"path"`
	} `json:"changes"`
}

type editResult struct {
	Mix   mixView    `json:"mix"`
	Draft *draftView `json:"draft"`
}

// reset zeroes v before it is decoded into again (json.Unmarshal keeps what the new document leaves out).
func reset[T any](v *T) *T {
	var zero T
	*v = zero
	return v
}

// startMixes is start with the registry seeded (the fixture dataset versions) and a project "hebrew".
func startMixes(t *testing.T) (*env, project) {
	t.Helper()
	e := start(t)
	if _, err := registry.Seed(t.Context(), e.pool, templates.FS, time.Now()); err != nil {
		t.Fatal(err)
	}
	return e, e.newProject("hebrew")
}

const heMix = `{"name":"he-smoke","groups":[{"name":"target","datasets":["dataset/fleurs-he-smoke"]}]}`

func (e *env) newMix(body string) mixView {
	e.t.Helper()
	var m mixView
	e.ok(e.do("POST", "/api/projects/hebrew/mixes", body, "Idempotency-Key", e.key()), 201, &m)
	return m
}

func (e *env) mix(id string) mixView {
	e.t.Helper()
	var m mixView
	e.ok(e.do("GET", "/api/mixes/"+id, ""), 200, &m)
	return m
}

func eventTypes(recs []events.Record) []string {
	out := make([]string, 0, len(recs))
	for _, r := range recs {
		out = append(out, r.Type)
	}
	return out
}

func TestMixPersonEditsDirectly(t *testing.T) {
	e, p := startMixes(t)
	var m mixView
	resp := e.ok(e.do("POST", "/api/projects/hebrew/mixes", heMix, "Idempotency-Key", e.key()), 201, &m)
	if resp.Header.Get("ETag") != `"1"` || m.Rev != 1 || !strings.HasPrefix(m.ID, "mix_") || m.ProjectID != p.ID ||
		m.Temperature != 1 || m.ReplayShare != 0 || len(m.Groups) != 1 || m.Groups[0].Weight != 1 ||
		!strings.HasPrefix(m.Groups[0].Datasets[0], "ver_") || len(m.Presence) != 0 || m.UpdatedBy.ID != "usr_admin" {
		t.Fatalf("created %+v (ETag %s)", m, resp.Header.Get("ETag"))
	}
	if len(m.Preview.Languages) != 1 || m.Preview.Languages[0].Locale != "he-IL" || m.Preview.Languages[0].Hours != 1.8 ||
		m.Preview.Languages[0].Share != 1 || m.Preview.TotalHours != 1.8 || m.Preview.Datasets[0].Adopted {
		t.Errorf("preview %+v", m.Preview)
	}

	// Validation: dataset references resolve and must be frozen datasets; a replay share needs a replay group.
	for _, tt := range []struct{ body, path string }{
		{`{"name":"x","groups":[{"name":"t","datasets":["dataset/nope"]}]}`, "/groups/0/datasets/0"},
		{`{"name":"x","groups":[{"name":"t","datasets":["` + m.ID + `"]}]}`, "/groups/0/datasets/0"},
		{`{"name":"x","replayShare":0.2,"groups":[{"name":"t","datasets":["fleurs-he-smoke"]}]}`, "/replayShare"},
		{`{"name":"x","temperature":50,"groups":[{"name":"t","datasets":["fleurs-he-smoke"]}]}`, "/temperature"},
		{`{"name":"x","groups":[{"name":"t","datasets":["fleurs-he-smoke"]},{"name":"T","datasets":["fleurs-ru-smoke"]}]}`, "/groups/1/name"},
		{`{"name":"x","groups":[{"name":"t","replay":true,"datasets":["fleurs-he-smoke"]}]}`, "/groups"},
	} {
		pr := expectProblem(t, e.do("POST", "/api/projects/hebrew/mixes", tt.body, "Idempotency-Key", e.key()), 422, "validation-failed")
		if len(pr.Errors) == 0 || pr.Errors[0].Path != tt.path {
			t.Errorf("%s: errors %+v, want path %s", tt.body, pr.Errors, tt.path)
		}
	}
	expectProblem(t, e.do("POST", "/api/projects/hebrew/mixes", heMix, "Idempotency-Key", e.key()), 409, "conflict")

	// Preview without saving: replay gets the default share (defaults.yaml mix.replay_share).
	var pv struct {
		Languages []struct {
			Locale string
			Share  float64
		}
	}
	e.ok(e.do("POST", "/api/projects/hebrew/mixes:preview", `{"name":"p","groups":[{"name":"t","datasets":["fleurs-he-smoke"]},
		{"name":"r","replay":true,"datasets":["@nope"]}]}`, "Idempotency-Key", e.key()), 422, nil)
	e.ok(e.do("POST", "/api/projects/hebrew/mixes:preview", `{"name":"p","groups":[{"name":"t","datasets":["fleurs-he-smoke"]},
		{"name":"r","replay":true,"datasets":["dataset/fleurs-ru-smoke"]}]}`, "Idempotency-Key", e.key()), 200, &pv)
	if len(pv.Languages) != 2 || pv.Languages[0].Locale != "he-IL" || pv.Languages[0].Share != 0.85 || pv.Languages[1].Share != 0.15 {
		t.Errorf("preview %+v", pv)
	}

	resp = e.ok(e.do("GET", "/api/mixes/"+m.ID, ""), 200, nil)
	if resp.Header.Get("ETag") != `"1"` {
		t.Errorf("get ETag %s", resp.Header.Get("ETag"))
	}
	var er editResult
	resp = e.ok(e.do("PATCH", "/api/mixes/"+m.ID, `{"temperature":2}`, "Idempotency-Key", e.key(), "If-Match", `"1"`), 200, reset(&er))
	if er.Draft != nil || er.Mix.Rev != 2 || er.Mix.Temperature != 2 || er.Mix.Name != "he-smoke" || resp.Header.Get("ETag") != `"2"` {
		t.Fatalf("person edit %+v", er)
	}
	pr := expectProblem(t, e.do("PATCH", "/api/mixes/"+m.ID, `{"temperature":3}`, "Idempotency-Key", e.key(), "If-Match", `"1"`), 412, "precondition-failed")
	if pr.CurrentRev == nil || *pr.CurrentRev != 2 {
		t.Errorf("stale edit currentRev %v", pr.CurrentRev)
	}
	expectProblem(t, e.do("PATCH", "/api/mixes/"+m.ID, `{"temperature":3}`, "Idempotency-Key", e.key()), 428, "precondition-required")

	var list struct{ Items []mixView }
	e.ok(e.do("GET", "/api/projects/hebrew/mixes", ""), 200, &list)
	if len(list.Items) != 1 || list.Items[0].Rev != 2 {
		t.Errorf("list %+v", list)
	}
	if n := e.count("SELECT count(*) FROM mix_revisions"); n != 2 {
		t.Errorf("%d revisions stored, want 2", n)
	}
	evs := e.events("entity.mix." + m.ID)
	if fmt.Sprint(eventTypes(evs)) != "[mix.created mix.revised]" || evs[1].Actor.ID != "usr_admin" || evs[1].ProjectID != p.ID ||
		evs[1].Entity == nil || evs[1].Entity.Rev != 2 {
		t.Errorf("events %+v", evs)
	}
}

func TestAgentEditLandsAsDraft(t *testing.T) {
	e, _ := startMixes(t)
	m := e.newMix(heMix)
	edit := `{"replayShare":0.2,"groups":[{"name":"target","datasets":["dataset/fleurs-he-smoke"]},{"name":"replay-ru","replay":true,"datasets":["dataset/fleurs-ru-smoke"]}]}`

	var er editResult
	resp := e.ok(e.agent("PATCH", "/api/mixes/"+m.ID, edit, "Idempotency-Key", e.key(), "If-Match", `"1"`,
		"Cadence-Tool-Call-Id", "toolu_A"), 200, reset(&er))
	d := er.Draft
	if d == nil || er.Mix.Rev != 1 || resp.Header.Get("ETag") != `"1"` || len(er.Mix.Groups) != 1 {
		t.Fatalf("agent edit did not land as a draft: %+v", er)
	}
	if d.State != "open" || d.Rev != 1 || d.BaseRev != 1 || d.Stale || d.Author.Kind != "agent" || d.Author.SessionID != "ses_test" ||
		d.ToolCallID != "toolu_A" || len(d.Content.Groups) != 2 || !strings.HasPrefix(d.ID, "drf_") {
		t.Fatalf("draft %+v", d)
	}
	paths := map[string]bool{}
	for _, c := range d.Changes {
		paths[c.Path] = true
	}
	if !paths["/groups/1"] || !paths["/replayShare"] || len(d.Changes) != 2 {
		t.Errorf("changes %+v", d.Changes)
	}
	got := e.mix(m.ID)
	if got.Rev != 1 || len(got.Presence) != 1 || got.Presence[0].DraftID != d.ID || got.Presence[0].ToolCallID != "toolu_A" ||
		got.Presence[0].Actor.SessionID != "ses_test" || got.Presence[0].Until != nil {
		t.Fatalf("presence %+v", got.Presence)
	}

	// The agent's next edit updates the same draft (rev 2) on top of its first one.
	e.ok(e.agent("PATCH", "/api/mixes/"+m.ID, `{"temperature":1.5}`, "Idempotency-Key", e.key(), "If-Match", `"1"`,
		"Cadence-Tool-Call-Id", "toolu_B"), 200, reset(&er))
	if er.Draft.ID != d.ID || er.Draft.Rev != 2 || er.Draft.Content.Temperature != 1.5 || len(er.Draft.Content.Groups) != 2 ||
		er.Draft.ToolCallID != "toolu_B" {
		t.Fatalf("second edit %+v", er.Draft)
	}
	var list struct{ Items []draftView }
	e.ok(e.do("GET", "/api/drafts?entityKind=mix&entityId="+m.ID, ""), 200, &list)
	if len(list.Items) != 1 || list.Items[0].Rev != 2 {
		t.Fatalf("drafts.list %+v", list.Items)
	}

	// An agent never accepts a draft; a stale draft revision is 412 with the draft's revision.
	expectProblem(t, e.agent("POST", "/api/drafts/"+d.ID+":accept", "", "Idempotency-Key", e.key(), "If-Match", `"2"`), 403, "policy-denied")
	pr := expectProblem(t, e.do("POST", "/api/drafts/"+d.ID+":accept", "", "Idempotency-Key", e.key(), "If-Match", `"1"`), 412, "precondition-failed")
	if pr.CurrentRev == nil || *pr.CurrentRev != 2 {
		t.Errorf("stale draft currentRev %v", pr.CurrentRev)
	}
	// A dry run changes nothing.
	e.ok(e.do("POST", "/api/drafts/"+d.ID+":accept?dryRun=true", "", "Idempotency-Key", e.key(), "If-Match", `"2"`), 200, nil)
	if e.mix(m.ID).Rev != 1 {
		t.Fatal("dry-run accept changed the mix")
	}

	var acc struct {
		Draft  draftView `json:"draft"`
		Entity mixView   `json:"entity"`
	}
	resp = e.ok(e.do("POST", "/api/drafts/"+d.ID+":accept", "", "Idempotency-Key", e.key(), "If-Match", `"2"`), 200, &acc)
	if acc.Draft.State != "accepted" || acc.Draft.Rev != 3 || acc.Draft.AppliedRev == nil || *acc.Draft.AppliedRev != 2 ||
		resp.Header.Get("ETag") != `"3"` {
		t.Fatalf("accepted draft %+v", acc.Draft)
	}
	if acc.Entity.Rev != 2 || len(acc.Entity.Groups) != 2 || acc.Entity.Temperature != 1.5 || acc.Entity.ReplayShare != 0.2 ||
		acc.Entity.UpdatedBy.ID != "usr_admin" || acc.Entity.Cause == nil || acc.Entity.Cause.DraftID != d.ID ||
		acc.Entity.Cause.DraftAuthor == nil || acc.Entity.Cause.DraftAuthor.Kind != "agent" || acc.Entity.Cause.ToolCallID != "toolu_B" ||
		len(acc.Entity.Presence) != 0 {
		t.Fatalf("accepted mix %+v", acc.Entity)
	}
	expectProblem(t, e.do("POST", "/api/drafts/"+d.ID+":accept", "", "Idempotency-Key", e.key(), "If-Match", `"3"`), 409, "conflict")

	evs := e.events("entity.mix." + m.ID)
	want := "[mix.created draft.created presence.changed draft.updated presence.changed mix.revised draft.accepted presence.changed]"
	if fmt.Sprint(eventTypes(evs)) != want {
		t.Fatalf("events %v, want %s", eventTypes(evs), want)
	}
	created, revised, cleared := evs[1], evs[5], evs[7]
	if created.Actor.Kind != "agent" || created.Actor.SessionID != "ses_test" || created.CausedBy == nil || created.CausedBy.ToolCallID != "toolu_A" {
		t.Errorf("draft.created attribution %+v %+v", created.Actor, created.CausedBy)
	}
	var pres struct{ Presence []presenceView }
	if err := json.Unmarshal(evs[2].Payload, &pres); err != nil || len(pres.Presence) != 1 || pres.Presence[0].DraftID != d.ID {
		t.Errorf("presence.changed payload %s", evs[2].Payload)
	}
	if revised.Actor.ID != "usr_admin" || revised.CausedBy == nil || revised.CausedBy.DraftID != d.ID || revised.Entity.Rev != 2 {
		t.Errorf("mix.revised attribution %+v %+v", revised.Actor, revised.CausedBy)
	}
	if err := json.Unmarshal(cleared.Payload, &pres); err != nil || len(pres.Presence) != 0 {
		t.Errorf("presence after accept %s", cleared.Payload)
	}

	// A new edit opens a new draft on revision 2; reverting it leaves the mix as it is.
	e.ok(e.agent("PATCH", "/api/mixes/"+m.ID, `{"temperature":4}`, "Idempotency-Key", e.key(), "If-Match", `"2"`), 200, reset(&er))
	if er.Draft == nil || er.Draft.ID == d.ID || er.Draft.BaseRev != 2 {
		t.Fatalf("second draft %+v", er.Draft)
	}
	var rev draftView
	e.ok(e.do("POST", "/api/drafts/"+er.Draft.ID+":revert", "", "Idempotency-Key", e.key(), "If-Match", `"1"`), 200, &rev)
	if rev.State != "reverted" {
		t.Fatalf("reverted %+v", rev)
	}
	if got := e.mix(m.ID); got.Rev != 2 || got.Temperature != 1.5 || len(got.Presence) != 0 {
		t.Fatalf("mix after revert %+v", got)
	}
	e.ok(e.do("GET", "/api/drafts?entityKind=mix&entityId="+m.ID, ""), 200, &list)
	if len(list.Items) != 0 {
		t.Errorf("open drafts after revert: %+v", list.Items)
	}
	e.ok(e.do("GET", "/api/drafts?entityKind=mix&entityId="+m.ID+"&state=all", ""), 200, &list)
	if len(list.Items) != 2 {
		t.Errorf("all drafts: %+v", list.Items)
	}
}

func TestStaleDraftAccept(t *testing.T) {
	e, _ := startMixes(t)
	m := e.newMix(heMix)
	var er editResult
	e.ok(e.agent("PATCH", "/api/mixes/"+m.ID, `{"temperature":2}`, "Idempotency-Key", e.key(), "If-Match", `"1"`), 200, reset(&er))
	d := er.Draft

	// A person's edit goes through directly while the draft is open.
	e.ok(e.do("PATCH", "/api/mixes/"+m.ID, `{"name":"he-smoke v2"}`, "Idempotency-Key", e.key(), "If-Match", `"1"`), 200, reset(&er))
	if er.Mix.Rev != 2 || er.Draft != nil {
		t.Fatalf("person edit %+v", er)
	}
	pr := expectProblem(t, e.do("POST", "/api/drafts/"+d.ID+":accept", "", "Idempotency-Key", e.key(), "If-Match", `"1"`), 412, "draft-stale")
	if pr.CurrentRev == nil || *pr.CurrentRev != 2 {
		t.Errorf("draft-stale currentRev %v", pr.CurrentRev)
	}
	var got draftView
	e.ok(e.do("GET", "/api/drafts/"+d.ID, ""), 200, &got)
	if !got.Stale || got.CurrentRev != 2 || got.BaseRev != 1 || got.State != "open" {
		t.Fatalf("stale draft %+v", got)
	}

	// The agent's stale If-Match is refused; on the current revision its draft is carried over and keeps the
	// person's rename.
	pr = expectProblem(t, e.agent("PATCH", "/api/mixes/"+m.ID, `{"replayShare":0}`, "Idempotency-Key", e.key(), "If-Match", `"1"`), 412, "precondition-failed")
	if pr.CurrentRev == nil || *pr.CurrentRev != 2 {
		t.Errorf("agent stale edit currentRev %v", pr.CurrentRev)
	}
	e.ok(e.agent("PATCH", "/api/mixes/"+m.ID, `{"description":"rebased"}`, "Idempotency-Key", e.key(), "If-Match", `"2"`), 200, reset(&er))
	if er.Draft.ID != d.ID || er.Draft.BaseRev != 2 || er.Draft.Stale || er.Draft.Content.Name != "he-smoke v2" || er.Draft.Content.Temperature != 2 {
		t.Fatalf("rebased draft %+v", er.Draft)
	}
	var acc struct{ Entity mixView }
	e.ok(e.do("POST", "/api/drafts/"+d.ID+":accept", "", "Idempotency-Key", e.key(), "If-Match", `"2"`), 200, &acc)
	if acc.Entity.Rev != 3 || acc.Entity.Name != "he-smoke v2" || acc.Entity.Temperature != 2 {
		t.Fatalf("accepted rebased draft %+v", acc.Entity)
	}
}

func TestAgentDirectPolicy(t *testing.T) {
	e, _ := startMixes(t)
	m := e.newMix(heMix)
	d := *defaults.Get()
	d.Drafts.Mix.Value = "direct"
	direct := httptest.NewServer(newTestServer(t, e.pool, events.NewHub(1), obs.NewMetrics(), func(c *Config) {
		c.Actor, c.Defaults = testAgent, &d
	}).Handler())
	defer direct.Close()
	var er editResult
	e.ok(e.send(direct.URL, "PATCH", "/api/mixes/"+m.ID, `{"temperature":2}`, "Idempotency-Key", e.key(), "If-Match", `"1"`,
		"Cadence-Tool-Call-Id", "toolu_D"), 200, reset(&er))
	if er.Draft != nil || er.Mix.Rev != 2 || er.Mix.UpdatedBy.Kind != "agent" || er.Mix.Cause == nil || er.Mix.Cause.ToolCallID != "toolu_D" {
		t.Fatalf("direct agent edit %+v", er)
	}
	if len(er.Mix.Presence) != 1 || er.Mix.Presence[0].Until == nil || er.Mix.Presence[0].ToolCallID != "toolu_D" {
		t.Fatalf("presence of a direct edit %+v", er.Mix.Presence)
	}
	if types := eventTypes(e.events("entity.mix." + m.ID)); fmt.Sprint(types) != "[mix.created mix.revised presence.changed]" {
		t.Errorf("events %v", types)
	}
}

// TestMixDraftThroughMCP drives the edit the way an agent session does: a minted cst_ token, the /mcp endpoint,
// Claude Code's tool-use id; the open event stream sees the draft with the session and the tool call.
func TestMixDraftThroughMCP(t *testing.T) {
	e, _ := startAuth(t)
	cookie := e.setup()
	ctx := context.Background()
	if _, err := registry.Seed(ctx, e.pool, templates.FS, time.Now()); err != nil {
		t.Fatal(err)
	}
	var p project
	e.ok(e.do("POST", "/api/projects", `{"slug":"hebrew","name":"Hebrew"}`, web(cookie, "Idempotency-Key", e.key())...), 201, &p)
	var m mixView
	e.ok(e.do("POST", "/api/projects/hebrew/mixes", heMix, web(cookie, "Idempotency-Key", e.key())...), 201, &m)
	var token string
	if err := pgx.BeginFunc(ctx, e.pool, func(tx pgx.Tx) error {
		var err error
		token, _, err = credentials.MintAgentToken(ctx, tx, "ses_9", p.ID, "guardrails-default")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	frames, disconnect := e.openStream("/api/events?topics=entity.mix."+m.ID, web(cookie)...)
	defer disconnect()

	cs, _ := mcptest.Connect(t, e.url+"/mcp", mcptest.Options{Header: http.Header{
		"Authorization": {"Bearer " + token}, mcp.HeaderProject: {"hebrew"},
	}})
	r, isErr := mcptest.Call(t, cs, "mixes.get", map[string]any{"id": m.ID}, nil)
	if isErr || r.ETag != `"1"` {
		t.Fatalf("mixes.get %+v", r)
	}
	r, isErr = mcptest.Call(t, cs, "mixes.edit", map[string]any{"id": m.ID, "ifMatch": r.ETag, "body": map[string]any{"temperature": 2}},
		sdk.Meta{"claudecode/toolUseId": "toolu_01MIX"})
	var er editResult
	if err := json.Unmarshal(r.Data, &er); isErr || err != nil || er.Draft == nil || er.Draft.ToolCallID != "toolu_01MIX" ||
		er.Draft.Author.SessionID != "ses_9" || er.Mix.Rev != 1 {
		t.Fatalf("mixes.edit through MCP %+v %v", r, err)
	}
	got := expectFrames(t, frames, 2)
	if got[0].data["type"] != "draft.created" || got[1].data["type"] != "presence.changed" {
		t.Fatalf("frames %+v", got)
	}
	actor, _ := got[0].data["actor"].(map[string]any)
	cause, _ := got[0].data["causedBy"].(map[string]any)
	if actor["kind"] != "agent" || actor["sessionId"] != "ses_9" || cause["toolCallId"] != "toolu_01MIX" {
		t.Errorf("draft.created attribution %+v %+v", actor, cause)
	}
	payload, _ := got[1].data["payload"].(map[string]any)
	if ps, _ := payload["presence"].([]any); len(ps) != 1 {
		t.Errorf("presence.changed %+v", payload)
	}
	r, isErr = mcptest.Call(t, cs, "drafts.accept", map[string]any{"id": er.Draft.ID, "ifMatch": `"1"`}, nil)
	if !isErr || r.Status != 403 || !strings.Contains(r.Help, "policy-denied") {
		t.Errorf("an agent accepted its draft: %+v", r)
	}
	// The person accepts it in the UI.
	e.ok(e.do("POST", "/api/drafts/"+er.Draft.ID+":accept", "", web(cookie, "Idempotency-Key", e.key(), "If-Match", `"1"`)...), 200, nil)
	got = expectFrames(t, frames, 3)
	if got[0].data["type"] != "mix.revised" || got[1].data["type"] != "draft.accepted" || got[2].data["type"] != "presence.changed" {
		t.Fatalf("accept frames %+v", got)
	}
	actor, _ = got[0].data["actor"].(map[string]any)
	cause, _ = got[0].data["causedBy"].(map[string]any)
	if actor["kind"] != "user" || cause["draftId"] != er.Draft.ID {
		t.Errorf("mix.revised attribution %+v %+v", actor, cause)
	}
}
