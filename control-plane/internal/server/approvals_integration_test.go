//go:build integration

package server

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/usunrise88/cadence/control-plane/internal/approvals"
	"github.com/usunrise88/cadence/control-plane/internal/audit"
	"github.com/usunrise88/cadence/control-plane/internal/auth"
	"github.com/usunrise88/cadence/control-plane/internal/events"
	"github.com/usunrise88/cadence/control-plane/internal/jobs"
)

type approvalView struct {
	ID        string     `json:"id"`
	State     string     `json:"state"`
	Scope     string     `json:"scope"`
	Operation string     `json:"operation"`
	ProjectID string     `json:"projectId"`
	Actor     auth.Actor `json:"actor"`
	Rule      string     `json:"rule"`
	Reason    string     `json:"reason"`
	Rev       int        `json:"rev"`
	Request   struct {
		Method  string            `json:"method"`
		Path    string            `json:"path"`
		Headers map[string]string `json:"headers"`
	} `json:"request"`
	DecidedBy *auth.Actor `json:"decidedBy"`
	Decision  *struct {
		Grant   string `json:"grant"`
		Expired bool   `json:"expired"`
	} `json:"decision"`
	Result *struct {
		Status    int             `json:"status"`
		CommandID string          `json:"commandId"`
		Body      json.RawMessage `json:"body"`
	} `json:"result"`
}

type accepted struct {
	ApprovalID string `json:"approvalId"`
}

type auditView struct {
	Items []struct {
		Operation string     `json:"operation"`
		Actor     auth.Actor `json:"actor"`
		Outcome   string     `json:"outcome"`
		Status    int        `json:"status"`
		Rule      string     `json:"rule"`
		Preset    string     `json:"preset"`
		ProjectID string     `json:"projectId"`
		CausedBy  *struct {
			CommandID  string `json:"commandId"`
			ToolCallID string `json:"toolCallId"`
			ApprovalID string `json:"approvalId"`
		} `json:"causedBy"`
	} `json:"items"`
	Next string `json:"next"`
}

// gateArchive has the agent archive a project, which the default preset gates, and returns the approval id.
func (e *env) gateArchive(slug, key string, rev int, hdr ...string) string {
	e.t.Helper()
	hdr = append([]string{"Idempotency-Key", key, "If-Match", strconv.Quote(strconv.Itoa(rev))}, hdr...)
	var a accepted
	resp := e.ok(e.agent("POST", "/api/projects/"+slug+":archive", "", hdr...), 202, &a)
	if !strings.HasPrefix(a.ApprovalID, "apr_") || resp.Header.Get("Cadence-Approval-Id") != a.ApprovalID {
		e.t.Fatalf("202 body %+v, header %q", a, resp.Header.Get("Cadence-Approval-Id"))
	}
	return a.ApprovalID
}

func (e *env) approval(id string) approvalView {
	e.t.Helper()
	var a approvalView
	resp := e.ok(e.do("GET", "/api/approvals/"+id, ""), 200, &a)
	if resp.Header.Get("ETag") != strconv.Quote(strconv.Itoa(a.Rev)) {
		e.t.Fatalf("approval ETag %q, rev %d", resp.Header.Get("ETag"), a.Rev)
	}
	return a
}

func (e *env) events(topics string) []events.Record {
	e.t.Helper()
	var list struct{ Items []events.Record }
	e.ok(e.do("GET", "/api/events?after=0&limit=1000&topics="+topics, ""), 200, &list)
	return list.Items
}

func (e *env) audit(query string) auditView {
	e.t.Helper()
	var v auditView
	e.ok(e.do("GET", "/api/audit?"+query, ""), 200, &v)
	return v
}

func TestGatedCommandApproveReplays(t *testing.T) {
	e := start(t)
	e.newProject("demo")
	key := e.key()
	id := e.gateArchive("demo", key, 1, "Cadence-Tool-Call-Id", "toolu_1",
		"Authorization", "Bearer cst_secret", "Cookie", "cadence_session=abc")

	// Nothing ran; the approval holds the request without credentials.
	var p project
	e.ok(e.do("GET", "/api/projects/demo", ""), 200, &p)
	if p.ArchivedAt != "" || p.Rev != 1 {
		t.Fatalf("gated command ran: %+v", p)
	}
	a := e.approval(id)
	if a.State != "pending" || a.Operation != "projects.archive" || a.Rule != "archive-project" || a.Scope != "project" ||
		a.ProjectID != p.ID || a.Actor.ID != testAgent.ID || a.Request.Path != "/api/projects/demo:archive" {
		t.Fatalf("approval %+v", a)
	}
	for _, h := range []string{"Authorization", "Cookie"} {
		if _, ok := a.Request.Headers[h]; ok {
			t.Errorf("stored request keeps %s", h)
		}
	}
	if a.Request.Headers["Idempotency-Key"] != key || a.Request.Headers["Cadence-Tool-Call-Id"] != "toolu_1" {
		t.Errorf("stored headers %v", a.Request.Headers)
	}
	req := e.events("approvals")
	if len(req) != 1 || req[0].Type != approvals.EventRequested || req[0].Actor.ID != testAgent.ID ||
		req[0].CausedBy == nil || req[0].CausedBy.ToolCallID != "toolu_1" || req[0].ProjectID != p.ID {
		t.Fatalf("approvals topic %+v", req)
	}

	// A repeat with the same key answers the stored 202.
	var again accepted
	resp := e.ok(e.agent("POST", "/api/projects/demo:archive", "", "Idempotency-Key", key, "If-Match", `"1"`,
		"Cadence-Tool-Call-Id", "toolu_1"), 202, &again)
	if again.ApprovalID != id || resp.Header.Get("Idempotent-Replayed") != "true" {
		t.Fatalf("repeat answered %+v", again)
	}

	// An agent cannot decide it.
	expectProblem(t, e.agent("POST", "/api/approvals/"+id+":approve", "{}", "Idempotency-Key", e.key(), "If-Match", `"1"`),
		403, "policy-denied")
	expectProblem(t, e.do("POST", "/api/approvals/"+id+":approve", "{}", "Idempotency-Key", e.key(), "If-Match", `"7"`),
		412, "precondition-failed")

	// A dry run of the approval replays nothing.
	e.ok(e.do("POST", "/api/approvals/"+id+":approve?dryRun=true", "{}", "Idempotency-Key", e.key(), "If-Match", `"1"`), 200, nil)
	if e.approval(id).State != "pending" {
		t.Fatal("dry run decided the approval")
	}

	// The admin approves: the request runs as the agent, attributed to the approval.
	var decided approvalView
	e.ok(e.do("POST", "/api/approvals/"+id+":approve", `{"note":"ok"}`, "Idempotency-Key", e.key(), "If-Match", `"1"`),
		200, &decided)
	if decided.State != "approved" || decided.Rev != 2 || decided.DecidedBy == nil || decided.DecidedBy.ID != "usr_admin" ||
		decided.Decision == nil || decided.Decision.Grant != "once" || decided.Result == nil || decided.Result.Status != 200 {
		t.Fatalf("decided %+v", decided)
	}
	var replayed project
	if err := json.Unmarshal(decided.Result.Body, &replayed); err != nil || replayed.ArchivedAt == "" || replayed.Rev != 2 {
		t.Fatalf("replay result %s (%v)", decided.Result.Body, err)
	}
	archived := e.events("entity.project." + p.ID)
	last := archived[len(archived)-1]
	if last.Type != "project.archived" || last.Actor.ID != testAgent.ID || last.CausedBy == nil ||
		last.CausedBy.ApprovalID != id || last.CausedBy.ToolCallID != "toolu_1" ||
		last.CausedBy.CommandID != decided.Result.CommandID {
		t.Fatalf("archive event %+v cause %+v", last, last.CausedBy)
	}
	if got := e.events("approvals"); len(got) != 2 || got[1].Type != approvals.EventDecided || got[1].Actor.ID != "usr_admin" {
		t.Fatalf("approvals topic after decision %+v", got)
	}

	// The original key now answers the real result; the approval cannot be decided twice.
	var p2 project
	e.ok(e.agent("POST", "/api/projects/demo:archive", "", "Idempotency-Key", key, "If-Match", `"1"`,
		"Cadence-Tool-Call-Id", "toolu_1"), 200, &p2)
	if p2.ArchivedAt == "" {
		t.Fatalf("original key after approval answered %+v", p2)
	}
	expectProblem(t, e.do("POST", "/api/approvals/"+id+":approve", "{}", "Idempotency-Key", e.key(), "If-Match", `"2"`),
		409, "conflict")

	// "Once" does not carry over: the same operation asks again.
	e.newProject("second")
	e.gateArchive("second", e.key(), 1)

	// The audit log has every step with its cause.
	au := e.audit("operation=projects.archive")
	if len(au.Items) != 3 {
		t.Fatalf("audit %+v", au.Items)
	}
	ran := au.Items[1] // newest first: second project's approval, the replay, the first approval
	if ran.Outcome != "ok" || ran.Actor.ID != testAgent.ID || ran.CausedBy == nil || ran.CausedBy.ApprovalID != id ||
		ran.CausedBy.ToolCallID != "toolu_1" || ran.ProjectID != p.ID || ran.Rule != "approval" {
		t.Fatalf("audit of the replay %+v", ran)
	}
	if asked := au.Items[2]; asked.Outcome != "approval" || asked.Status != 202 || asked.Rule != "archive-project" ||
		asked.Preset != "guardrails-default" {
		t.Fatalf("audit of the request %+v", asked)
	}
	denied := e.audit("operation=approvals.approve&actor=" + testAgent.ID)
	if len(denied.Items) != 1 || denied.Items[0].Outcome != "denied" || denied.Items[0].Status != 403 ||
		denied.Items[0].Rule != "approvals-are-for-people" {
		t.Fatalf("audit of the denied approve %+v", denied.Items)
	}
}

func TestDenyAndList(t *testing.T) {
	e := start(t)
	e.newProject("demo")
	e.newProject("other")
	first := e.gateArchive("demo", e.key(), 1)
	second := e.gateArchive("other", e.key(), 1)

	var denied approvalView
	e.ok(e.do("POST", "/api/approvals/"+first+":deny", `{"note":"not now"}`, "Idempotency-Key", e.key(), "If-Match", `"1"`),
		200, &denied)
	if denied.State != "denied" || denied.Result != nil || denied.Decision == nil {
		t.Fatalf("denied %+v", denied)
	}
	var p project
	e.ok(e.do("GET", "/api/projects/demo", ""), 200, &p)
	if p.ArchivedAt != "" {
		t.Fatal("a denied request ran")
	}

	var list struct{ Items []approvalView }
	e.ok(e.do("GET", "/api/approvals", ""), 200, &list)
	if len(list.Items) != 2 || list.Items[0].ID != second || list.Items[1].ID != first {
		t.Fatalf("pending first: %+v", list.Items)
	}
	e.ok(e.do("GET", "/api/approvals?state=decided", ""), 200, &list)
	if len(list.Items) != 1 || list.Items[0].ID != first {
		t.Fatalf("decided: %+v", list.Items)
	}
	e.ok(e.do("GET", "/api/approvals?project=other", ""), 200, &list)
	if len(list.Items) != 1 || list.Items[0].ID != second {
		t.Fatalf("by project: %+v", list.Items)
	}
	expectProblem(t, e.do("GET", "/api/approvals/apr_nope", ""), 404, "not-found")

	// A dry run of a gated command runs as a dry run and says a person would decide.
	resp := e.ok(e.agent("POST", "/api/projects/other:archive?dryRun=true", "", "Idempotency-Key", e.key(), "If-Match", `"1"`),
		200, nil)
	if !strings.HasPrefix(resp.Header.Get("Cadence-Policy"), "approval; rule=archive-project") {
		t.Fatalf("Cadence-Policy = %q", resp.Header.Get("Cadence-Policy"))
	}
	if n := e.count("SELECT count(*) FROM approvals"); n != 2 {
		t.Fatalf("dry run stored an approval (%d)", n)
	}
	// People are not gated by the fixture rule.
	e.ok(e.do("POST", "/api/projects/other:archive", "", "Idempotency-Key", e.key(), "If-Match", `"1"`), 200, nil)
}

func TestApprovalExpires(t *testing.T) {
	e := start(t)
	e.newProject("demo")
	id := e.gateArchive("demo", e.key(), 1)

	ctx := context.Background()
	if n, err := approvals.Sweep(ctx, e.pool, time.Now().Add(time.Hour)); err != nil || n != 0 {
		t.Fatalf("early sweep expired %d (%v)", n, err)
	}
	if n, err := approvals.Sweep(ctx, e.pool, time.Now().Add(approvals.TTL+time.Minute)); err != nil || n != 1 {
		t.Fatalf("sweep expired %d (%v)", n, err)
	}
	a := e.approval(id)
	if a.State != "denied" || a.Decision == nil || !a.Decision.Expired || a.DecidedBy == nil || a.DecidedBy.ID != "cadence" {
		t.Fatalf("expired approval %+v", a)
	}
	if got := e.events("approvals"); len(got) != 2 || got[1].Type != approvals.EventDecided {
		t.Fatalf("approvals topic %+v", got)
	}
	expectProblem(t, e.do("POST", "/api/approvals/"+id+":approve", "{}", "Idempotency-Key", e.key(), "If-Match", `"2"`),
		409, "conflict")
	if au := e.audit("operation=approvals.deny"); len(au.Items) != 1 || au.Items[0].Outcome != "expired" {
		t.Fatalf("audit %+v", au.Items)
	}

	// Retention: a year later every entry goes.
	total := e.count("SELECT count(*) FROM audit_log")
	if n, err := audit.Prune(ctx, e.pool, time.Now()); err != nil || n != 0 {
		t.Fatalf("prune now removed %d (%v)", n, err)
	}
	if n, err := audit.Prune(ctx, e.pool, time.Now().Add(audit.Retention+48*time.Hour)); err != nil || int(n) != total {
		t.Fatalf("prune a year later removed %d of %d (%v)", n, total, err)
	}
}

func TestSessionScopedApproval(t *testing.T) {
	e := start(t)
	e.newProject("demo")
	e.newProject("other")
	id := e.gateArchive("demo", e.key(), 1)
	var a approvalView
	e.ok(e.do("POST", "/api/approvals/"+id+":approve", `{"grant":"session"}`, "Idempotency-Key", e.key(),
		"If-Match", `"1"`), 200, &a)
	if a.Decision == nil || a.Decision.Grant != "session" {
		t.Fatalf("approved %+v", a)
	}
	// The same operation on the same path now runs without asking: the project is already archived, so the
	// command itself answers 409 (it ran), not 202.
	expectProblem(t, e.agent("POST", "/api/projects/demo:archive", "", "Idempotency-Key", e.key(), "If-Match", `"2"`),
		409, "conflict")
	if n := e.count("SELECT count(*) FROM approvals"); n != 1 {
		t.Fatalf("%d approvals, want 1", n)
	}
	au := e.audit("operation=projects.archive&limit=1")
	if it := au.Items[0]; it.Rule != "session-grant" || it.CausedBy == nil || it.CausedBy.ApprovalID != id || it.Status != 409 {
		t.Fatalf("audit of the granted attempt %+v", it)
	}
	if au.Next == "" {
		t.Fatal("paged audit has no next cursor")
	}
	older := e.audit("operation=projects.archive&limit=1&before=" + au.Next)
	if len(older.Items) != 1 || older.Items[0].Outcome != "ok" {
		t.Fatalf("second page %+v", older.Items)
	}
	// Another path still asks.
	e.gateArchive("other", e.key(), 1)
}

// enqueue starts a noop job as testAgent in project p, the way a command would.
func (e *env) enqueue(projectID string, args any) jobs.Job {
	e.t.Helper()
	ctx := auth.WithActor(context.Background(), testAgent)
	var j jobs.Job
	err := pgx.BeginFunc(ctx, e.pool, func(tx pgx.Tx) error {
		var drafts []events.Draft
		var err error
		j, drafts, err = e.jobs.Enqueue(ctx, tx, jobs.Spec{Kind: jobs.KindNoop, ProjectID: projectID, Args: args})
		if err != nil {
			return err
		}
		return events.Append(ctx, tx, testAgent, &events.CausedBy{CommandID: "cmd_test"}, drafts)
	})
	if err != nil {
		e.t.Fatal(err)
	}
	return j
}

type jobView struct {
	ID                string          `json:"id"`
	Kind              string          `json:"kind"`
	ProjectID         string          `json:"projectId"`
	State             string          `json:"state"`
	Progress          float64         `json:"progress"`
	Message           string          `json:"message"`
	Result            json.RawMessage `json:"result"`
	Error             string          `json:"error"`
	Rev               int             `json:"rev"`
	Actor             auth.Actor      `json:"actor"`
	CancelRequestedAt string          `json:"cancelRequestedAt"`
}

func TestJobsLifecycle(t *testing.T) {
	e := start(t)
	p := e.newProject("demo")

	// done
	j := e.enqueue(p.ID, map[string]string{"hello": "world"})
	if !strings.HasPrefix(j.ID, "job_") || j.State != jobs.StateQueued {
		t.Fatalf("enqueued %+v", j)
	}
	var v jobView
	e.ok(e.agent("GET", "/api/jobs/"+j.ID+":wait?timeout=20", ""), 200, &v)
	if v.State != jobs.StateDone || v.Progress != 1 || !strings.Contains(string(v.Result), "world") ||
		v.Actor.ID != testAgent.ID || v.Kind != "noop" {
		t.Fatalf("waited %+v", v)
	}
	types := []string{}
	for _, ev := range e.events(jobs.Topic(j.ID)) {
		var payload struct{ Job jobView }
		_ = json.Unmarshal(ev.Payload, &payload)
		types = append(types, ev.Type+":"+payload.Job.State)
		if ev.ProjectID != p.ID {
			t.Errorf("job event without project: %+v", ev)
		}
	}
	want := "job.state_changed:queued job.state_changed:running job.progress:running job.state_changed:done"
	if got := strings.Join(types, " "); got != want {
		t.Fatalf("job events %q, want %q", got, want)
	}
	expectProblem(t, e.do("POST", "/api/jobs/"+j.ID+":cancel", "", "Idempotency-Key", e.key(),
		"If-Match", strconv.Quote(strconv.Itoa(v.Rev))), 409, "conflict")

	// failed
	f := e.enqueue(p.ID, map[string]string{"fail": "boom"})
	e.ok(e.agent("GET", "/api/jobs/"+f.ID+":wait?timeout=20", ""), 200, &v)
	if v.State != jobs.StateFailed || v.Error != "boom" {
		t.Fatalf("failed job %+v", v)
	}

	// wait times out on a running job; cancel stops it
	c := e.enqueue(p.ID, map[string]int{"sleepMs": 60000})
	deadline := time.Now().Add(20 * time.Second)
	for {
		resp := e.ok(e.do("GET", "/api/jobs/"+c.ID, ""), 200, &v)
		if resp.Header.Get("ETag") != strconv.Quote(strconv.Itoa(v.Rev)) {
			t.Fatalf("job ETag %q rev %d", resp.Header.Get("ETag"), v.Rev)
		}
		if v.State == jobs.StateRunning && v.Progress == 0.5 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("job never ran: %+v", v)
		}
		time.Sleep(50 * time.Millisecond)
	}
	began := time.Now()
	e.ok(e.agent("GET", "/api/jobs/"+c.ID+":wait?timeout=1", ""), 200, &v)
	if v.State != jobs.StateRunning || time.Since(began) < 900*time.Millisecond {
		t.Fatalf("wait returned %s after %s", v.State, time.Since(began))
	}
	expectProblem(t, e.agent("GET", "/api/jobs/"+c.ID+":wait?timeout=61", ""), 422, "validation-failed")

	var cancelled jobView
	e.ok(e.agent("POST", "/api/jobs/"+c.ID+":cancel", "", "Idempotency-Key", e.key(),
		"If-Match", strconv.Quote(strconv.Itoa(v.Rev))), 200, &cancelled)
	if cancelled.CancelRequestedAt == "" {
		t.Fatalf("cancel answered %+v", cancelled)
	}
	e.ok(e.agent("GET", "/api/jobs/"+c.ID+":wait?timeout=20", ""), 200, &v)
	if v.State != jobs.StateCancelled {
		t.Fatalf("cancelled job %+v", v)
	}

	var list struct{ Items []jobView }
	e.ok(e.do("GET", "/api/projects/demo/jobs", ""), 200, &list)
	if len(list.Items) != 3 || list.Items[0].ID != c.ID {
		t.Fatalf("jobs.list %+v", list.Items)
	}
	e.ok(e.do("GET", "/api/projects/demo/jobs?state=failed", ""), 200, &list)
	if len(list.Items) != 1 || list.Items[0].ID != f.ID {
		t.Fatalf("jobs.list failed %+v", list.Items)
	}
	expectProblem(t, e.do("GET", "/api/jobs/job_nope", ""), 404, "not-found")
	if au := e.audit("operation=jobs.cancel"); len(au.Items) != 2 {
		t.Fatalf("jobs.cancel audit %+v", au.Items)
	}
}
