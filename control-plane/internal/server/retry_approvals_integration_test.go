//go:build integration

package server

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"strings"
	"testing"

	"github.com/usunrise88/cadence/control-plane/internal/pipelines/pipelinestest"
	"github.com/usunrise88/cadence/control-plane/internal/steps"
)

// A retry of a pipeline run a person approved inherits the approval on the same UTC day (owner decision of
// 2026-10-04): with an unknown estimate any retry does, with a known one while the spend stays within it; a retry
// on another day asks again, and an approved retry becomes the run's approval.
func TestRetryInheritsTheRunsApproval(t *testing.T) {
	e := start(t)
	ctx := context.Background()
	if err := pipelinestest.RegisterKinds(ctx, e.pool); err != nil {
		t.Fatal(err)
	}
	// tally@2: the GPU fixture kind without a declared estimate, so its cost is unknown and an agent's run waits.
	unknown := maps.Clone(pipelinestest.Fixtures[1])
	unknown["version"] = "2"
	delete(unknown, "estimateSeconds")
	if err := pipelinestest.Register(ctx, e.pool, unknown); err != nil {
		t.Fatal(err)
	}
	e.newProject("retry")
	e.commitPipeline("retry", "gpu-unknown", "name: gpu-unknown\ninputs: {text: text}\nsteps:\n  - {id: a, kind: tally@2, in: {text: $inputs.text}}\n")
	e.commitPipeline("retry", "gpu-known", "name: gpu-known\ninputs: {text: text}\nsteps:\n  - {id: b, kind: tally@1, in: {text: $inputs.text}}\n")

	// startApproved starts pipeline name as the agent, which waits for an approval; a person approves it and the run
	// fails on its first attempt (scripted).
	startApproved := func(name, step, text string) (runID, approvalID string) {
		t.Helper()
		e.leases.Script(step, pipelinestest.Action{Fail: &steps.StepError{Type: steps.ErrStep, Message: "boom"}})
		var gated struct{ ApprovalID string }
		e.ok(e.agent("POST", "/api/projects/retry/pipelines/"+name+":run", `{"inputs":{"text":`+e.putText(text)+`}}`,
			"Idempotency-Key", e.key(), "If-Match", "*"), 202, &gated)
		var decided approvalView
		e.ok(e.do("POST", "/api/approvals/"+gated.ApprovalID+":approve", "{}", "Idempotency-Key", e.key(), "If-Match", `"1"`), 200, &decided)
		if decided.State != "approved" || decided.Result == nil || decided.Result.Status != 201 {
			t.Fatalf("decided %+v", decided)
		}
		var r pipelineRunView
		if err := json.Unmarshal(decided.Result.Body, &r); err != nil {
			t.Fatal(err)
		}
		e.waitPipelineRun(r.ID, "failed")
		var recorded string
		if err := e.pool.QueryRow(ctx, "SELECT coalesce(approval_id, '') FROM pipeline_runs WHERE id = $1", r.ID).Scan(&recorded); err != nil ||
			recorded != gated.ApprovalID {
			t.Fatalf("run %s records approval %q (%v), want %s", r.ID, recorded, err, gated.ApprovalID)
		}
		return r.ID, gated.ApprovalID
	}
	revOf := func(runID string) string {
		t.Helper()
		var r pipelineRunView
		e.ok(e.do("GET", "/api/pipeline-runs/"+runID, ""), 200, &r)
		return fmt.Sprintf(`"%d"`, r.Rev)
	}
	retry := func(runID string) *pipelineRunView {
		t.Helper()
		resp := e.agent("POST", "/api/pipeline-runs/"+runID+":retry", "{}", "Idempotency-Key", e.key(), "If-Match", revOf(runID))
		if resp.StatusCode == 202 {
			_ = resp.Body.Close()
			return nil
		}
		var r pipelineRunView
		e.ok(resp, 200, &r)
		return &r
	}

	// Unknown estimate: the retry inherits the approval, and the audit row names it.
	runID, aprID := startApproved("gpu-unknown", "a", "one")
	if retry(runID) == nil {
		t.Fatal("a retry of an approved run on the same day asked for a new approval")
	}
	e.waitPipelineRun(runID, "done")
	if n := e.count("SELECT count(*) FROM audit_log WHERE operation = 'pipelineRuns.retry' AND rule = 'inherited-approval' AND approval_id = '" + aprID + "'"); n != 1 {
		t.Errorf("inherited retries in the audit log: %d", n)
	}

	// Another UTC day: the retry asks again; approving it makes it the run's approval.
	runID, aprID = startApproved("gpu-unknown", "a", "two")
	if _, err := e.pool.Exec(ctx, "UPDATE approvals SET decided_at = decided_at - interval '1 day' WHERE id = $1", aprID); err != nil {
		t.Fatal(err)
	}
	var gated struct{ ApprovalID string }
	e.ok(e.agent("POST", "/api/pipeline-runs/"+runID+":retry", "{}", "Idempotency-Key", e.key(), "If-Match", revOf(runID)), 202, &gated)
	if gated.ApprovalID == "" || gated.ApprovalID == aprID {
		t.Fatalf("retry on another day: %+v", gated)
	}
	e.leases.Script("a", pipelinestest.Action{}, pipelinestest.Action{Fail: &steps.StepError{Type: steps.ErrStep, Message: "again"}})
	e.ok(e.do("POST", "/api/approvals/"+gated.ApprovalID+":approve", "{}", "Idempotency-Key", e.key(), "If-Match", `"1"`), 200, nil)
	e.waitPipelineRun(runID, "failed")
	if n := e.count("SELECT count(*) FROM pipeline_runs WHERE id = '" + runID + "' AND approval_id = '" + gated.ApprovalID + "'"); n != 1 {
		t.Fatal("the approved retry did not become the run's approval")
	}
	if retry(runID) == nil {
		t.Fatal("a retry under the approved retry's approval asked again")
	}

	// Known estimate (0.5 GPU-hours, over a tiny budget): the retry fits what was approved (nothing metered yet).
	if _, err := e.pool.Exec(ctx, `UPDATE projects SET budgets = jsonb_set(budgets, '{gpuHoursPerDay}', '0.01') WHERE slug = 'retry'`); err != nil {
		t.Fatal(err)
	}
	runID, _ = startApproved("gpu-known", "b", "three")
	if retry(runID) == nil {
		t.Fatal("a retry within the approved estimate asked for a new approval")
	}
	// Once the approved hours are used up (a metered lease of the run), the next retry asks.
	runID, _ = startApproved("gpu-known", "b", "four")
	for _, sql := range []string{
		`INSERT INTO compute_hosts (id, name, cards) VALUES ('cmp_fx', 'fx-host', '[]')`,
		`INSERT INTO workers (id, host_id, runtime_name, runtime_version_id, runtime, step_kinds)
			SELECT 'wrk_fx', 'cmp_fx', 'test', v.id, '{}', ARRAY['tally@1'] FROM registry_versions v
			JOIN registry_collections c ON c.id = v.collection_id WHERE c.name = 'step-kind/tally' LIMIT 1`,
		`INSERT INTO step_jobs (job_id, spec, kind_ref, job_kind, gpu)
			SELECT ps.job_id, jsonb_build_object('pipelineRunId', ps.pipeline_run_id), 'tally@1', 'training', true
			FROM pipeline_steps ps WHERE ps.pipeline_run_id = $1 ON CONFLICT (job_id) DO NOTHING`,
		// 40 minutes on a card since the approval: more than the 0.5 GPU-hours approved, with the retry's estimate.
		`INSERT INTO leases (id, job_id, worker_id, host_id, card_index, job_kind, state, created_at, ended_at)
			SELECT 'lse_fx', ps.job_id, 'wrk_fx', 'cmp_fx', 0, 'training', 'released', now(), now() + interval '40 minutes'
			FROM pipeline_steps ps WHERE ps.pipeline_run_id = $1`,
	} {
		var args []any
		if strings.Contains(sql, "$1") {
			args = append(args, runID)
		}
		if _, err := e.pool.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	if retry(runID) != nil {
		t.Fatal("a retry beyond the approved estimate inherited the approval")
	}
}
