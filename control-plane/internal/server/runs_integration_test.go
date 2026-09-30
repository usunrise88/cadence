//go:build integration

package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/usunrise88/cadence/control-plane/internal/compute"
	"github.com/usunrise88/cadence/control-plane/internal/defaults"
	"github.com/usunrise88/cadence/control-plane/internal/pipelines/pipelinestest"
	"github.com/usunrise88/cadence/control-plane/internal/registry"
	"github.com/usunrise88/cadence/control-plane/internal/runs"
	"github.com/usunrise88/cadence/control-plane/internal/steps"
	"github.com/usunrise88/cadence/control-plane/internal/telemetry"
)

type runView struct {
	ID            string `json:"id"`
	Status        string `json:"status"`
	Init          string `json:"init"`
	CheckpointID  string `json:"checkpointId"`
	ParentRunID   string `json:"parentRunId"`
	PipelineRunID string `json:"pipelineRunId"`
	TrainStep     string `json:"trainStep"`
	Steps         int    `json:"steps"`
	Rev           int    `json:"rev"`
	CurrentJobID  string `json:"currentJobId"`
	ResumedFrom   string `json:"resumedFrom"`
	Error         string `json:"error"`
	Family        struct{ Name string }
	Mix           struct {
		ID, Name, Hash string
		Revision       int
	}
	Recipe   struct{ Pipeline, Source, Commit, Version string }
	Timeline []struct {
		Step, Kind, Role, State string
		Attempts                int
		BatchScale              float64
		OOMRetries              int
	}
	Departures []struct {
		Step, Param string
		Value       any
	}
	ParentDiff []struct {
		Param       string
		Value       any
		ParentValue any
	}
	FinalMetrics     map[string]float64
	BestCheckpointID string
	CheckpointCount  int
	GPUHours         float64
	Estimate         struct{ Basis string }
}

type checkpointView struct {
	ID, RunID, Artifact, Kind, Family string
	Step                              *int64
	ValWer                            *float64
	AveragedFrom                      []string
	Kept                              bool
	Rank                              int
}

// trainingProject seeds compute, the fixture family, a project whose train-stage pipeline uses the fixture kinds, a
// trainable dataset version and a mix of it.
func trainingProject(t *testing.T, e *env, slug string) (mixID string) {
	t.Helper()
	ctx := context.Background()
	if _, err := compute.Seed(ctx, e.pool, defaults.Get().Compute.Hosts, registry.Bundled()); err != nil {
		t.Fatal(err)
	}
	e.newProject(slug)
	e.commitPipeline(slug, "train-stage", pipelinestest.TrainStage)
	if _, err := pipelinestest.RegisterDataset(ctx, e.pool, e.admin.CAS, "fx-he", 2, false); err != nil {
		t.Fatal(err)
	}
	var m struct{ ID string }
	e.ok(e.do("POST", "/api/projects/"+slug+"/mixes", `{"name":"he-mix","groups":[{"name":"he","datasets":["dataset/fx-he"]}]}`,
		"Idempotency-Key", e.key()), 201, &m)
	return m.ID
}

func (e *env) run(id string) runView {
	e.t.Helper()
	var r runView
	e.ok(e.do("GET", "/api/runs/"+id, ""), 200, &r)
	return r
}

func (e *env) waitRun(id, status string) runView {
	e.t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		r := e.run(id)
		if r.Status == status {
			return r
		}
		if time.Now().After(deadline) {
			e.t.Fatalf("run %s is %s (%s), want %s", id, r.Status, r.Error, status)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func (e *env) checkpoints(slug, runID string) []checkpointView {
	e.t.Helper()
	var l struct {
		Items    []checkpointView
		KeepTopK int
	}
	e.ok(e.do("GET", "/api/projects/"+slug+"/checkpoints?run="+runID, ""), 200, &l)
	return l.Items
}

// The training loop on the in-process fake worker: calibration switches the estimate to measured, a run mirrors its
// pipeline run, checkpoints register with top k, averaging adds checkpoints, a new stage starts from the best one,
// eval-only data is refused, an agent over the budget waits for an approval, and metrics come back binned.
func TestRunsEndToEnd(t *testing.T) {
	e := start(t)
	ctx := context.Background()
	if err := pipelinestest.RegisterTraining(ctx, e.pool); err != nil {
		t.Fatal(err)
	}
	mixID := trainingProject(t, e, "train")
	newRun := func(body string, query ...string) *http.Response {
		q := ""
		if len(query) > 0 {
			q = "?" + query[0]
		}
		return e.do("POST", "/api/projects/train/runs"+q, body, "Idempotency-Key", e.key())
	}
	body := `{"baseModel":"` + pipelinestest.BaseModel + `","mix":"he-mix"}`

	// Before any calibration the fixture base model has no estimate (no table row).
	expectProblem(t, newRun(body, "dryRun=true"), 422, "estimate-unavailable")
	expectProblem(t, newRun(`{"baseModel":"`+pipelinestest.BaseModel+`"}`), 422, "validation-failed")

	// runs.calibrate runs the family's calibrate role; the estimate turns measured.
	var cal struct {
		Key struct {
			BaseModel, CardClass, Precision string
			MemoryCapGb                     float64
		}
		Family, Step string
		Plan         struct{ Steps []struct{ Kind string } }
		PipelineRun  struct{ ID string }
	}
	e.ok(e.do("POST", "/api/projects/train/runs:calibrate?dryRun=true", `{"baseModel":"`+pipelinestest.BaseModel+`","mix":"he-mix"}`,
		"Idempotency-Key", e.key()), 200, &cal)
	if cal.Family != pipelinestest.FamilyName || cal.Step != "fx_calibrate@1" || len(cal.Plan.Steps) != 1 || cal.Key.CardClass != "blackwell-48gb" ||
		cal.Key.MemoryCapGb != 24 || cal.Key.BaseModel != pipelinestest.BaseModel {
		t.Fatalf("calibration dry run %+v", cal)
	}
	e.ok(e.do("POST", "/api/projects/train/runs:calibrate", `{"baseModel":"`+pipelinestest.BaseModel+`","mix":"`+mixID+`"}`,
		"Idempotency-Key", e.key()), 201, &cal)
	e.waitPipelineRun(cal.PipelineRun.ID, "done")
	var est struct {
		Basis          string
		SecondsPerStep float64
		PlusMinus      float64
		Steps          int
		GpuHours       runs.Range
		Mix            struct{ Hash string }
		Budget         struct {
			GpuHoursPerProjectPerDay, UsedTodayGpuHours, RemainingGpuHours float64
			WithinDailyBudget                                              bool
		}
	}
	e.ok(newRun(body, "dryRun=true"), 200, &est)
	// The pipeline's train step writes steps: 200; 200 × 0.5 s measured, ±10 %.
	if est.Basis != "measured" || est.SecondsPerStep != 0.5 || est.PlusMinus != 0.1 || est.Steps != 200 || est.GpuHours.Value != 0.028 ||
		!strings.HasPrefix(est.Mix.Hash, "b3:") || est.Budget.GpuHoursPerProjectPerDay != 8 || !est.Budget.WithinDailyBudget {
		t.Fatalf("measured estimate %+v", est)
	}
	if n := e.count("SELECT count(*) FROM runs"); n != 0 {
		t.Fatalf("a dry run wrote %d runs", n)
	}

	// A real run: 201 with the run; it mirrors its pipeline run to done, with the calibrate step reused.
	var r runView
	resp := e.ok(newRun(`{"baseModel":"`+pipelinestest.BaseModel+`","mix":"he-mix","seed":7,"params":{"peak_lr":0.0003}}`), 201, &r)
	if resp.Header.Get("ETag") == "" || !strings.HasPrefix(r.ID, "run_") || r.Init != "base" || r.Family.Name != pipelinestest.FamilyName ||
		r.TrainStep != "train" || r.Recipe.Pipeline != "train-stage" || r.Recipe.Source != "repository" || r.Recipe.Commit == "" ||
		r.Mix.ID != mixID || r.Mix.Revision != 1 || r.Mix.Hash != est.Mix.Hash || r.Steps != 200 || r.Estimate.Basis != "measured" {
		t.Fatalf("new run %+v", r)
	}
	r = e.waitRun(r.ID, "done")
	if len(r.Timeline) != 2 || r.Timeline[0].State != "reused" || r.Timeline[0].Role != "calibrate" || r.Timeline[1].Role != "train" ||
		r.FinalMetrics["val_wer"] == 0 || r.CheckpointCount != 1 || r.BestCheckpointID == "" {
		t.Fatalf("finished run %+v", r)
	}
	deps := map[string]any{}
	for _, d := range r.Departures {
		deps[d.Step+"."+d.Param] = d.Value
	}
	if deps["train.steps"] != float64(200) || deps["train.seed"] != float64(7) || deps["train.peak_lr"] != 0.0003 {
		t.Errorf("departures %+v", r.Departures)
	}
	st := e.events("run." + r.ID + ".status")
	if len(st) < 2 || st[0].Type != runs.EventCreated || st[len(st)-1].Type != runs.EventStatusChanged ||
		!strings.Contains(string(st[len(st)-1].Payload), `"status":"done"`) {
		t.Fatalf("status events %+v", st)
	}

	// Checkpoints: the trained one, then averages; the top k (3) keeps the best three of four.
	ck := e.checkpoints("train", r.ID)
	if len(ck) != 1 || ck[0].Kind != "trained" || ck[0].Step == nil || *ck[0].Step != 200 || ck[0].ValWer == nil ||
		ck[0].Family != pipelinestest.FamilyName || !ck[0].Kept || ck[0].Rank != 1 {
		t.Fatalf("trained checkpoint %+v", ck)
	}
	if ev := e.events("run." + r.ID + ".checkpoints"); len(ev) != 1 || ev[0].Type != runs.EventCheckpointSaved {
		t.Fatalf("checkpoint events %+v", ev)
	}
	a := ck[0].ID
	average := func(ids ...string) string {
		var out struct {
			Step        string
			PipelineRun struct{ ID string }
		}
		b, _ := json.Marshal(map[string]any{"checkpoints": ids})
		e.ok(e.do("POST", "/api/runs/"+r.ID+"/checkpoints:average", string(b), "Idempotency-Key", e.key()), 201, &out)
		if out.Step != "fx_average@1" {
			t.Fatalf("average step %s", out.Step)
		}
		e.waitPipelineRun(out.PipelineRun.ID, "done")
		return out.PipelineRun.ID
	}
	expectProblem(t, e.do("POST", "/api/runs/"+r.ID+"/checkpoints:average", `{"checkpoints":["`+a+`","ckp_nope"]}`, "Idempotency-Key", e.key()), 404, "not-found")
	average(a, a)
	ck = e.checkpoints("train", r.ID)
	b := ck[0].ID
	if len(ck) != 2 || ck[0].Kind != "averaged" || len(ck[0].AveragedFrom) != 2 || ck[0].AveragedFrom[0] != a {
		t.Fatalf("after one average %+v", ck)
	}
	average(a, b)
	c := e.checkpoints("train", r.ID)[0].ID
	average(b, c)
	ck = e.checkpoints("train", r.ID)
	if len(ck) != 4 || !ck[0].Kept || !ck[2].Kept || ck[3].Kept || ck[3].ID != a || ck[3].Rank != 4 {
		t.Fatalf("top k %+v", ck)
	}
	if v := e.run(r.ID); v.Status != "done" || v.BestCheckpointID != ck[0].ID || v.CheckpointCount != 4 {
		t.Fatalf("an average changed the run %+v", v)
	}
	var one checkpointView
	e.ok(e.do("GET", "/api/checkpoints/"+ck[0].ID, ""), 200, &one)
	if one.ID != ck[0].ID || one.RunID != r.ID {
		t.Errorf("checkpoints.get %+v", one)
	}

	// A new stage from the best checkpoint with an explicit peak learning rate; the config diff names the parent.
	parent := e.run(r.ID)
	expectProblem(t, e.do("POST", "/api/runs/"+r.ID+":stage", `{"peakLr":0.00002}`, "Idempotency-Key", e.key(), "If-Match", `"99"`), 412, "precondition-failed")
	var staged runView
	e.ok(e.do("POST", "/api/runs/"+r.ID+":stage", `{"peakLr":0.00002,"steps":100}`, "Idempotency-Key", e.key(),
		"If-Match", fmt.Sprintf(`"%d"`, parent.Rev)), 201, &staged)
	if staged.Init != "checkpoint" || staged.CheckpointID != ck[0].ID || staged.ParentRunID != r.ID || staged.Mix.Hash != r.Mix.Hash {
		t.Fatalf("stage %+v", staged)
	}
	staged = e.waitRun(staged.ID, "done")
	diff := map[string][2]any{}
	for _, d := range staged.ParentDiff {
		diff[d.Param] = [2]any{d.Value, d.ParentValue}
	}
	if diff["init"] != [2]any{"checkpoint", "base"} || diff["peak_lr"] != [2]any{0.00002, 0.0003} || diff["steps"] != [2]any{float64(100), float64(200)} {
		t.Fatalf("parent diff %+v", staged.ParentDiff)
	}
	if staged.Timeline[0].State == "reused" {
		t.Error("the stage's calibrate step starts from another model and must not be reused")
	}
	sck := e.checkpoints("train", staged.ID)
	if len(sck) != 1 || sck[0].RunID != staged.ID {
		t.Fatalf("stage checkpoints %+v", sck)
	}

	// Eval-only data is refused: by the datasets dry run and, once its source loses clearance, through the mix.
	if _, err := pipelinestest.RegisterDataset(ctx, e.pool, e.admin.CAS, "fx-golden", 1, true); err != nil {
		t.Fatal(err)
	}
	expectProblem(t, newRun(`{"datasets":["dataset/fx-golden"]}`, "dryRun=true"), 422, "eval-only-dataset")
	if _, err := e.pool.Exec(ctx, `INSERT INTO sources (id, name, licence, kind, training_cleared, created_by)
		VALUES ('src_fx', 'fx-src', 'cc-by-4.0', 'public', true, '{"kind":"user","id":"usr_admin"}')`); err != nil {
		t.Fatal(err)
	}
	if err := pgx.BeginFunc(ctx, e.pool, func(tx pgx.Tx) error {
		p, _ := json.Marshal(map[string]any{"hours": 1, "artifact": ck[0].Artifact, "sourceIds": []string{"src_fx"}, "locales": []string{"he"}})
		_, _, _, err := registry.Register(ctx, tx, registry.RegisterInput{Kind: registry.KindDataset, Name: "dataset/fx-sourced", Payload: p, Freeze: true,
			Actor: testAgent}, time.Now())
		return err
	}); err != nil {
		t.Fatal(err)
	}
	e.ok(e.do("POST", "/api/projects/train/mixes", `{"name":"sourced","groups":[{"name":"he","datasets":["dataset/fx-sourced"]}]}`,
		"Idempotency-Key", e.key()), 201, nil)
	if _, err := e.pool.Exec(ctx, "UPDATE sources SET training_cleared = false WHERE id = 'src_fx'"); err != nil {
		t.Fatal(err)
	}
	expectProblem(t, newRun(`{"baseModel":"`+pipelinestest.BaseModel+`","mix":"sourced"}`), 422, "eval-only-dataset")

	// An agent over the project's budget gets an approval; approving it starts the run as the agent.
	if _, err := e.pool.Exec(ctx, `UPDATE projects SET budgets = jsonb_set(budgets, '{gpuHoursPerDay}', '0.01') WHERE slug = 'train'`); err != nil {
		t.Fatal(err)
	}
	dry := e.agent("POST", "/api/projects/train/runs?dryRun=true", body, "Idempotency-Key", e.key())
	if dry.StatusCode != 200 || !strings.HasPrefix(dry.Header.Get("Cadence-Policy"), "approval; rule=gpu-spend") {
		t.Fatalf("agent dry run over budget: %d %q", dry.StatusCode, dry.Header.Get("Cadence-Policy"))
	}
	_ = dry.Body.Close()
	var gated struct{ ApprovalID string }
	e.ok(e.agent("POST", "/api/projects/train/runs", body, "Idempotency-Key", e.key()), 202, &gated)
	if gated.ApprovalID == "" || e.count("SELECT count(*) FROM runs") != 2 {
		t.Fatalf("gated run %+v", gated)
	}
	var decided approvalView
	e.ok(e.do("POST", "/api/approvals/"+gated.ApprovalID+":approve", "{}", "Idempotency-Key", e.key(), "If-Match", `"1"`), 200, &decided)
	if decided.State != "approved" || decided.Result == nil || decided.Result.Status != 201 {
		t.Fatalf("decided %+v", decided)
	}
	var approved runView
	if err := json.Unmarshal(decided.Result.Body, &approved); err != nil || !strings.HasPrefix(approved.ID, "run_") {
		t.Fatalf("approved run %s (%v)", decided.Result.Body, err)
	}
	e.waitRun(approved.ID, "done")
	var actor string
	if err := e.pool.QueryRow(ctx, "SELECT actor->>'id' FROM runs WHERE id = $1", approved.ID).Scan(&actor); err != nil || actor != testAgent.ID {
		t.Errorf("approved run's actor %q (%v)", actor, err)
	}

	// metrics.get bins long series server-side and appends after a step.
	pts := make([]telemetry.Point, 0, 1000)
	t0 := time.Now().Add(-time.Hour)
	for i := range 1000 {
		step := int64(i + 1)
		pts = append(pts, telemetry.Point{Name: "loss", Step: &step, Value: float64(1000 - i), WallTime: t0.Add(time.Duration(i) * time.Second)})
	}
	if err := pgx.BeginFunc(ctx, e.pool, func(tx pgx.Tx) error {
		return telemetry.Insert(ctx, tx, telemetry.Source{JobID: "job_x", RunID: r.ID}, pts)
	}); err != nil {
		t.Fatal(err)
	}
	var set struct {
		X         string
		MaxPoints int
		LastStep  int64
		Series    []struct {
			Name   string
			Total  int
			Binned bool
			Points []struct {
				X, Value, Min, Max float64
				Count              int
				Step               int64
			}
		}
		Checkpoints []struct{ ID string }
	}
	e.ok(e.do("GET", "/api/metrics/"+r.ID+"?names=loss&maxPoints=10", ""), 200, &set)
	if len(set.Series) != 1 || set.Series[0].Total != 1000 || !set.Series[0].Binned || len(set.Series[0].Points) != 10 ||
		set.Series[0].Points[0].Count != 100 || set.Series[0].Points[0].Max != 1000 || set.Series[0].Points[0].Min != 901 ||
		set.Series[0].Points[9].Step != 1000 || set.LastStep != 1000 || len(set.Checkpoints) != 4 {
		t.Fatalf("binned %+v", set)
	}
	e.ok(e.do("GET", "/api/metrics/"+r.ID+"?names=loss&afterStep=990&x=wall", ""), 200, &set)
	if set.X != "wall" || set.Series[0].Binned || len(set.Series[0].Points) != 10 || set.Series[0].Points[0].X != 990 {
		t.Fatalf("appended %+v", set.Series)
	}
	e.ok(e.do("GET", "/api/metrics/"+r.ID+"?x=gpuHours&maxPoints=2", ""), 200, &set)
	if len(set.Series[0].Points) != 2 {
		t.Fatalf("gpu-hours axis %+v", set.Series)
	}
	var list struct{ Items []runView }
	e.ok(e.do("GET", "/api/projects/train/runs?status=done", ""), 200, &list)
	if len(list.Items) != 3 {
		t.Errorf("runs.list %d", len(list.Items))
	}
}

// The OOM retry shows on the run: the train step's second attempt at 0.75× batch.
func TestRunShowsOOMRetry(t *testing.T) {
	e := start(t)
	if err := pipelinestest.RegisterTraining(context.Background(), e.pool); err != nil {
		t.Fatal(err)
	}
	trainingProject(t, e, "oom")
	e.ok(e.do("POST", "/api/projects/oom/runs:calibrate", `{"baseModel":"`+pipelinestest.BaseModel+`","mix":"he-mix"}`, "Idempotency-Key", e.key()), 201, nil)
	deadline := time.Now().Add(20 * time.Second)
	for e.count("SELECT count(*) FROM calibrations") == 0 {
		if time.Now().After(deadline) {
			t.Fatal("no calibration")
		}
		time.Sleep(50 * time.Millisecond)
	}
	e.leases.Script("train", pipelinestest.Action{Fail: &steps.StepError{Type: steps.ErrOOM, Message: "CUDA out of memory"}})
	var r runView
	e.ok(e.do("POST", "/api/projects/oom/runs", `{"baseModel":"`+pipelinestest.BaseModel+`","mix":"he-mix"}`, "Idempotency-Key", e.key()), 201, &r)
	r = e.waitRun(r.ID, "done")
	tr := r.Timeline[1]
	if tr.Attempts != 2 || tr.OOMRetries != 1 || tr.BatchScale != steps.OOMBatchScale {
		t.Fatalf("train stage %+v", tr)
	}
}
