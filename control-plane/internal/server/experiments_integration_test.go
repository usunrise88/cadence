//go:build integration

package server

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/usunrise88/cadence/control-plane/internal/experiments"
	"github.com/usunrise88/cadence/control-plane/internal/pipelines/pipelinestest"
	"github.com/usunrise88/cadence/control-plane/internal/runs"
	"github.com/usunrise88/cadence/control-plane/internal/steps"
)

type sweepPointView struct {
	Index            int
	Values           map[string]any
	State            string
	RunID            string
	EstimateGpuHours float64
	GpuHours         float64
}

type sweepView struct {
	ID, ExperimentID, Mode, State, StopReason, CurrentRunID string
	GpuHourCap, GpuHoursSpent, EstimateGpuHours             float64
	Points                                                  []sweepPointView
	RunsDone, Rev                                           int
}

type experimentView struct {
	ID, ProjectID, Name, Question, Tag string
	Mix                                struct {
		ID, Name    string
		Revision    int
		ReplayShare float64
	}
	BaseModel  struct{ ID string }
	RunCount   int
	Parameters []struct {
		Name    string
		Default any
		Swept   bool
		Departs bool
	}
	Runs []struct {
		RunID, Status, SweepID, BestCheckpointID string
		Point                                    *int
		Values                                   map[string]any
		Departures                               []string
		BestValWer                               *float64
		GpuHours                                 float64
		Best                                     bool
	}
	Best *struct {
		RunID, CheckpointID, Reason string
		ValWer                      float64
		Registrable                 bool
	}
	Sweeps []sweepView
	Rev    int
}

type sweepPlanView struct {
	Points           []sweepPointView
	EstimateGpuHours runs.Range
	GpuHourCap       float64
	WithinCap        bool
	Fits             int
	Basis            string
	Budget           struct {
		RemainingGpuHours float64
		WithinDailyBudget bool
	}
}

func (e *env) experiment(id string) experimentView {
	e.t.Helper()
	var x experimentView
	e.ok(e.do("GET", "/api/experiments/"+id, ""), 200, &x)
	return x
}

// waitSweep polls experiments.get until sweep id is in state.
func (e *env) waitSweep(expID, sweepID, state string) sweepView {
	e.t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		for _, sw := range e.experiment(expID).Sweeps {
			if sw.ID == sweepID && sw.State == state {
				return sw
			}
		}
		if time.Now().After(deadline) {
			e.t.Fatalf("sweep %s never reached %s: %+v", sweepID, state, e.experiment(expID).Sweeps)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// trainDone releases a train lease as done with a checkpoint of the given validation WER.
func (e *env) trainDone(l workerLease, wer float64) {
	e.t.Helper()
	e.release(l.ID, steps.Outcome{State: steps.StateDone, Metrics: map[string]float64{"val_wer": wer}, Outputs: map[string]steps.ArtifactRef{
		"checkpoint": e.putJSON(map[string]any{"lease": l.ID, "of": "weights"}, "checkpoint", map[string]any{"step": 100, "valWer": wer, "family": pipelinestest.FamilyName,
			"weightsHash": "w-" + l.ID}),
		"state": e.putJSON(map[string]any{"lease": l.ID, "of": "state"}, "training-state", map[string]any{"step": 100}),
	}})
}

// Experiments and sweeps on the real worker protocol: an experiment pins its mix revision and base model; runs.new
// with it inherits them; a sweep is planned point by point, refused over its cap, starts one run at a time, stops
// before a run that would pass the cap, goes on to the next point when a run ends and is cancelled with its run;
// experiments.get compares the runs and names the best one.
func TestExperimentsAndSweeps(t *testing.T) {
	e := start(t)
	e.useWorkers()
	ctx := context.Background()
	if err := pipelinestest.RegisterBaseModel(ctx, e.pool); err != nil {
		t.Fatal(err)
	}
	kinds := map[string]any{}
	for _, f := range pipelinestest.TrainingFixtures {
		k := map[string]any{}
		for key, v := range f {
			if key != "name" && key != "runtime" && key != "runtimeVersionId" {
				k[key] = v
			}
		}
		kinds[f["name"].(string)] = k
	}
	reg, _ := json.Marshal(map[string]any{"host": "staging", "instance": "boot-1",
		"runtime":       map[string]any{"name": "fixture", "version": "1", "digest": "sha256:" + strings.Repeat("c", 64)},
		"stepKinds":     kinds,
		"modelFamilies": []any{pipelinestest.Family}})
	mixID := trainingProject(t, e, "exper")
	var w struct{ ID string }
	e.ok(e.do("POST", "/api/worker-registrations", string(reg)), 200, &w)
	var cal struct{ PipelineRun struct{ ID string } }
	e.ok(e.do("POST", "/api/projects/exper/runs:calibrate", `{"baseModel":"`+pipelinestest.BaseModel+`","mix":"`+mixID+`"}`,
		"Idempotency-Key", e.key()), 201, &cal)
	l := e.claim(w.ID)
	e.release(l.ID, steps.Outcome{State: steps.StateDone, Outputs: map[string]steps.ArtifactRef{
		"calibration": e.putJSON(map[string]any{"measured": true}, "calibration", map[string]any{"secondsPerStep": 2.0, "leaseOverheadSeconds": 20}),
	}})
	e.waitPipelineRun(cal.PipelineRun.ID, "done")

	// experiments.new: the dry run writes nothing; the experiment pins the mix revision and base model.
	newExp := `{"name":"LR on he","question":"Does a lower peak LR help?","mix":"he-mix","baseModel":"` + pipelinestest.BaseModel + `"}`
	var x experimentView
	e.ok(e.do("POST", "/api/projects/exper/experiments?dryRun=true", newExp, "Idempotency-Key", e.key()), 200, &x)
	if x.Name != "LR on he" || x.Mix.ID != mixID || x.Mix.Revision != 1 || x.Tag != "lr-on-he" || e.count("SELECT count(*) FROM experiments") != 0 {
		t.Fatalf("dry run %+v", x)
	}
	resp := e.ok(e.do("POST", "/api/projects/exper/experiments", newExp, "Idempotency-Key", e.key()), 201, &x)
	if !strings.HasPrefix(x.ID, "exp_") || resp.Header.Get("ETag") != `"1"` || x.BaseModel.ID == "" || x.RunCount != 0 {
		t.Fatalf("experiment %+v", x)
	}
	expectProblem(t, e.do("POST", "/api/projects/exper/experiments", newExp, "Idempotency-Key", e.key()), 409, "conflict")

	// runs.new with the experiment inherits its mix and base model; another mix is refused.
	e.ok(e.do("POST", "/api/projects/exper/mixes", `{"name":"other","groups":[{"name":"he","datasets":["dataset/fx-he"]}]}`,
		"Idempotency-Key", e.key()), 201, nil)
	expectProblem(t, e.do("POST", "/api/projects/exper/runs", `{"experiment":"`+x.ID+`","mix":"other"}`, "Idempotency-Key", e.key()),
		422, "validation-failed")
	expectProblem(t, e.do("POST", "/api/projects/exper/runs", `{"experiment":"`+x.ID+`","init":"checkpoint","checkpoint":"ckp_x"}`, "Idempotency-Key", e.key()),
		422, "validation-failed")
	var manual runView
	e.ok(e.do("POST", "/api/projects/exper/runs", `{"experiment":"`+x.ID+`","steps":100}`, "Idempotency-Key", e.key()), 201, &manual)
	var manualFull struct{ ExperimentID, SweepID string }
	e.ok(e.do("GET", "/api/runs/"+manual.ID, ""), 200, &manualFull)
	if manual.Mix.ID != mixID || manualFull.ExperimentID != x.ID || manualFull.SweepID != "" {
		t.Fatalf("manual run %+v %+v", manual, manualFull)
	}
	e.trainDone(e.claim(w.ID), 0.30)
	e.waitRun(manual.ID, "done")

	sweep := func(query string) string {
		return "/api/experiments/" + x.ID + "/sweeps:run" + query
	}
	ifMatch := func() string { return fmt.Sprintf(`"%d"`, e.experiment(x.ID).Rev) }

	// The dry run plans every point as a run: two peak learning rates, each estimated.
	grid := `{"mode":"grid","parameters":[{"name":"peak_lr","values":[0.0001,0.0003]}],"steps":100%s}`
	var plan sweepPlanView
	e.ok(e.do("POST", sweep("?dryRun=true"), fmt.Sprintf(grid, ""), "Idempotency-Key", e.key(), "If-Match", ifMatch()), 200, &plan)
	if len(plan.Points) != 2 || plan.Points[0].Values["peak_lr"] != 0.0001 || plan.Points[0].EstimateGpuHours <= 0 || plan.Basis != "measured" ||
		!plan.WithinCap || plan.Fits != 2 || plan.GpuHourCap != 8 || plan.EstimateGpuHours.Value <= plan.Points[0].EstimateGpuHours {
		t.Fatalf("plan %+v", plan)
	}
	one := plan.Points[0].EstimateGpuHours
	if e.count("SELECT count(*) FROM sweeps") != 0 || e.count("SELECT count(*) FROM runs") != 1 {
		t.Fatal("a dry run wrote a sweep or a run")
	}
	// Problems: an unknown parameter, the mix has no replay group, a stale revision, a cap the whole sweep passes.
	expectProblem(t, e.do("POST", sweep("?dryRun=true"), `{"parameters":[{"name":"warmup","values":[1]}]}`, "Idempotency-Key", e.key(),
		"If-Match", ifMatch()), 422, "pipeline-invalid")
	expectProblem(t, e.do("POST", sweep("?dryRun=true"), `{"parameters":[{"name":"replayShare","values":[0,0.2]}]}`, "Idempotency-Key", e.key(),
		"If-Match", ifMatch()), 422, "validation-failed")
	expectProblem(t, e.do("POST", sweep(""), fmt.Sprintf(grid, ""), "Idempotency-Key", e.key(), "If-Match", `"99"`), 412, "precondition-failed")
	tight := fmt.Sprintf(grid, fmt.Sprintf(`,"gpuHourCap":%g`, one*1.5))
	e.ok(e.do("POST", sweep("?dryRun=true"), tight, "Idempotency-Key", e.key(), "If-Match", ifMatch()), 200, &plan)
	if plan.WithinCap || plan.Fits != 1 {
		t.Fatalf("tight plan %+v", plan)
	}
	expectProblem(t, e.do("POST", sweep(""), tight, "Idempotency-Key", e.key(), "If-Match", ifMatch()), 422, "sweep-over-cap")

	// An agent's sweep is weighed against the GPU budget as a whole (gpu-spend): over it, the dry run names the rule.
	if _, err := e.pool.Exec(ctx, `UPDATE projects SET budgets = jsonb_set(budgets, '{gpuHoursPerDay}', '0.0001') WHERE slug = 'exper'`); err != nil {
		t.Fatal(err)
	}
	dry := e.agent("POST", sweep("?dryRun=true"), fmt.Sprintf(grid, ""), "Idempotency-Key", e.key(), "If-Match", ifMatch())
	if dry.StatusCode != 200 || !strings.HasPrefix(dry.Header.Get("Cadence-Policy"), "approval; rule=gpu-spend") {
		t.Fatalf("agent dry run over budget: %d %q", dry.StatusCode, dry.Header.Get("Cadence-Policy"))
	}
	_ = dry.Body.Close()
	if _, err := e.pool.Exec(ctx, `UPDATE projects SET budgets = jsonb_set(budgets, '{gpuHoursPerDay}', '8') WHERE slug = 'exper'`); err != nil {
		t.Fatal(err)
	}

	// Sweep 1 starts its first run; a second sweep waits for it; the first run used an hour of the card, so the
	// second point (its estimate on top) would pass the cap and the sweep stops.
	capped := fmt.Sprintf(grid, fmt.Sprintf(`,"gpuHourCap":%g`, 2*one+0.01))
	var s1 sweepView
	resp = e.ok(e.do("POST", sweep(""), capped, "Idempotency-Key", e.key(), "If-Match", ifMatch()), 201, &s1)
	if !strings.HasPrefix(s1.ID, "swp_") || s1.State != experiments.StateRunning || len(s1.Points) != 2 || s1.Points[0].RunID == "" ||
		s1.CurrentRunID != s1.Points[0].RunID || s1.Points[1].State != "pending" || resp.Header.Get("ETag") == "" {
		t.Fatalf("sweep %+v", s1)
	}
	expectProblem(t, e.do("POST", sweep(""), capped, "Idempotency-Key", e.key(), "If-Match", ifMatch()), 409, "conflict")
	l = e.claim(w.ID)
	if l.Spec.RunID != s1.Points[0].RunID || l.Spec.Kind != pipelinestest.KindTrain {
		t.Fatalf("sweep lease %+v", l.Spec)
	}
	if _, err := e.pool.Exec(ctx, "UPDATE leases SET created_at = now() - interval '1 hour' WHERE id = $1", l.ID); err != nil {
		t.Fatal(err)
	}
	e.trainDone(l, 0.25)
	s1 = e.waitSweep(x.ID, s1.ID, experiments.StateStopped)
	if !strings.Contains(s1.StopReason, "cap") || s1.Points[0].State != "done" || s1.Points[1].State != "skipped" || s1.Points[1].RunID != "" ||
		s1.GpuHoursSpent < 0.99 || s1.RunsDone != 1 {
		t.Fatalf("stopped sweep %+v", s1)
	}

	// Sweep 2: when its first run ends the next one starts; cancelling that run cancels the sweep.
	var s2 sweepView
	e.ok(e.do("POST", sweep(""), `{"parameters":[{"name":"seed","values":[1,2,3]}],"steps":100}`, "Idempotency-Key", e.key(),
		"If-Match", ifMatch()), 201, &s2)
	l = e.claim(w.ID)
	if l.Spec.RunID != s2.Points[0].RunID {
		t.Fatalf("second sweep lease %+v", l.Spec)
	}
	e.trainDone(l, 0.20)
	l = e.claim(w.ID)
	got := e.experiment(x.ID).Sweeps[0]
	if got.ID != s2.ID || got.Points[1].RunID == "" || l.Spec.RunID != got.Points[1].RunID || got.CurrentRunID != got.Points[1].RunID {
		t.Fatalf("the next point did not start: %+v (lease %s)", got, l.Spec.RunID)
	}
	var job struct{ Rev int }
	e.ok(e.do("GET", "/api/jobs/"+l.JobID, ""), 200, &job)
	e.ok(e.do("POST", "/api/jobs/"+l.JobID+":cancel", "", "Idempotency-Key", e.key(), "If-Match", fmt.Sprintf(`"%d"`, job.Rev)), 200, nil)
	s2 = e.waitSweep(x.ID, s2.ID, experiments.StateCancelled)
	e.release(l.ID, steps.Outcome{State: steps.StateCancelled, Error: &steps.StepError{Type: steps.ErrCancelled, Message: "stopped"}})
	if s2.Points[1].State != "cancelled" || s2.Points[2].State != "skipped" {
		t.Fatalf("cancelled sweep %+v", s2)
	}

	// The comparison: four runs oldest first, the swept parameters lead, departures marked, the best by val WER.
	x = e.experiment(x.ID)
	if x.RunCount != 4 || len(x.Runs) != 4 || x.Runs[0].RunID != manual.ID || len(x.Sweeps) != 2 || x.Sweeps[0].ID != s2.ID {
		t.Fatalf("experiment %+v", x)
	}
	if len(x.Parameters) < 3 || x.Parameters[0].Name != "peak_lr" || !x.Parameters[0].Swept || x.Parameters[0].Default != 0.0002 ||
		x.Parameters[1].Name != "seed" || !x.Parameters[1].Swept {
		t.Fatalf("parameters %+v", x.Parameters)
	}
	r1 := x.Runs[1]
	if r1.SweepID != s1.ID || r1.Point == nil || *r1.Point != 0 || r1.Values["peak_lr"] != 0.0001 || !contains(r1.Departures, "peak_lr") ||
		contains(x.Runs[0].Departures, "peak_lr") || x.Runs[0].Values["peak_lr"] != 0.0002 || r1.GpuHours < 0.99 {
		t.Fatalf("sweep run row %+v (manual %+v)", r1, x.Runs[0])
	}
	if x.Best == nil || x.Best.RunID != x.Runs[2].RunID || x.Best.ValWer != 0.2 || x.Best.Registrable || !strings.Contains(x.Best.Reason, "evals.new") ||
		!x.Runs[2].Best || x.Runs[1].Best {
		t.Fatalf("best %+v", x.Best)
	}
	var list struct{ Items []experimentView }
	e.ok(e.do("GET", "/api/projects/exper/experiments", ""), 200, &list)
	if len(list.Items) != 1 || list.Items[0].RunCount != 4 || list.Items[0].Best == nil || list.Items[0].Best.RunID != x.Best.RunID || len(list.Items[0].Runs) != 0 {
		t.Fatalf("experiments.list %+v", list.Items)
	}
	var runList struct{ Items []runView }
	e.ok(e.do("GET", "/api/projects/exper/runs?experiment="+x.ID, ""), 200, &runList)
	if len(runList.Items) != 4 {
		t.Fatalf("runs.list by experiment: %d", len(runList.Items))
	}

	// Events on entity.experiment.{id}.
	types := map[string]int{}
	for _, ev := range e.events("entity.experiment." + x.ID) {
		types[ev.Type]++
	}
	if types[experiments.EventCreated] != 1 || types[experiments.EventSweepStart] != 2 || types[experiments.EventSweepEnd] != 2 ||
		types[experiments.EventSweepStep] < 3 || types[experiments.EventRunChanged] == 0 {
		t.Fatalf("events %v", types)
	}
}

// A random sweep draws the same points for the same seed and other points for another.
func TestRandomSweepPlan(t *testing.T) {
	e := start(t)
	ctx := context.Background()
	if err := pipelinestest.RegisterTraining(ctx, e.pool); err != nil {
		t.Fatal(err)
	}
	trainingProject(t, e, "rnd")
	e.ok(e.do("POST", "/api/projects/rnd/runs:calibrate", `{"baseModel":"`+pipelinestest.BaseModel+`","mix":"he-mix"}`, "Idempotency-Key", e.key()), 201, nil)
	deadline := time.Now().Add(20 * time.Second)
	for e.count("SELECT count(*) FROM calibrations") == 0 {
		if time.Now().After(deadline) {
			t.Fatal("no calibration")
		}
		time.Sleep(50 * time.Millisecond)
	}
	var x experimentView
	e.ok(e.do("POST", "/api/projects/rnd/experiments", `{"name":"random","question":"q","mix":"he-mix","baseModel":"`+pipelinestest.BaseModel+`"}`,
		"Idempotency-Key", e.key()), 201, &x)
	body := func(seed int) string {
		return fmt.Sprintf(`{"mode":"random","runs":3,"seed":%d,"parameters":[{"name":"peak_lr","min":0.00001,"max":0.001,"scale":"log"}]}`, seed)
	}
	plan := func(seed int) sweepPlanView {
		var p sweepPlanView
		e.ok(e.do("POST", "/api/experiments/"+x.ID+"/sweeps:run?dryRun=true", body(seed), "Idempotency-Key", e.key(), "If-Match", `"1"`), 200, &p)
		return p
	}
	a, b, c := plan(5), plan(5), plan(6)
	if len(a.Points) != 3 || fmt.Sprint(a.Points) != fmt.Sprint(b.Points) || fmt.Sprint(a.Points) == fmt.Sprint(c.Points) {
		t.Fatalf("random plans %+v / %+v / %+v", a.Points, b.Points, c.Points)
	}
	for _, p := range a.Points {
		if lr, _ := p.Values["peak_lr"].(float64); lr < 0.00001 || lr > 0.001 {
			t.Fatalf("drawn %v", p.Values)
		}
	}
	// On the in-process fake worker each run ends at once and the next one starts: the sweep runs to its end.
	var sw sweepView
	e.ok(e.do("POST", "/api/experiments/"+x.ID+"/sweeps:run", body(5), "Idempotency-Key", e.key(), "If-Match", `"1"`), 201, &sw)
	sw = e.waitSweep(x.ID, sw.ID, experiments.StateDone)
	if sw.RunsDone != 3 {
		t.Fatalf("done sweep %+v", sw)
	}
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
