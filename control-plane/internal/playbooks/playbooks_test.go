package playbooks

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/usunrise88/cadence/control-plane/internal/defaults"
	"github.com/usunrise88/cadence/control-plane/internal/problems"
	"github.com/usunrise88/cadence/control-plane/internal/storage"
	"github.com/usunrise88/cadence/control-plane/templates"
)

// known answers the contract's operations plus the pending ones, as the server does.
func known(t *testing.T) func(string) bool {
	t.Helper()
	k := Known()
	if !k("mixes.new") || !k("runs.calibrate") || k("runs.fly") {
		t.Fatal("Known does not answer the operation table plus the pending operations")
	}
	return k
}

func TestBundledPlaybooksValidate(t *testing.T) {
	lib, err := Load(templates.FS, defaults.Get(), known(t))
	if err != nil {
		t.Fatalf("bundled playbooks: %v", err)
	}
	list := lib.List()
	if len(list) != 5 {
		t.Fatalf("got %d playbooks, want the five v1 playbooks", len(list))
	}
	if list[0].Name != "finetune-from-dataset" || !list[0].Runnable() {
		t.Fatalf("the runnable fine-tune playbook comes first, got %s", list[0].Name)
	}
	for _, p := range list[1:] {
		if p.Runnable() || p.AvailableFrom != 4 {
			t.Errorf("%s: the other v1 playbooks run from phase 4, got %d", p.Name, p.AvailableFrom)
		}
	}
	// Every operation a chain names uses a verb of the vocabulary, later phases included.
	raw, err := os.ReadFile("../../../api/vocabulary.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var vocab struct {
		Verbs map[string]any `yaml:"verbs"`
	}
	if err := yaml.Unmarshal(raw, &vocab); err != nil {
		t.Fatal(err)
	}
	for op := range lib.Operations() {
		_, verb, _ := strings.Cut(op, ".")
		if _, ok := vocab.Verbs[verb]; !ok {
			t.Errorf("%s: verb %q is not in api/vocabulary.yaml", op, verb)
		}
	}
	ft, _ := lib.Get("finetune-from-dataset")
	var cmds []string
	for _, s := range ft.Chain {
		cmds = append(cmds, s.Command)
	}
	if got := strings.Join(cmds, " "); got != "mixes.new runs.calibrate runs.new jobs.wait checkpoints.list evals.new evals.gate" {
		t.Errorf("fine-tune chain = %s", got)
	}
}

const minimal = `name: tiny
title: Tiny
description: A test playbook
availableFrom: 2
inputs:
  steps: { type: integer, defaultRef: training.steps }
chain:
  - { id: train, title: Train, command: runs.new }
stop:
  - step: failed
prompt: "Train {{ .Inputs.steps }} steps"
`

func TestValidate(t *testing.T) {
	d := defaults.Get()
	ok := func(op string) bool { return op == "runs.new" || op == "mixes.new" }
	cases := []struct {
		name, yaml, want string
	}{
		{"minimal", minimal, ""},
		{"unknown key", strings.Replace(minimal, "title: Tiny", "title: Tiny\nbogus: 1", 1), "field bogus not found"},
		{"bad defaultRef", strings.Replace(minimal, "training.steps", "training.nope", 1), "does not resolve"},
		{"two sources", strings.Replace(minimal, "defaultRef: training.steps", "defaultRef: training.steps, required: true", 1), "exactly one of"},
		{"unknown operation", strings.Replace(minimal, "command: runs.new", "command: runs.fly", 1), `unknown operation "runs.fly"`},
		{"later phase needs only the form", strings.Replace(minimal, "command: runs.new }", "command: evals.gate, phase: 3 }", 1), ""},
		{"not an operation", strings.Replace(minimal, "command: runs.new", "command: Runs", 1), "is not an operation"},
		{"prompt names a missing input", strings.Replace(minimal, ".Inputs.steps", ".Inputs.nope", 1), "prompt"},
		{"bad stop", strings.Replace(minimal, "step: failed", "gate: passed", 1), "stop[0]"},
		{"with names a missing input", strings.Replace(minimal, "command: runs.new }", "command: runs.new, with: { steps: $inputs.nope } }", 1), `no input "nope"`},
		{"name differs from file", strings.Replace(minimal, "name: tiny", "name: other", 1), "differs from the file name"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p, err := Parse("tiny", []byte(c.yaml))
			if err == nil {
				err = Validate(p, d, ok)
			}
			switch {
			case c.want == "" && err != nil:
				t.Fatalf("unexpected: %v", err)
			case c.want != "" && (err == nil || !strings.Contains(err.Error(), c.want)):
				t.Fatalf("got %v, want %q", err, c.want)
			}
		})
	}
}

func TestInputsKeepFileOrder(t *testing.T) {
	p, err := Parse("tiny", []byte(strings.Replace(minimal, "inputs:\n", "inputs:\n  zeta: { type: string, required: true }\n", 1)))
	if err != nil {
		t.Fatal(err)
	}
	if p.Inputs[0].Name != "zeta" || p.Inputs[1].Name != "steps" {
		t.Fatalf("inputs out of order: %+v", p.Inputs)
	}
}

func TestStepEstimatesAndSum(t *testing.T) {
	lib, err := Load(templates.FS, defaults.Get(), known(t))
	if err != nil {
		t.Fatal(err)
	}
	p, _ := lib.Get("finetune-from-dataset")
	r := Resolved{Values: map[string]any{"dataset": []any{"ver_d"}, "replay": nil, "base": "ver_b", "steps": 500}}
	var seen map[string]any
	est := map[string]Estimator{"runs.new": func(_ context.Context, _ storage.Querier, _ *defaults.Defaults, _ string, with map[string]any) (StepEstimate, error) {
		seen = with
		return StepEstimate{Basis: BasisTable, GPUHours: &Range{Value: 0.5, Low: 0.25, High: 0.75},
			DurationSeconds: &Range{Value: 1800, Low: 900, High: 2700}, PlusMinus: 0.5}, nil
	}}
	steps, err := StepEstimates(context.Background(), nil, defaults.Get(), est, p, r, "prj_1")
	if err != nil {
		t.Fatal(err)
	}
	if ds, _ := seen["datasets"].([]any); len(ds) != 1 || ds[0] != "ver_d" || seen["steps"] != 500 || seen["baseModel"] != "ver_b" {
		t.Fatalf("runs.new estimator got with = %v (replay without a value is dropped)", seen)
	}
	e := Sum(steps)
	// calibrate (hint 0.1 ± 50%) + train (table 0.5 ± 50%); eval and gate skipped; the rest spends nothing.
	if e.GPUHours != (Range{Value: 0.6, Low: 0.3, High: 0.9}) || e.Basis != BasisMixed || e.PlusMinus != 0.5 {
		t.Fatalf("sum = %+v", e)
	}
	if e.DurationSeconds.Value != 1800+360 {
		t.Fatalf("duration = %+v", e.DurationSeconds)
	}
	if !steps[5].Skipped || !steps[6].Skipped || steps[0].Basis != BasisNone {
		t.Fatalf("steps = %+v", steps)
	}
	// An estimator's problem is the step's note; the step is not counted.
	est["runs.new"] = func(context.Context, storage.Querier, *defaults.Defaults, string, map[string]any) (StepEstimate, error) {
		return StepEstimate{}, problems.EstimateUnavailable.New("no row")
	}
	steps, err = StepEstimates(context.Background(), nil, defaults.Get(), est, p, r, "")
	if err != nil {
		t.Fatal(err)
	}
	if steps[2].Note != "no row" || Sum(steps).GPUHours.Value != 0.1 || Sum(steps).Basis != BasisHint {
		t.Fatalf("with the table missing: %+v / %+v", steps[2], Sum(steps))
	}
}

func testState(t *testing.T) State {
	t.Helper()
	lib, err := Load(templates.FS, defaults.Get(), known(t))
	if err != nil {
		t.Fatal(err)
	}
	p, _ := lib.Get("finetune-from-dataset")
	return State{Name: p.Name, Title: p.Title, State: StateRunning, Plan: NewPlan(p, Estimate{}), Stops: p.Stop, NextText: p.Next}
}

func states(st State) string {
	var out []string
	for _, it := range st.Plan {
		out = append(out, it.ID+"="+it.State)
	}
	return strings.Join(out, " ")
}

func TestObserveTicksInOrder(t *testing.T) {
	st := testState(t)
	at := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	obs := func(op string, dry bool, body map[string]any) bool {
		return st.Observe(Observation{Operation: op, DryRun: dry, Status: 200, Body: body, At: at})
	}
	if st.Plan[5].State != ItemSkipped || st.Plan[6].State != ItemSkipped || !st.Plan[1].Spending || st.Plan[0].Spending {
		t.Fatalf("initial plan: %s", states(st))
	}
	// A later step's operation does not tick out of order.
	if obs("checkpoints.list", false, nil) {
		t.Fatal("checkpoints.list ticked before the mix")
	}
	// A dry run of a non-spending command changes nothing.
	if obs("mixes.new", true, nil) {
		t.Fatal("a dry run of mixes.new ticked")
	}
	obs("mixes.edit", false, map[string]any{"id": "mix_1"})
	if st.Plan[0].State != ItemDone || st.Plan[0].EntityID != "mix_1" {
		t.Fatalf("mix: %+v", st.Plan[0])
	}
	obs("runs.calibrate", true, map[string]any{"gpuHours": map[string]any{"value": 0.1}})
	if st.Plan[1].State != ItemRunning || len(st.DryRuns) != 1 || !strings.Contains(st.Plan[1].Note, "0.10 GPU-hours") {
		t.Fatalf("calibrate after its dry run: %+v %v", st.Plan[1], st.DryRuns)
	}
	obs("runs.calibrate", false, map[string]any{"jobId": "job_c"})
	if st.Plan[1].State != ItemDone || len(st.DryRuns) != 0 {
		t.Fatalf("calibrate: %+v %v", st.Plan[1], st.DryRuns)
	}
	obs("runs.new", true, nil)
	obs("runs.new", false, map[string]any{"id": "run_1", "jobId": "job_t"})
	if st.Plan[2].JobID != "job_t" || st.Plan[2].Note != "run_1, job job_t" {
		t.Fatalf("train: %+v", st.Plan[2])
	}
	// A wait on another job does not tick; a running one marks the step running; the ended one ticks it.
	if obs("jobs.wait", false, map[string]any{"id": "job_c", "state": "done"}) {
		t.Fatal("a wait on the calibration job ticked the training wait")
	}
	obs("jobs.wait", false, map[string]any{"id": "job_t", "state": "running"})
	if st.Plan[3].State != ItemRunning {
		t.Fatalf("watch while running: %+v", st.Plan[3])
	}
	obs("jobs.wait", false, map[string]any{"id": "job_t", "state": "done"})
	obs("checkpoints.list", false, map[string]any{"items": []any{}})
	if st.State != StateDone || !strings.Contains(st.Summary, "complete: 5 step(s) done") || st.Next == "" {
		t.Fatalf("end: %s — %s / %s", st.State, st.Summary, st.Next)
	}
	if obs("mixes.new", false, nil) {
		t.Fatal("a done playbook still ticks")
	}
}

func TestObserveFailedJobStops(t *testing.T) {
	st := testState(t)
	for i := range 3 {
		st.Plan[i].State = ItemDone
	}
	st.Plan[2].JobID = "job_t"
	st.Observe(Observation{Operation: "jobs.wait", Body: map[string]any{"id": "job_t", "state": "failed", "error": "oom"}})
	if st.State != StateStopped || st.Stop == nil || st.Stop.On != "step" || !strings.Contains(st.Stop.Message, "oom") {
		t.Fatalf("stop: %s %+v", st.State, st.Stop)
	}
	if !strings.Contains(st.Summary, "stopped (step failed)") {
		t.Fatalf("summary: %s", st.Summary)
	}
}

func TestStopOnHonoursTheTemplate(t *testing.T) {
	st := testState(t)
	st.Stops = []Stop{{On: "step", When: "failed"}}
	if st.StopOn("budget", "exceeded", "paused", time.Now()) {
		t.Fatal("stopped on budget though the template does not name it")
	}
	st.Stops = []Stop{{On: "budget", When: "exceeded"}}
	if !st.StopOn("budget", "exceeded", "paused", time.Now()) || st.State != StateStopped {
		t.Fatal("did not stop on budget")
	}
}

func TestNextItemSaysDryRunFirst(t *testing.T) {
	st := testState(t)
	st.Plan[0].State = ItemDone
	if got := st.NextItem(); !strings.Contains(got, "runs.calibrate, dry run first") || !strings.HasPrefix(got, "step 2 of 7") {
		t.Fatalf("next item: %s", got)
	}
}

func TestRenderPrompt(t *testing.T) {
	lib, err := Load(templates.FS, defaults.Get(), known(t))
	if err != nil {
		t.Fatal(err)
	}
	p, _ := lib.Get("finetune-from-dataset")
	out, err := Render(p, PromptData{Inputs: map[string]string{"base": "B", "dataset": "D", "replay": "R", "steps": "500", "replayShare": "0.15"},
		Project: PromptProject{Name: "Hebrew", Slug: "hebrew", Locales: "he-IL"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"project Hebrew (hebrew, locales he-IL)", "Base model: B", "Training steps: 500", "runs.new — dry run first"} {
		if !strings.Contains(out, want) {
			t.Errorf("prompt lacks %q:\n%s", want, out)
		}
	}
}

func TestEstimateNotice(t *testing.T) {
	st := testState(t)
	_ = st
	lib, _ := Load(templates.FS, defaults.Get(), known(t))
	p, _ := lib.Get("finetune-from-dataset")
	steps := make([]StepEstimate, len(p.Chain))
	for i, s := range p.Chain {
		steps[i] = StepEstimate{ID: s.ID, Command: s.Command, Basis: BasisNone, Skipped: !s.Available()}
	}
	steps[2] = StepEstimate{ID: "train", Command: "runs.new", Basis: BasisTable, GPUHours: &Range{Value: 0.83, Low: 0.42, High: 1.25}, PlusMinus: 0.5}
	e := Sum(steps)
	e.Budget = &Budget{GPUHoursPerProjectPerDay: 8, WithinDailyBudget: true}
	n := EstimateNotice(p, e)
	for _, want := range []string{"estimate 0.83 GPU-hours (0.42–1.25, ±50%, basis table)", "within the daily budget", "3. Start the training run", "skipped"} {
		if !strings.Contains(n, want) {
			t.Errorf("notice lacks %q:\n%s", want, n)
		}
	}
}
