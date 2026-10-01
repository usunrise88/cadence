//go:build integration

package server

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/usunrise88/cadence/control-plane/internal/pipelines/pipelinestest"
)

// Playbooks against the real control plane (R16): playbooks.list|get with the estimate, playbooks.run (dry run and
// real) starting a playbook session whose plan is the chain and whose first transcript entry is the estimate; the plan
// ticking from the session's own commands; a spending command refused without its dry run and let through after it;
// the reminder when a turn ends without progress; a denied approval stopping the playbook.

type planItem struct {
	ID, Title, Command, State, Note, EntityID, JobID string
	Spending                                         bool
}

type agentPlaybook struct {
	Name     string         `json:"name"`
	State    string         `json:"state"`
	Inputs   map[string]any `json:"inputs"`
	Plan     []planItem     `json:"plan"`
	DryRuns  []string       `json:"dryRuns"`
	Summary  string         `json:"summary"`
	Next     string         `json:"next"`
	Estimate struct {
		Basis    string `json:"basis"`
		GPUHours struct{ Value, Low, High float64 }
	} `json:"estimate"`
	Stop *struct{ On, When, Message string } `json:"stop"`
}

type playbookSession struct {
	sessionView
	Playbook *agentPlaybook `json:"playbook"`
}

func (h *hostEnv) playbookSession(id string) playbookSession {
	h.t.Helper()
	var s playbookSession
	h.ok(h.do("GET", "/api/agent-sessions/"+id, ""), 200, &s)
	if s.Playbook == nil {
		h.t.Fatalf("session %s has no playbook", id)
	}
	return s
}

func TestPlaybooks(t *testing.T) {
	h := startHost(t)
	// The fixture family and a trainable dataset, so the chain runs end to end on the in-process fake worker.
	if err := pipelinestest.RegisterTraining(t.Context(), h.pool); err != nil {
		t.Fatal(err)
	}
	trainingProject(t, h.env, "hebrew")

	// playbooks.list: five, the runnable one first, with project facts and the estimate.
	var list struct {
		Items []struct {
			Name, Title, VersionID, Unavailable string
			Runnable                            bool
			AvailableFrom                       int
			Inputs                              []map[string]any
			Chain                               []map[string]any
			Estimate                            *struct {
				Basis    string
				GPUHours struct{ Value, Low, High float64 }
				Steps    []map[string]any
			}
		}
	}
	h.ok(h.do("GET", "/api/playbooks?project=hebrew", ""), 200, &list)
	if len(list.Items) != 5 || list.Items[0].Name != "finetune-from-dataset" || !list.Items[0].Runnable ||
		list.Items[1].Runnable || list.Items[1].Unavailable == "" || !strings.HasPrefix(list.Items[0].VersionID, "ver_") {
		t.Fatalf("playbooks.list %+v", list.Items)
	}
	ft := list.Items[0]
	// 3 000 steps × 1.0 s (table) + the calibration hint 0.1 GPU-hours; both ±50%.
	if ft.Estimate == nil || ft.Estimate.Basis != "mixed" || ft.Estimate.GPUHours.Value != 0.683 || ft.Estimate.GPUHours.High != 0.85 {
		t.Fatalf("estimate %+v", ft.Estimate)
	}
	for _, in := range ft.Inputs {
		switch in["name"] {
		case "base":
			if !strings.HasPrefix(in["default"].(string), "ver_") {
				t.Errorf("base input without the project's base model: %v", in)
			}
		case "steps":
			if in["default"] != float64(3000) || in["max"] != float64(200000) {
				t.Errorf("steps input %v", in)
			}
		}
	}

	// playbooks.get: the ETag is the version playbooks.run takes as If-Match.
	resp := h.ok(h.do("GET", "/api/playbooks/finetune-from-dataset", ""), 200, nil)
	etag := resp.Header.Get("ETag")
	if etag == "" {
		t.Fatal("playbooks.get without an ETag")
	}
	expectProblem(t, h.do("GET", "/api/playbooks/nope", ""), 404, "not-found")

	run := func(body string, dry bool, hdr ...string) *http.Response {
		path := "/api/projects/hebrew/playbooks/finetune-from-dataset:run"
		if dry {
			path += "?dryRun=true"
		}
		return h.do("POST", path, body, append([]string{"Idempotency-Key", h.key(), "If-Match", etag}, hdr...)...)
	}
	body := `{"inputs":{"dataset":["dataset/fleurs-he-smoke"],"steps":500}}`
	expectProblem(t, run(`{"inputs":{"steps":500}}`, true), 422, "validation-failed")
	expectProblem(t, run(`{"inputs":{"dataset":"dataset/fleurs-he-smoke","steps":0}}`, true), 422, "validation-failed")
	expectProblem(t, h.do("POST", "/api/projects/hebrew/playbooks/finetune-from-dataset:run", body, "Idempotency-Key", h.key(), "If-Match", `"old"`), 412, "precondition-failed")
	expectProblem(t, h.do("POST", "/api/projects/hebrew/playbooks/adapt-new-language:run", `{"inputs":{"source":"x"}}`,
		"Idempotency-Key", h.key(), "If-Match", "*"), 409, "playbook-unavailable")

	// Dry run: inputs, estimate, plan and prompt; no session.
	var dry struct {
		Inputs   map[string]any
		Estimate struct{ GPUHours struct{ Value float64 } }
		Plan     []planItem
		Prompt   string
		Session  *sessionView
	}
	before := h.count("SELECT count(*) FROM agent_sessions")
	h.ok(run(body, true), 200, &dry)
	if dry.Session != nil || h.count("SELECT count(*) FROM agent_sessions") != before || len(dry.Plan) != 7 ||
		dry.Plan[5].State != "skipped" || dry.Estimate.GPUHours.Value != 0.197 || !strings.Contains(dry.Prompt, "Training steps: 500") ||
		!strings.Contains(dry.Prompt, "none (the project has not adopted dataset/replay-base)") {
		t.Fatalf("dry run %+v", dry)
	}

	// The real run: a session of kind playbook on its own branch, the plan, the estimate first in the transcript.
	var res struct {
		Session playbookSession
	}
	h.ok(run(body, false), 201, &res)
	s := res.Session
	if s.Kind != "playbook" || s.State != "created" || s.Branch != "session/"+s.ID || s.Playbook == nil ||
		s.Playbook.State != "running" || len(s.Playbook.Plan) != 7 || s.Playbook.Plan[0].State != "pending" {
		t.Fatalf("playbook session %+v", s)
	}
	msgs := h.transcript(s.ID)
	if len(msgs) < 2 || msgs[0].Kind != "notice" || !strings.Contains(msgs[0].Text, "estimate 0.20 GPU-hours") ||
		msgs[1].Kind != "user_message" || !strings.Contains(msgs[1].Text, "Fine-tune from a dataset version") {
		t.Fatalf("transcript %+v", msgs)
	}
	// Agents never start playbook sessions; agentSessions.new never makes one.
	expectProblem(t, h.do("POST", "/api/projects/hebrew/agent-sessions", `{"kind":"playbook","prompt":"x"}`, "Idempotency-Key", h.key()), 422, "validation-failed")

	// The host takes it; the agent works the chain with its session token.
	w := h.claim("host-a")
	if len(w.Start) != 1 || w.Start[0].Session.Kind != "playbook" {
		t.Fatalf("claim %+v", w)
	}
	token := w.Start[0].Token
	h.report(s.ID, map[string]any{"hostId": "host-a", "state": map[string]any{"state": "running"}})
	agent := func(method, path, body string, hdr ...string) *http.Response {
		return h.send(h.url, method, path, body, append([]string{"Authorization", "Bearer " + token}, hdr...)...)
	}
	expectProblem(t, agent("POST", "/api/projects/hebrew/playbooks/finetune-from-dataset:run", body, "Idempotency-Key", h.key(), "If-Match", "*"), 403, "policy-denied")
	h.report(s.ID, map[string]any{"hostId": "host-a", "state": map[string]any{"state": "running", "busy": true, "turn": 1}})

	// A later step's operation does not tick; the mix does.
	h.ok(agent("GET", "/api/projects/hebrew/checkpoints", ""), 200, nil)
	if pb := h.playbookSession(s.ID).Playbook; pb.Plan[0].State != "pending" || pb.Plan[4].State != "pending" {
		t.Fatalf("a later step ticked: %+v", pb.Plan)
	}
	var mix mixView
	h.ok(agent("POST", "/api/projects/hebrew/mixes", `{"name":"pb-mix","groups":[{"name":"he","datasets":["dataset/fx-he"]}]}`, "Idempotency-Key", h.key()), 201, &mix)
	pb := h.playbookSession(s.ID).Playbook
	if pb.Plan[0].State != "done" || pb.Plan[0].EntityID != mix.ID || pb.Plan[1].State != "pending" {
		t.Fatalf("after mixes.new: %+v", pb.Plan[:2])
	}
	var n int
	if err := h.pool.QueryRow(t.Context(), `SELECT count(*) FROM events WHERE topic = $1 AND type = 'agent_session.changed'
		AND payload->'session'->'playbook'->'plan'->0->>'state' = 'done'`, "agent.session."+s.ID).Scan(&n); err != nil || n == 0 {
		t.Fatalf("the tick is not an event on the session's topic: %d %v", n, err)
	}

	// The turn ends without progress on the current step: the agent is reminded of it, at most twice.
	for turn := 1; turn <= 3; turn++ {
		h.report(s.ID, map[string]any{"hostId": "host-a", "state": map[string]any{"state": "running", "busy": true, "turn": turn}})
		h.report(s.ID, map[string]any{"hostId": "host-a", "state": map[string]any{"state": "running", "busy": false, "turn": turn}})
	}
	reminders := 0
	for _, m := range h.transcript(s.ID) {
		if m.Kind == "notice" && strings.Contains(m.Text, "is not finished: the next step is step 2 of 7") {
			reminders++
			if m.Delivery != "pending" && m.Delivery != "delivered" {
				t.Errorf("the reminder is not for the agent: %+v", m)
			}
		}
	}
	if reminders != 2 {
		t.Fatalf("%d reminders, want 2", reminders)
	}
	h.report(s.ID, map[string]any{"hostId": "host-a", "state": map[string]any{"state": "running", "busy": true, "turn": 4}})

	// Calibrate: refused without its dry run; the dry run marks the step running; the real call ticks it.
	calBody := `{"baseModel":"` + pipelinestest.BaseModel + `","mix":"` + mix.ID + `"}`
	expectProblem(t, agent("POST", "/api/projects/hebrew/runs:calibrate", calBody, "Idempotency-Key", h.key()), 409, "playbook-dry-run-required")
	h.ok(agent("POST", "/api/projects/hebrew/runs:calibrate?dryRun=true", calBody, "Idempotency-Key", h.key()), 200, nil)
	if pb = h.playbookSession(s.ID).Playbook; pb.Plan[1].State != "running" || len(pb.DryRuns) != 1 || pb.DryRuns[0] != "runs.calibrate" {
		t.Fatalf("after the calibration dry run: %+v %v", pb.Plan[1], pb.DryRuns)
	}
	var cal struct{ PipelineRun struct{ ID string } }
	h.ok(agent("POST", "/api/projects/hebrew/runs:calibrate", calBody, "Idempotency-Key", h.key()), 201, &cal)
	if pb = h.playbookSession(s.ID).Playbook; pb.Plan[1].State != "done" || pb.Plan[1].EntityID != cal.PipelineRun.ID || len(pb.DryRuns) != 0 {
		t.Fatalf("after the calibration: %+v %v", pb.Plan[1], pb.DryRuns)
	}
	h.waitPipelineRun(cal.PipelineRun.ID, "done")

	// Train: the calibration's dry run does not count for runs.new; its own does, once.
	runBody := calBody
	expectProblem(t, agent("POST", "/api/projects/hebrew/runs", runBody, "Idempotency-Key", h.key()), 409, "playbook-dry-run-required")
	h.ok(agent("POST", "/api/projects/hebrew/runs?dryRun=true", runBody, "Idempotency-Key", h.key()), 200, nil)
	var trained runView
	h.ok(agent("POST", "/api/projects/hebrew/runs", runBody, "Idempotency-Key", h.key()), 201, &trained)
	expectProblem(t, agent("POST", "/api/projects/hebrew/runs", runBody, "Idempotency-Key", h.key()), 409, "playbook-dry-run-required")
	if pb = h.playbookSession(s.ID).Playbook; pb.Plan[2].State != "done" || pb.Plan[2].EntityID != trained.ID {
		t.Fatalf("after runs.new: %+v", pb.Plan[2])
	}
	// A person's run is not a playbook session's: no dry-run rule for it.
	var other runView
	h.ok(h.do("POST", "/api/projects/hebrew/runs", runBody, "Idempotency-Key", h.key()), 201, &other)

	// Wait: another run does not count; the run reaching done ticks the step; the checkpoints end the chain.
	h.waitRun(other.ID, "done")
	h.ok(agent("GET", "/api/runs/"+other.ID, ""), 200, nil)
	if pb = h.playbookSession(s.ID).Playbook; pb.Plan[3].State == "done" {
		t.Fatal("another run ticked the wait")
	}
	h.waitRun(trained.ID, "done")
	h.ok(agent("GET", "/api/runs/"+trained.ID, ""), 200, nil)
	h.ok(agent("GET", "/api/projects/hebrew/checkpoints?run="+trained.ID, ""), 200, nil)
	got := h.playbookSession(s.ID)
	pb = got.Playbook
	if pb.Plan[3].State != "done" || pb.Plan[4].State != "done" || pb.State != "done" || !strings.Contains(pb.Summary, "complete: 5 step(s) done") {
		b, _ := json.Marshal(pb)
		t.Fatalf("after the chain: %s", b)
	}
	// The turn ends: the session ends with it.
	h.report(s.ID, map[string]any{"hostId": "host-a", "state": map[string]any{"state": "running", "busy": false, "turn": 4}})
	if got = h.playbookSession(s.ID); got.PendingControl != "end" {
		t.Fatalf("the finished playbook's session goes on: %+v", got.sessionView)
	}
}

func TestPlaybookStopsOnDeniedApproval(t *testing.T) {
	h := startHost(t)
	startSeededCompute(t, h.env)
	h.newProject("hebrew")
	var res struct{ Session playbookSession }
	h.ok(h.do("POST", "/api/projects/hebrew/playbooks/finetune-from-dataset:run",
		`{"inputs":{"dataset":"dataset/fleurs-he-smoke"}}`, "Idempotency-Key", h.key(), "If-Match", "*"), 201, &res)
	s := res.Session
	w := h.claim("host-a")
	token := w.Start[0].Token
	h.report(s.ID, map[string]any{"hostId": "host-a", "state": map[string]any{"state": "running"}})
	// A gated command of the session (projects.archive waits for a person), then denied.
	var p struct{ Rev int }
	h.ok(h.do("GET", "/api/projects/hebrew", ""), 200, &p)
	var acc struct{ ApprovalID string }
	h.ok(h.send(h.url, "POST", "/api/projects/hebrew:archive", "", "Authorization", "Bearer "+token, "Idempotency-Key", h.key(),
		"If-Match", ifMatch(p.Rev)), 202, &acc)
	var a struct{ Rev int }
	h.ok(h.do("GET", "/api/approvals/"+acc.ApprovalID, ""), 200, &a)
	h.ok(h.do("POST", "/api/approvals/"+acc.ApprovalID+":deny", `{"note":"not now"}`, "Idempotency-Key", h.key(), "If-Match", ifMatch(a.Rev)), 200, nil)
	got := h.playbookSession(s.ID)
	if got.Playbook.State != "stopped" || got.Playbook.Stop == nil || got.Playbook.Stop.On != "approval" ||
		!strings.Contains(got.Playbook.Summary, "stopped (approval denied)") || got.Playbook.Next == "" {
		b, _ := json.Marshal(got.Playbook)
		t.Fatalf("after the denial: %s", b)
	}
	// The session ends (no turn runs), and a spending command is refused from now on.
	if got.PendingControl != "end" && got.State != "done" {
		t.Fatalf("the stopped playbook's session goes on: %+v", got.sessionView)
	}
	var sawEnd bool
	for _, m := range h.transcript(s.ID) {
		sawEnd = sawEnd || (m.Kind == "notice" && strings.Contains(m.Text, "Next: "))
	}
	if !sawEnd {
		t.Fatal("no summary notice in the transcript")
	}
	expectProblem(t, h.send(h.url, "POST", "/api/projects/hebrew/runs?dryRun=true", `{}`, "Authorization", "Bearer "+token, "Idempotency-Key", h.key()), 409, "playbook-stopped")
}
