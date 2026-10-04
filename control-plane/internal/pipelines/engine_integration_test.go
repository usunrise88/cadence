//go:build integration

package pipelines_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"maps"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/usunrise88/cadence/control-plane/internal/artifacts"
	"github.com/usunrise88/cadence/control-plane/internal/auth"
	"github.com/usunrise88/cadence/control-plane/internal/cas"
	"github.com/usunrise88/cadence/control-plane/internal/events"
	"github.com/usunrise88/cadence/control-plane/internal/jobs"
	"github.com/usunrise88/cadence/control-plane/internal/pipelines"
	"github.com/usunrise88/cadence/control-plane/internal/pipelines/pipelinestest"
	"github.com/usunrise88/cadence/control-plane/internal/problems"
	"github.com/usunrise88/cadence/control-plane/internal/projects"
	"github.com/usunrise88/cadence/control-plane/internal/steps"
	"github.com/usunrise88/cadence/control-plane/internal/storage"
	"github.com/usunrise88/cadence/control-plane/internal/testdb"
	"github.com/usunrise88/cadence/control-plane/migrations"
)

func TestMain(m *testing.M) { testdb.Main(m) }

type rig struct {
	t       *testing.T
	pool    *pgxpool.Pool
	store   *cas.Store
	eng     *pipelines.Engine
	leases  *pipelinestest.Leases
	jobs    *jobs.Service
	project string
}

var person = auth.Actor{Kind: auth.KindUser, ID: "usr_admin", Name: "admin"}

func newRig(t *testing.T, hooks func(*steps.Hooks)) *rig {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	pool, err := storage.Open(ctx, testdb.New(t))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := storage.Migrate(ctx, pool, migrations.FS, jobs.MigrateRiver); err != nil {
		t.Fatal(err)
	}
	store, err := cas.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	h := &steps.Hooks{}
	if hooks != nil {
		hooks(h)
	}
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	leases := &pipelinestest.Leases{Pool: pool, CAS: store}
	eng := pipelines.New(pipelines.Options{Pool: pool, CAS: store, Hooks: h, Leases: leases, Log: quiet})
	leases.Engine = eng
	js := jobs.New(pool, quiet)
	js.FetchPollInterval = 100 * time.Millisecond
	eng.Register(js)
	if err := js.Start(ctx); err != nil {
		t.Fatal(err)
	}
	if err := pipelinestest.RegisterKinds(ctx, pool); err != nil {
		t.Fatal(err)
	}
	var pid string
	if err := pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		p, _, err := projects.Create(ctx, tx, projects.NewInput{Slug: "demo", Name: "demo", Budgets: projects.DefaultBudgets()})
		pid = p.ID
		return err
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		sctx, scancel := context.WithTimeout(context.Background(), 5*time.Second)
		_ = js.Stop(sctx)
		scancel()
		cancel()
		pool.Close()
	})
	return &rig{t: t, pool: pool, store: store, eng: eng, leases: leases, jobs: js, project: pid}
}

func (r *rig) put(text string) steps.ArtifactRef {
	r.t.Helper()
	h, err := r.store.PutBytes([]byte(text))
	if err != nil {
		r.t.Fatal(err)
	}
	return steps.ArtifactRef{Hash: h, Type: "text", Size: int64(len(text))}
}

func (r *rig) read(ref steps.ArtifactRef) string {
	r.t.Helper()
	f, err := r.store.Open(ref.Hash)
	if err != nil {
		r.t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	b, _ := io.ReadAll(f)
	return string(b)
}

// chain is echo (with a prefix) into tally.
func chain() *pipelines.Pipeline {
	return &pipelines.Pipeline{Name: "chain", Inputs: map[string]string{"text": "text"}, Steps: []pipelines.Step{
		{ID: "first", Kind: "echo@1", In: map[string]string{"text": "$inputs.text"}, Params: map[string]any{"prefix": "> "}},
		{ID: "count", Kind: "tally@1", In: map[string]string{"text": "first.text"}},
	}}
}

func (r *rig) input(text string) pipelines.StartInput {
	return pipelines.StartInput{ProjectID: r.project, Pipeline: chain(), Inputs: map[string]steps.ArtifactRef{"text": r.put(text)}, Actor: person}
}

func (r *rig) tx(fn func(ctx context.Context, tx pgx.Tx) ([]events.Draft, error)) error {
	ctx := auth.WithActor(context.Background(), person)
	return pgx.BeginFunc(ctx, r.pool, func(tx pgx.Tx) error {
		drafts, err := fn(ctx, tx)
		if err != nil {
			return err
		}
		return events.Append(ctx, tx, person, &events.CausedBy{CommandID: "cmd_test"}, drafts)
	})
}

func (r *rig) start(in pipelines.StartInput) pipelines.Run {
	r.t.Helper()
	var run pipelines.Run
	if err := r.tx(func(ctx context.Context, tx pgx.Tx) ([]events.Draft, error) {
		var drafts []events.Draft
		var err error
		run, drafts, err = r.eng.Start(ctx, tx, in)
		return drafts, err
	}); err != nil {
		r.t.Fatal(err)
	}
	return run
}

func (r *rig) wait(id, state string) pipelines.Run {
	r.t.Helper()
	run, err := r.eng.Wait(context.Background(), id, 20*time.Second, 50*time.Millisecond)
	if err != nil {
		r.t.Fatal(err)
	}
	if run.State != state {
		r.t.Fatalf("pipeline run %s is %s (%s), want %s; steps %+v", id, run.State, run.Error, state, run.Steps)
	}
	return run
}

// waitStep polls until step reaches state.
func (r *rig) waitStep(id, step, state string) pipelines.StepRow {
	r.t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for {
		run, err := pipelines.Get(context.Background(), r.pool, id)
		if err != nil {
			r.t.Fatal(err)
		}
		s := stepOf(r.t, run, step)
		if s.State == state {
			return s
		}
		if time.Now().After(deadline) {
			r.t.Fatalf("step %s is %s, want %s", step, s.State, state)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func stepOf(t *testing.T, run pipelines.Run, id string) pipelines.StepRow {
	t.Helper()
	for _, s := range run.Steps {
		if s.Step == id {
			return s
		}
	}
	t.Fatalf("run %s has no step %s", run.ID, id)
	return pipelines.StepRow{}
}

func reasons(s pipelines.StepRow) string {
	var out []string
	for _, a := range s.AttemptLog {
		out = append(out, a.Reason+":"+a.State)
	}
	return strings.Join(out, ",")
}

func (r *rig) count(sql string, args ...any) int {
	r.t.Helper()
	var n int
	if err := r.pool.QueryRow(context.Background(), sql, args...).Scan(&n); err != nil {
		r.t.Fatal(err)
	}
	return n
}

func TestTwoStepPipelineRunsAndReuses(t *testing.T) {
	r := newRig(t, nil)
	started := r.start(r.input("hello world"))
	if started.State != pipelines.RunRunning || stepOf(t, started, "first").State != pipelines.StepQueued ||
		stepOf(t, started, "count").State != pipelines.StepWaiting {
		t.Fatalf("started run %+v", started)
	}
	run := r.wait(started.ID, pipelines.RunDone)
	first, count := stepOf(t, run, "first"), stepOf(t, run, "count")
	if first.State != pipelines.StepDone || count.State != pipelines.StepDone || first.Position != 0 || count.Position != 1 {
		t.Fatalf("steps %+v", run.Steps)
	}
	if got := r.read(first.Outputs["text"]); got != "> hello world" {
		t.Errorf("echo wrote %q", got)
	}
	if count.Inputs["text"].Hash != first.Outputs["text"].Hash || !strings.Contains(r.read(count.Outputs["tally"]), `"steps":3000`) {
		t.Errorf("tally read %v and wrote %s", count.Inputs, r.read(count.Outputs["tally"]))
	}
	if len(first.Departures) != 1 || first.Departures[0].Param != "prefix" || first.Departures[0].Value != "> " || first.Departures[0].Default != "" {
		t.Errorf("echo departures %+v", first.Departures)
	}
	if count.Params["steps"] != 3000.0 || count.Params["precision"] != "bf16" || len(count.Departures) != 0 {
		t.Errorf("tally params %v departures %v (defaultRef resolves against defaults.yaml)", count.Params, count.Departures)
	}
	if first.Attempts != 1 || reasons(first) != "initial:done" || first.StartedAt == nil || first.InputHash == "" || first.JobID == "" {
		t.Errorf("first step %+v", first)
	}
	// Outputs are indexed with their producer; the input is linked to the project.
	a, err := artifacts.Get(context.Background(), r.pool, count.Outputs["tally"].Hash)
	if err != nil || a.Type != "tally" || a.Producer == nil || a.Producer.Step != "count" || a.Producer.PipelineRunID != run.ID || a.ProjectID != r.project {
		t.Errorf("tally artifact %+v (%v)", a, err)
	}
	if n := r.count("SELECT count(*) FROM artifact_projects WHERE project_id = $1", r.project); n != 3 {
		t.Errorf("%d artifacts linked to the project, want 3", n)
	}
	if n := r.count("SELECT count(*) FROM events WHERE topic = $1 AND type = 'pipeline_run.step_changed' AND project_id = $2", pipelines.Topic(run.ID), r.project); n < 4 {
		t.Errorf("%d step events", n)
	}
	if n := r.count("SELECT count(*) FROM events WHERE topic = $1 AND type = 'pipeline_run.state_changed' AND payload->'pipelineRun'->>'state' = 'done'", pipelines.Topic(run.ID)); n != 1 {
		t.Errorf("%d done events", n)
	}
	if len(r.leases.Calls()) != 2 {
		t.Fatalf("%d leases", len(r.leases.Calls()))
	}

	// The same input again: both steps are reused inside Start, no worker is asked.
	again := r.start(r.input("hello world"))
	if again.State != pipelines.RunDone {
		t.Fatalf("re-run %+v", again)
	}
	for _, s := range again.Steps {
		orig := stepOf(t, run, s.Step)
		if s.State != pipelines.StepReused || s.ReusedFrom != orig.ID || s.Outputs[firstKey(orig.Outputs)].Hash != orig.Outputs[firstKey(orig.Outputs)].Hash {
			t.Errorf("re-run step %+v", s)
		}
	}
	if len(r.leases.Calls()) != 2 {
		t.Fatalf("reuse asked a worker: %d leases", len(r.leases.Calls()))
	}

	// fresh runs everything; a changed parameter re-runs the step and, with a new input, its dependent.
	in := r.input("hello world")
	in.Fresh = true
	r.wait(r.start(in).ID, pipelines.RunDone)
	in = r.input("hello world")
	in.Params = map[string]map[string]any{"first": {"prefix": ">> "}}
	changed := r.wait(r.start(in).ID, pipelines.RunDone)
	if stepOf(t, changed, "first").State != pipelines.StepDone || stepOf(t, changed, "count").State != pipelines.StepDone {
		t.Errorf("changed run %+v", changed.Steps)
	}
	if len(r.leases.Calls()) != 6 {
		t.Errorf("%d leases after fresh and changed runs, want 6", len(r.leases.Calls()))
	}
}

func TestRuntimeUpgradeIsNotReused(t *testing.T) {
	r := newRig(t, nil)
	run := r.wait(r.start(r.input("hello world")).ID, pipelines.RunDone)
	if len(r.leases.Calls()) != 2 {
		t.Fatalf("%d leases", len(r.leases.Calls()))
	}
	// The echo runtime is upgraded (a new image): the same echo@1 is published by a new runtime version.
	upgraded := map[string]any{}
	maps.Copy(upgraded, pipelinestest.Fixtures[0])
	upgraded["runtimeVersionId"] = "ver_test_upgraded"
	if err := pipelinestest.Register(context.Background(), r.pool, upgraded); err != nil {
		t.Fatal(err)
	}
	again := r.wait(r.start(r.input("hello world")).ID, pipelines.RunDone)
	first, count := stepOf(t, again, "first"), stepOf(t, again, "count")
	if first.State != pipelines.StepDone || first.ReusedFrom != "" || first.InputHash == stepOf(t, run, "first").InputHash {
		t.Errorf("echo on the upgraded runtime: %+v; it must run again, not reuse the old runtime's output", first)
	}
	// Its output is the same bytes, and tally's runtime did not change: tally is still reused.
	if count.State != pipelines.StepReused || count.ReusedFrom != stepOf(t, run, "count").ID {
		t.Errorf("tally after the echo upgrade: %+v", count)
	}
	if len(r.leases.Calls()) != 3 {
		t.Errorf("%d leases, want 3 (echo again only)", len(r.leases.Calls()))
	}
}

func firstKey(m map[string]steps.ArtifactRef) string {
	for k := range m {
		return k
	}
	return ""
}

func TestOOMRetriesOnceAtThreeQuartersBatch(t *testing.T) {
	r := newRig(t, nil)
	oom := &steps.StepError{Type: steps.ErrOOM, Message: "CUDA out of memory"}
	r.leases.Script("count", pipelinestest.Action{Fail: oom})
	run := r.wait(r.start(r.input("abc")).ID, pipelines.RunDone)
	count := stepOf(t, run, "count")
	calls := r.leases.CallsOf("count")
	if len(calls) != 2 || calls[0].Spec.Overrides.BatchScale != 0 || calls[1].Spec.Overrides.BatchScale != steps.OOMBatchScale || calls[1].Spec.Attempt != 2 {
		t.Fatalf("calls %+v", calls)
	}
	if count.Attempts != 2 || reasons(count) != "initial:failed,oom:done" || count.AttemptLog[0].Error.Type != steps.ErrOOM ||
		count.AttemptLog[1].BatchScale != 0.75 || count.Error != nil {
		t.Errorf("count %+v", count)
	}
	if !strings.Contains(r.read(count.Outputs["tally"]), `"batchScale":0.75`) {
		t.Errorf("the retry did not run at 0.75: %s", r.read(count.Outputs["tally"]))
	}

	// A second OOM fails the step and the run.
	r.leases.Script("count", pipelinestest.Action{Fail: oom}, pipelinestest.Action{Fail: oom})
	failed := r.wait(r.start(r.input("abcd")).ID, pipelines.RunFailed)
	count = stepOf(t, failed, "count")
	if count.State != pipelines.StepFailed || count.Error == nil || count.Error.Type != steps.ErrOOM || count.Attempts != 2 ||
		!strings.Contains(failed.Error, "step count failed (oom)") {
		t.Errorf("failed count %+v run error %q", count, failed.Error)
	}
}

// After a manual retry at half batch, the automatic OOM retry shrinks that batch (0.5 × 0.75), never back to 0.75.
func TestOOMRetryScalesTheFailedAttempt(t *testing.T) {
	r := newRig(t, nil)
	r.leases.Script("count", pipelinestest.Action{Fail: &steps.StepError{Type: steps.ErrStep, Message: "boom"}},
		pipelinestest.Action{Fail: &steps.StepError{Type: steps.ErrOOM, Message: "CUDA out of memory"}})
	failed := r.wait(r.start(r.input("abc")).ID, pipelines.RunFailed)
	half := 0.5
	if err := r.tx(func(ctx context.Context, tx pgx.Tx) ([]events.Draft, error) {
		_, drafts, err := r.eng.Retry(ctx, tx, failed.ID, failed.Rev, pipelines.RetryInput{Step: "count", BatchScale: &half})
		return drafts, err
	}); err != nil {
		t.Fatal(err)
	}
	run := r.wait(failed.ID, pipelines.RunDone)
	count := stepOf(t, run, "count")
	if got := reasons(count); got != "initial:failed,retry:failed,oom:done" {
		t.Fatalf("attempts %s", got)
	}
	if count.AttemptLog[1].BatchScale != 0.5 || count.AttemptLog[2].BatchScale != 0.375 {
		t.Errorf("batch scales %v, %v; want 0.5, 0.375", count.AttemptLog[1].BatchScale, count.AttemptLog[2].BatchScale)
	}
	if calls := r.leases.CallsOf("count"); len(calls) != 3 || calls[2].Spec.Overrides.BatchScale != 0.375 {
		t.Errorf("calls %+v", calls)
	}
}

func TestLostLeaseRetriesOnce(t *testing.T) {
	r := newRig(t, nil)
	r.leases.Script("first", pipelinestest.Action{Fail: &steps.StepError{Type: steps.ErrLost, Message: "missed 3 heartbeats"}})
	run := r.wait(r.start(r.input("abc")).ID, pipelines.RunDone)
	if got := reasons(stepOf(t, run, "first")); got != "initial:failed,lost:done" {
		t.Errorf("attempts %s", got)
	}
}

func TestRetryOneFailedStep(t *testing.T) {
	r := newRig(t, nil)
	r.leases.Script("first", pipelinestest.Action{Fail: &steps.StepError{Type: steps.ErrStep, Message: "boom"}})
	failed := r.wait(r.start(r.input("abc")).ID, pipelines.RunFailed)
	if s := stepOf(t, failed, "first"); s.State != pipelines.StepFailed || s.Error.Message != "boom" {
		t.Fatalf("first %+v", s)
	}
	if s := stepOf(t, failed, "count"); s.State != pipelines.StepSkipped {
		t.Fatalf("count %+v", s)
	}

	retry := func(rev int, in pipelines.RetryInput) (pipelines.Run, error) {
		var out pipelines.Run
		err := r.tx(func(ctx context.Context, tx pgx.Tx) ([]events.Draft, error) {
			var drafts []events.Draft
			var err error
			out, drafts, err = r.eng.Retry(ctx, tx, failed.ID, rev, in)
			return drafts, err
		})
		return out, err
	}
	if _, err := retry(failed.Rev+5, pipelines.RetryInput{Step: "first"}); !isProblem(err, problems.PreconditionFailed) {
		t.Fatalf("stale retry: %v", err)
	}
	if _, err := retry(failed.Rev, pipelines.RetryInput{Step: "count"}); !isProblem(err, problems.Conflict) {
		t.Fatalf("retrying a skipped step: %v", err)
	}
	reopened, err := retry(failed.Rev, pipelines.RetryInput{Step: "first"})
	if err != nil {
		t.Fatal(err)
	}
	if reopened.State != pipelines.RunRunning || stepOf(t, reopened, "count").State != pipelines.StepWaiting {
		t.Fatalf("reopened %+v", reopened)
	}
	run := r.wait(failed.ID, pipelines.RunDone)
	if got := reasons(stepOf(t, run, "first")); got != "initial:failed,retry:done" {
		t.Errorf("first attempts %s", got)
	}
	if s := stepOf(t, run, "count"); s.Attempts != 1 || s.State != pipelines.StepDone {
		t.Errorf("count %+v", s)
	}
	if _, err := retry(run.Rev, pipelines.RetryInput{}); !isProblem(err, problems.Conflict) {
		t.Errorf("retrying a done run: %v", err)
	}
}

func isProblem(err error, typ problems.Type) bool {
	var pe *problems.Error
	return errors.As(err, &pe) && pe.Type == typ
}

func TestCancelStopsRunningStep(t *testing.T) {
	r := newRig(t, nil)
	r.leases.Script("first", pipelinestest.Action{Block: true})
	started := r.start(r.input("abc"))
	running := r.waitStep(started.ID, "first", pipelines.StepRunning)
	if running.AttemptLog[0].State != pipelines.StepRunning || running.AttemptLog[0].StartedAt == nil {
		t.Errorf("leased attempt %+v", running.AttemptLog)
	}
	cur, err := pipelines.GetRun(context.Background(), r.pool, started.ID)
	if err != nil {
		t.Fatal(err)
	}
	var cancelled pipelines.Run
	if err := r.tx(func(ctx context.Context, tx pgx.Tx) ([]events.Draft, error) {
		var drafts []events.Draft
		var err error
		cancelled, drafts, err = r.eng.Cancel(ctx, tx, started.ID, cur.Rev)
		return drafts, err
	}); err != nil {
		t.Fatal(err)
	}
	if cancelled.State != pipelines.RunCancelled || stepOf(t, cancelled, "first").State != pipelines.StepCancelled ||
		stepOf(t, cancelled, "count").State != pipelines.StepCancelled {
		t.Fatalf("cancelled %+v", cancelled)
	}
	j, err := jobs.Wait(context.Background(), r.pool, running.JobID, 20*time.Second, 50*time.Millisecond)
	if err != nil || j.State != jobs.StateCancelled {
		t.Fatalf("step job %+v (%v)", j, err)
	}
	final, _ := pipelines.Get(context.Background(), r.pool, started.ID)
	if final.State != pipelines.RunCancelled || stepOf(t, final, "first").State != pipelines.StepCancelled {
		t.Errorf("after the job ended %+v", final)
	}
	if err := r.tx(func(ctx context.Context, tx pgx.Tx) ([]events.Draft, error) {
		_, drafts, err := r.eng.Cancel(ctx, tx, started.ID, final.Rev)
		return drafts, err
	}); !isProblem(err, problems.Conflict) {
		t.Errorf("cancelling an ended run: %v", err)
	}
}

// A job requeued in place (pause, window close) and leased again keeps its step running; the run observer hears of
// the new lease so a training run's status goes back from queued to running.
func TestReleaseOfRunningStepCallsTheObserver(t *testing.T) {
	r := newRig(t, nil)
	var mu sync.Mutex
	seen := map[string]int{}
	r.eng.SetObserver(func(_ context.Context, _ pgx.Tx, run pipelines.Run) ([]events.Draft, error) {
		mu.Lock()
		defer mu.Unlock()
		seen[run.RunID]++
		return nil, nil
	})
	r.leases.Script("first", pipelinestest.Action{Block: true})
	in := r.input("abc")
	in.RunID = "run_observed"
	started := r.start(in)
	running := r.waitStep(started.ID, "first", pipelines.StepRunning)
	count := func() int {
		mu.Lock()
		defer mu.Unlock()
		return seen["run_observed"]
	}
	before := count()
	if err := r.tx(func(ctx context.Context, tx pgx.Tx) ([]events.Draft, error) {
		return r.eng.Leased(ctx, tx, running.JobID)
	}); err != nil {
		t.Fatal(err)
	}
	if after := count(); after != before+1 {
		t.Fatalf("observer calls: %d before the second lease, %d after", before, after)
	}
	again, _ := pipelines.Get(context.Background(), r.pool, started.ID)
	if st := stepOf(t, again, "first"); st.State != pipelines.StepRunning || len(st.AttemptLog) != 1 {
		t.Fatalf("a second lease must not touch the step: %+v", st)
	}
}

// A control plane that stops while a step waits on a worker does not lose the step: the step job is snoozed (same
// attempt, mirror still running), the startup sweep leaves it alone, and the next start waits for the same job's
// outcome instead of failing it and training again from scratch (found in the 2026-10-01 rehearsal).
func TestStepSurvivesAControlPlaneRestart(t *testing.T) {
	r := newRig(t, nil)
	r.leases.Script("first", pipelinestest.Action{Block: true})
	started := r.start(r.input("abc"))
	running := r.waitStep(started.ID, "first", pipelines.StepRunning)

	// Stop as main does: the grace period ends, so River cancels the waiting handler.
	sctx, scancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	_ = r.jobs.Stop(sctx)
	scancel()
	ctx := context.Background()
	var mirror, river string
	if err := r.pool.QueryRow(ctx, `SELECT j.state, rj.state FROM jobs j JOIN river_job rj ON rj.id = j.river_id WHERE j.id = $1`,
		running.JobID).Scan(&mirror, &river); err != nil {
		t.Fatal(err)
	}
	if mirror != jobs.StateRunning || (river != "available" && river != "scheduled") {
		t.Fatalf("after the stop: job %s, river job %s", mirror, river)
	}
	if err := r.eng.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	cur, err := pipelines.Get(ctx, r.pool, started.ID)
	if err != nil {
		t.Fatal(err)
	}
	if st := stepOf(t, cur, "first"); cur.State != pipelines.RunRunning || st.State != pipelines.StepRunning || st.JobID != running.JobID {
		t.Fatalf("the sweep touched the interrupted step: %+v", st)
	}

	// The next start: the worker's outcome now arrives (the fake runs the step normally) and the run finishes on
	// the same job and attempt.
	r.leases.Script("first")
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	js := jobs.New(r.pool, quiet)
	js.FetchPollInterval = 100 * time.Millisecond
	r.eng.Register(js)
	jctx, jcancel := context.WithCancel(ctx)
	if err := js.Start(jctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		c, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_ = js.Stop(c)
		cancel()
		jcancel()
	})
	done := r.wait(started.ID, pipelines.RunDone)
	if st := stepOf(t, done, "first"); st.JobID != running.JobID || st.Attempts != 1 || len(st.AttemptLog) != 1 {
		t.Fatalf("the step after the restart: %+v", st)
	}
}

func TestOutputHooksRunInTheStepTransaction(t *testing.T) {
	failHook := false
	r := newRig(t, func(h *steps.Hooks) {
		h.On("text", func(ctx context.Context, tx pgx.Tx, out steps.Output) ([]events.Draft, error) {
			if _, err := tx.Exec(ctx, "INSERT INTO hook_marks (hash, step) VALUES ($1, $2)", out.Artifact.Hash, out.StepID); err != nil {
				return nil, err
			}
			return nil, nil
		})
		h.On("tally", func(ctx context.Context, tx pgx.Tx, out steps.Output) ([]events.Draft, error) {
			// The output is already indexed in this transaction, and the step is not yet done outside it.
			var n int
			if err := tx.QueryRow(ctx, "SELECT count(*) FROM artifacts WHERE hash = $1", out.Artifact.Hash).Scan(&n); err != nil || n != 1 {
				return nil, errors.New("the output is not recorded in the hook's transaction")
			}
			if failHook {
				return nil, errors.New("checkpoint registry refused it")
			}
			if _, err := tx.Exec(ctx, "INSERT INTO hook_marks (hash, step) VALUES ($1, $2)", out.Artifact.Hash, out.StepID); err != nil {
				return nil, err
			}
			return []events.Draft{{Topic: "test.hooks", Type: "test.hooked", ProjectID: out.ProjectID, Payload: map[string]any{"name": out.Name}}}, nil
		})
	})
	if _, err := r.pool.Exec(context.Background(), "CREATE TABLE hook_marks (hash text, step text)"); err != nil {
		t.Fatal(err)
	}
	run := r.wait(r.start(r.input("abc")).ID, pipelines.RunDone)
	if n := r.count("SELECT count(*) FROM hook_marks"); n != 2 {
		t.Errorf("%d hook marks", n)
	}
	if n := r.count("SELECT count(*) FROM events WHERE type = 'test.hooked'"); n != 1 {
		t.Errorf("%d hook events", n)
	}
	_ = run

	// A failing hook fails the step; its output is not indexed and its writes are rolled back.
	failHook = true
	failed := r.wait(r.start(r.input("abcdef")).ID, pipelines.RunFailed)
	count := stepOf(t, failed, "count")
	if count.State != pipelines.StepFailed || !strings.Contains(count.Error.Message, "checkpoint registry refused it") || count.Outputs != nil {
		t.Fatalf("count %+v", count)
	}
	if n := r.count("SELECT count(*) FROM hook_marks"); n != 3 { // + the echo output of the second run
		t.Errorf("%d hook marks after the failing hook", n)
	}
	if n := r.count("SELECT count(*) FROM artifacts WHERE type = 'tally'"); n != 1 {
		t.Errorf("%d tally artifacts indexed", n)
	}
}

// A hook that refuses reused outputs fails that step and its run, like a refusal of a fresh output, and leaves the
// caller's transaction intact: the pipeline run is created, its hook writes are undone.
func TestReusedOutputRefusedByAHook(t *testing.T) {
	refuse := false
	r := newRig(t, func(h *steps.Hooks) {
		h.On("text", func(ctx context.Context, tx pgx.Tx, out steps.Output) ([]events.Draft, error) {
			if _, err := tx.Exec(ctx, "INSERT INTO hook_marks (hash, step) VALUES ($1, $2)", out.Artifact.Hash, out.StepID); err != nil {
				return nil, err
			}
			if refuse {
				return nil, errors.New("checkpoint registry refused it")
			}
			return nil, nil
		})
	})
	if _, err := r.pool.Exec(context.Background(), "CREATE TABLE hook_marks (hash text, step text)"); err != nil {
		t.Fatal(err)
	}
	r.wait(r.start(r.input("abc")).ID, pipelines.RunDone)
	if n := r.count("SELECT count(*) FROM hook_marks"); n != 1 {
		t.Fatalf("%d hook marks", n)
	}

	refuse = true
	started := r.start(r.input("abc")) // would fail the test if Start returned the hook's error
	failed := r.wait(started.ID, pipelines.RunFailed)
	first := stepOf(t, failed, "first")
	if first.State != pipelines.StepFailed || first.Error == nil || first.Error.Type != steps.ErrStep ||
		!strings.Contains(first.Error.Message, "checkpoint registry refused it") || first.Outputs != nil || first.ReusedFrom != "" {
		t.Fatalf("first %+v", first)
	}
	if s := stepOf(t, failed, "count"); s.State != pipelines.StepSkipped {
		t.Errorf("count %+v", s)
	}
	if !strings.Contains(failed.Error, "step first failed (step)") {
		t.Errorf("run error %q", failed.Error)
	}
	if n := r.count("SELECT count(*) FROM hook_marks"); n != 1 {
		t.Errorf("%d hook marks: the refused hook's write was not undone", n)
	}
	if len(r.leases.CallsOf("first")) != 1 {
		t.Errorf("the refused reuse asked a worker")
	}
}

func TestSweepRetriesStepsWhoseJobEnded(t *testing.T) {
	r := newRig(t, nil)
	r.leases.Script("first", pipelinestest.Action{Block: true})
	started := r.start(r.input("abc"))
	running := r.waitStep(started.ID, "first", pipelines.StepRunning)
	// The control plane stopped while the job waited: its job ended without an outcome.
	if _, err := r.pool.Exec(context.Background(), "UPDATE jobs SET state = 'failed', finished_at = now() WHERE id = $1", running.JobID); err != nil {
		t.Fatal(err)
	}
	if err := r.eng.Sweep(context.Background()); err != nil {
		t.Fatal(err)
	}
	run := r.wait(started.ID, pipelines.RunDone)
	if got := reasons(stepOf(t, run, "first")); got != "initial:failed,lost:done" {
		t.Errorf("attempts %s", got)
	}
}

func TestStartValidatesInputsAgainstTheStore(t *testing.T) {
	r := newRig(t, nil)
	in := r.input("abc")
	in.Inputs["text"] = steps.ArtifactRef{Hash: cas.Hash([]byte("never stored")), Type: "text", Size: 12}
	err := r.tx(func(ctx context.Context, tx pgx.Tx) ([]events.Draft, error) {
		_, drafts, err := r.eng.Start(ctx, tx, in)
		return drafts, err
	})
	if !isProblem(err, problems.PipelineInvalid) || !strings.Contains(err.Error(), "not in the content store") {
		t.Fatalf("start with a missing input: %v", err)
	}
	in = r.input("abc")
	in.Version = "somewhere-else"
	err = r.tx(func(ctx context.Context, tx pgx.Tx) ([]events.Draft, error) {
		_, drafts, err := r.eng.Start(ctx, tx, in)
		return drafts, err
	})
	if !isProblem(err, problems.PreconditionFailed) {
		t.Fatalf("start at another version: %v", err)
	}

	// An indexed artifact may be named by {hash, type} alone (checkpoints.list gives no size): the index fills it in.
	first := r.start(r.input("indexed"))
	again := r.input("indexed")
	again.Inputs["text"] = steps.ArtifactRef{Hash: first.Inputs["text"].Hash, Type: "text"}
	second := r.start(again)
	if got := second.Inputs["text"]; got.Size != int64(len("indexed")) {
		t.Fatalf("input recorded as %+v", got)
	}
	// An unindexed blob without its size is still refused.
	loose := r.put("loose blob")
	in = r.input("abc")
	in.Inputs["text"] = steps.ArtifactRef{Hash: loose.Hash, Type: "text"}
	err = r.tx(func(ctx context.Context, tx pgx.Tx) ([]events.Draft, error) {
		_, drafts, err := r.eng.Start(ctx, tx, in)
		return drafts, err
	})
	if !isProblem(err, problems.PipelineInvalid) {
		t.Fatalf("start with an unindexed input without size: %v", err)
	}
}

// A lost-lease retry resumes from the newest training state the step published during its leases, not from the
// start; an evicted one does not count, and another step's states are not this step's.
func TestNewestPublishedState(t *testing.T) {
	r := newRig(t, nil)
	ctx := context.Background()
	h := func(c byte) string { return "b3:" + strings.Repeat(string(c), 64) }
	ins := func(hash, typ, step string, ago time.Duration, evicted bool) {
		t.Helper()
		var ev *time.Time
		if evicted {
			now := time.Now()
			ev = &now
		}
		if _, err := r.pool.Exec(ctx, `INSERT INTO artifacts (hash, type, size, step_id, created_at, evicted_at)
			VALUES ($1, $2, 1, $3, now() - $4::interval, $5)`, hash, typ, step, ago.String(), ev); err != nil {
			t.Fatal(err)
		}
	}
	if got, err := pipelines.NewestPublishedState(ctx, r.pool, "pls_a"); err != nil || got != "" {
		t.Fatalf("no states: %q, %v", got, err)
	}
	ins(h('a'), "training-state", "pls_a", 40*time.Minute, false)
	ins(h('b'), "training-state", "pls_a", 20*time.Minute, false)
	ins(h('c'), "training-state", "pls_a", time.Minute, true) // evicted: its bytes are gone
	ins(h('d'), "checkpoint", "pls_a", 0, false)              // not a state
	ins(h('e'), "training-state", "pls_b", 0, false)          // another step's
	if got, err := pipelines.NewestPublishedState(ctx, r.pool, "pls_a"); err != nil || got != h('b') {
		t.Fatalf("newest state %q, %v; want %s", got, err, h('b'))
	}
}

// A kind may leave an optional output unwritten (a train step's final training state): the step still succeeds,
// and no pipeline may wire that output into another step.
func TestOptionalOutputs(t *testing.T) {
	r := newRig(t, nil)
	ctx := context.Background()
	echo2 := map[string]any{}
	for k, v := range pipelinestest.Fixtures[0] {
		echo2[k] = v
	}
	echo2["version"] = "2"
	echo2["produces"] = map[string]string{"text": "text", "extra": "text"}
	echo2["optionalOutputs"] = []string{"extra"}
	if err := pipelinestest.Register(ctx, r.pool, echo2); err != nil {
		t.Fatal(err)
	}
	one := &pipelines.Pipeline{Name: "one", Inputs: map[string]string{"text": "text"},
		Steps: []pipelines.Step{{ID: "a", Kind: "echo@2", In: map[string]string{"text": "$inputs.text"}}}}
	in := r.input("abc")
	in.Pipeline = one
	run := r.wait(r.start(in).ID, pipelines.RunDone)
	if a := stepOf(t, run, "a"); a.State != pipelines.StepDone || len(a.Outputs) != 1 {
		t.Fatalf("step a %+v: done with only its written output", a)
	}

	wired := &pipelines.Pipeline{Name: "wired", Inputs: map[string]string{"text": "text"}, Steps: []pipelines.Step{
		{ID: "a", Kind: "echo@2", In: map[string]string{"text": "$inputs.text"}},
		{ID: "b", Kind: "echo@1", In: map[string]string{"text": "a.extra"}},
	}}
	_, err := r.eng.Plan(ctx, r.pool, *wired, pipelines.PlanInput{Inputs: in.Inputs})
	var pe *problems.Error
	if !errors.As(err, &pe) || len(pe.Errors) == 0 || !strings.Contains(pe.Errors[0].Message, "may finish without") {
		t.Fatalf("plan of a pipeline wiring an optional output: %v", err)
	}
}

// TestPlanRefusesUnpublishedPin: once the kind's runtime has registered workers, a pinned version none of them
// publishes any more is refused at planning (it would wait in the queue for ever), naming the published versions.
func TestPlanRefusesUnpublishedPin(t *testing.T) {
	r := newRig(t, nil)
	ctx := context.Background()
	in := r.input("abc")
	if _, err := r.eng.Plan(ctx, r.pool, *chain(), pipelines.PlanInput{Inputs: in.Inputs}); err != nil {
		t.Fatalf("no worker registered yet: the pins are not judged: %v", err)
	}
	var verID string
	if err := r.pool.QueryRow(ctx, `SELECT v.id FROM registry_versions v JOIN registry_collections c ON c.id = v.collection_id
		WHERE c.name = 'step-kind/echo' LIMIT 1`).Scan(&verID); err != nil {
		t.Fatal(err)
	}
	if _, err := r.pool.Exec(ctx, `INSERT INTO compute_hosts (id, name, cards) VALUES ('cmp_stale', 'stale-host', '[]')`); err != nil {
		t.Fatal(err)
	}
	if _, err := r.pool.Exec(ctx, `INSERT INTO workers (id, host_id, runtime_name, runtime_version_id, runtime, step_kinds)
		VALUES ('wrk_stale', 'cmp_stale', 'test', $1, '{}', ARRAY['echo@2', 'tally@1'])`, verID); err != nil {
		t.Fatal(err)
	}
	_, err := r.eng.Plan(ctx, r.pool, *chain(), pipelines.PlanInput{Inputs: in.Inputs})
	var pe *problems.Error
	if !errors.As(err, &pe) || len(pe.Errors) != 1 || pe.Errors[0].Path != "steps[0].kind" ||
		!strings.Contains(pe.Errors[0].Message, "echo@1") || !strings.Contains(pe.Errors[0].Message, "they publish echo@2") {
		t.Fatalf("plan with a stale echo@1 pin: %v (%+v)", err, pe)
	}
	if _, err := r.pool.Exec(ctx, `UPDATE workers SET step_kinds = ARRAY['echo@1', 'echo@2', 'tally@1'] WHERE id = 'wrk_stale'`); err != nil {
		t.Fatal(err)
	}
	if _, err := r.eng.Plan(ctx, r.pool, *chain(), pipelines.PlanInput{Inputs: in.Inputs}); err != nil {
		t.Fatalf("a published pin: %v", err)
	}
}

// TestOptionalStepFailureLeavesTheRunDone: an optional step that fails skips the optional steps reading it, and the
// run ends done once the rest has; a step reading an optional one must be optional too.
func TestOptionalStepFailureLeavesTheRunDone(t *testing.T) {
	r := newRig(t, nil)
	p := &pipelines.Pipeline{Name: "soft", Inputs: map[string]string{"text": "text"}, Steps: []pipelines.Step{
		{ID: "main", Kind: "echo@1", In: map[string]string{"text": "$inputs.text"}},
		{ID: "extra", Kind: "echo@1", In: map[string]string{"text": "$inputs.text"}, Optional: true},
		{ID: "after", Kind: "tally@1", In: map[string]string{"text": "extra.text"}, Optional: true},
		{ID: "count", Kind: "tally@1", In: map[string]string{"text": "main.text"}},
	}}
	r.leases.Script("extra", pipelinestest.Action{Fail: &steps.StepError{Type: steps.ErrStep, Message: "no VAD"}})
	in := r.input("abc")
	in.Pipeline = p
	run := r.wait(r.start(in).ID, pipelines.RunDone)
	if s := stepOf(t, run, "extra"); s.State != pipelines.StepFailed || s.Error == nil || s.Error.Message != "no VAD" {
		t.Fatalf("extra %+v", s)
	}
	if s := stepOf(t, run, "after"); s.State != pipelines.StepSkipped {
		t.Fatalf("after %+v", s)
	}
	if s := stepOf(t, run, "count"); s.State != pipelines.StepDone {
		t.Fatalf("count %+v", s)
	}
	// A required step may read an optional one as one of several artifacts of an input (a pseudo-label member):
	// when the optional step fails, it runs without that artifact.
	members := &pipelines.Pipeline{Name: "members", Inputs: map[string]string{"text": "text"}, Steps: []pipelines.Step{
		{ID: "main", Kind: "echo@1", In: map[string]string{"text": "$inputs.text"}},
		{ID: "extra", Kind: "echo@1", In: map[string]string{"text": "$inputs.text"}, Optional: true},
		{ID: "count", Kind: "tally@1", In: map[string]string{"text.0": "extra.text", "text.1": "main.text"}},
	}}
	if err := members.Check(""); err != nil {
		t.Fatalf("an indexed wire from an optional step: %v", err)
	}
	r.leases.Script("extra", pipelinestest.Action{Fail: &steps.StepError{Type: steps.ErrStep, Message: "service down"}})
	in = r.input("abcd")
	in.Pipeline = members
	run = r.wait(r.start(in).ID, pipelines.RunDone)
	count := stepOf(t, run, "count")
	if count.State != pipelines.StepDone {
		t.Fatalf("count %+v", count)
	}
	if _, ok := count.Inputs["text.0"]; ok || len(count.Inputs) != 1 {
		t.Fatalf("count ran with the failed optional step's artifact: %+v", count.Inputs)
	}

	p.Steps[2].Optional = false
	err := p.Check("")
	var pe *problems.Error
	if !errors.As(err, &pe) || len(pe.Errors) == 0 || !strings.Contains(pe.Errors[0].Message, "optional") {
		t.Fatalf("a required step reading an optional one: %v", err)
	}
}

// TestOptionalStepWithoutAWorkerIsSkipped (the phase-4 audit's C3): an optional pseudo-label member whose kind no
// runtime publishes, or whose kind only a worker not seen for a while publishes, never blocks the run: the plan warns
// (step-kind-unavailable), Start records the step skipped, and the ensemble runs without its artifact.
func TestOptionalStepWithoutAWorkerIsSkipped(t *testing.T) {
	r := newRig(t, nil)
	ctx := context.Background()
	members := func(kind string) *pipelines.Pipeline {
		return &pipelines.Pipeline{Name: "members", Inputs: map[string]string{"text": "text"}, Steps: []pipelines.Step{
			{ID: "main", Kind: "echo@1", In: map[string]string{"text": "$inputs.text"}},
			{ID: "oasis", Kind: kind, In: map[string]string{"text": "$inputs.text"}, Optional: true},
			{ID: "after", Kind: "tally@1", In: map[string]string{"text": "oasis.text"}, Optional: true},
			{ID: "ensemble", Kind: "tally@1", In: map[string]string{"text.0": "main.text", "text.2": "oasis.text"}},
		}}
	}
	check := func(text, kind, why string) {
		t.Helper()
		in := r.input(text)
		in.Pipeline = members(kind)
		plan, err := r.eng.Plan(ctx, r.pool, *in.Pipeline, pipelines.PlanInput{Inputs: in.Inputs})
		if err != nil {
			t.Fatalf("plan with an unavailable optional member (%s): %v", kind, err)
		}
		if len(plan.Warnings) != 1 || plan.Warnings[0].Code != pipelines.WarningStepKindUnavailable ||
			plan.Warnings[0].Step != "oasis" || !strings.Contains(plan.Warnings[0].Message, why) {
			t.Fatalf("warnings %+v", plan.Warnings)
		}
		run := r.wait(r.start(in).ID, pipelines.RunDone)
		for _, id := range []string{"oasis", "after"} {
			if s := stepOf(t, run, id); s.State != pipelines.StepSkipped {
				t.Fatalf("%s %+v", id, s)
			}
		}
		ens := stepOf(t, run, "ensemble")
		if ens.State != pipelines.StepDone || len(ens.Inputs) != 1 {
			t.Fatalf("ensemble %+v", ens)
		}
	}
	// No runtime publishes the member's kind at all (its worker never registered).
	check("abc", "oasis_transcribe@1", "no runtime publishes step kind oasis_transcribe@1")

	// The member's kind is published, but only by a worker last seen an hour ago; the live worker does not publish it.
	var verID string
	if err := r.pool.QueryRow(ctx, `SELECT v.id FROM registry_versions v JOIN registry_collections c ON c.id = v.collection_id
		WHERE c.name = 'step-kind/echo' LIMIT 1`).Scan(&verID); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`INSERT INTO compute_hosts (id, name, cards) VALUES ('cmp_live', 'live-host', '[]'), ('cmp_gone', 'gone-host', '[]')`,
		`INSERT INTO workers (id, host_id, runtime_name, runtime_version_id, runtime, step_kinds, last_seen_at) VALUES
			('wrk_live', 'cmp_live', 'test', '` + verID + `', '{}', ARRAY['tally@1'], now()),
			('wrk_gone', 'cmp_gone', 'test', '` + verID + `', '{}', ARRAY['echo@1', 'tally@1'], now() - interval '1 hour')`,
	} {
		if _, err := r.pool.Exec(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	check("abcd", "echo@1", "no test worker that publishes step kind echo@1 has been seen")

	// A required step is never judged by liveness: it waits for a worker like any other.
	in := r.input("abcde")
	if _, err := r.eng.Plan(ctx, r.pool, *in.Pipeline, pipelines.PlanInput{Inputs: in.Inputs}); err != nil {
		t.Fatalf("a required echo@1 step without a live worker: %v", err)
	}
}
