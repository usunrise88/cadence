//go:build integration

package server

import (
	"context"
	"math"
	"testing"
	"time"

	"github.com/usunrise88/cadence/control-plane/internal/defaults"
	"github.com/usunrise88/cadence/control-plane/internal/policy"
	"github.com/usunrise88/cadence/control-plane/internal/runs"
)

// Work already queued counts against the budget: three runs started back to back must not each see the whole
// remainder (found on the test stand, 2026-10-01). A running step's live lease is metered, so only the rest of its
// estimate is committed; ended pipeline runs and steps without a card commit nothing.
func TestBudgetCountsCommittedWork(t *testing.T) {
	e := start(t)
	ctx := context.Background()
	e.newProject("budget")
	pid := e.projectID("budget")
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := e.pool.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`UPDATE projects SET budgets = jsonb_set(budgets, '{gpuHoursPerDay}', '1') WHERE id = $1`, pid)
	run := func(id, state string) {
		exec(`INSERT INTO pipeline_runs (id, project_id, pipeline, source, definition, actor, state)
			VALUES ($1, $2, 'train-stage', 'inline', '{}', '{"kind":"agent","id":"usr_admin","sessionId":"ses_x"}', $3)`, id, pid, state)
	}
	step := func(run, name, state, resources string, seconds *float64) {
		exec(`INSERT INTO pipeline_steps (id, pipeline_run_id, project_id, step, position, kind, kind_version, state, resources,
				estimate_seconds) VALUES ('pls_'||$1||$2, $1, $3, $2, 0, 'train_toy', '1', $4, $5::jsonb, $6)`,
			run, name, pid, state, resources, seconds)
	}
	half, quarter := 1800.0, 900.0
	run("plr_a", "running")
	step("plr_a", "train", "waiting", `{"gpu":true}`, &half) // waits for calibrate: 0.5 h committed
	step("plr_a", "import", "queued", `{}`, &quarter)        // no card: free
	step("plr_a", "eval", "queued", `{"gpu":true}`, nil)     // no estimate: the policy gates its command instead
	run("plr_b", "running")
	step("plr_b", "train", "queued", `{"gpu":true}`, &quarter) // 0.25 h
	run("plr_c", "failed")
	step("plr_c", "train", "waiting", `{"gpu":true}`, &half) // the run ended: nothing committed

	b, err := runs.ProjectBudgetOf(ctx, e.pool, defaults.Get(), pid, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(b.Committed-0.75) > 1e-6 || math.Abs(b.Remaining()-0.25) > 1e-6 {
		t.Fatalf("budget %+v: want 0.75 committed, 0.25 remaining", b)
	}
	m := runs.Meter{Pool: e.pool}
	if left, err := m.RemainingSessionGPUHours(ctx, "ses_x"); err != nil || left > defaults.Get().Budgets.AgentGPUHoursPerSession.Value-0.75+1e-6 {
		t.Fatalf("session remaining %v (%v): the committed work counts there too", left, err)
	}
	// A third run of 0.3 GPU-hours no longer fits.
	eng, err := policy.Embedded(m)
	if err != nil {
		t.Fatal(err)
	}
	d, err := eng.Decide(ctx, policy.Input{Actor: testAgent, Operation: "runs.new", VerbClass: "mutate", ProjectID: pid,
		Estimate: &policy.Estimate{GPUHours: 0.3}})
	if err != nil || d.Outcome != policy.Approval {
		t.Fatalf("decision %+v (%v): want approval", d, err)
	}
}
