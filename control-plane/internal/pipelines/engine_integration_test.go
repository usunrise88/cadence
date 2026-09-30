//go:build integration

package pipelines_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
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
}
