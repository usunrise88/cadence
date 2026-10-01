package pipelines

import (
	"context"
	"encoding/json"
	"io/fs"
	"strings"
	"testing"

	"github.com/usunrise88/cadence/control-plane/internal/defaults"
	"github.com/usunrise88/cadence/control-plane/internal/problems"
	"github.com/usunrise88/cadence/control-plane/internal/steps"
	"github.com/usunrise88/cadence/control-plane/internal/storage"
	"github.com/usunrise88/cadence/control-plane/templates"
)

const twoStep = `
name: demo
inputs: { text: text }
steps:
  - id: count
    kind: tally@1
    in: { text: first.text }
    params: { steps: 500 }
  - id: first
    kind: echo@1
    in: { text: $inputs.text }
`

func fieldErrors(t *testing.T, err error) []string {
	t.Helper()
	pe, ok := problems.As(err)
	if !ok || pe.Type != problems.PipelineInvalid {
		t.Fatalf("want pipeline-invalid, got %v", err)
	}
	var out []string
	for _, f := range pe.Errors {
		out = append(out, f.Path+": "+f.Message)
	}
	return out
}

func expectErrors(t *testing.T, err error, want ...string) {
	t.Helper()
	got := strings.Join(fieldErrors(t, err), "\n")
	for _, w := range want {
		if !strings.Contains(got, w) {
			t.Errorf("errors lack %q:\n%s", w, got)
		}
	}
}

func TestParseAndOrder(t *testing.T) {
	p, err := Parse([]byte(twoStep), "demo")
	if err != nil {
		t.Fatal(err)
	}
	order, cycle := p.Order()
	if cycle != nil || len(order) != 2 || p.Steps[order[0]].ID != "first" || p.Steps[order[1]].ID != "count" {
		t.Fatalf("order %v cycle %v", order, cycle)
	}
	if v, ok := p.Steps[0].Params["steps"].(float64); !ok || v != 500 {
		t.Fatalf("params are not normalised to JSON: %#v", p.Steps[0].Params)
	}
}

func TestParseRejects(t *testing.T) {
	tests := []struct {
		name, file, doc string
		want            []string
	}{
		{"unknown key", "demo", "name: demo\nstepz: []\n", []string{"stepz"}},
		{"name differs from file", "other", twoStep, []string{"name: the pipeline is named \"demo\" but stored as pipelines/other.yaml"}},
		{"kind not pinned", "demo", "name: demo\nsteps:\n  - id: a\n    kind: echo\n", []string{"steps[0].kind"}},
		{"duplicate id", "demo", "name: demo\nsteps:\n  - {id: a, kind: echo@1}\n  - {id: a, kind: echo@1}\n", []string{"already used"}},
		{"undeclared input", "demo", "name: demo\nsteps:\n  - {id: a, kind: echo@1, in: {text: $inputs.text}}\n", []string{"declares no input \"text\""}},
		{"unknown step", "demo", "name: demo\nsteps:\n  - {id: a, kind: echo@1, in: {text: b.text}}\n", []string{"no step \"b\""}},
		{"bad wiring", "demo", "name: demo\nsteps:\n  - {id: a, kind: echo@1, in: {text: nonsense}}\n", []string{"must be $inputs.<name> or <step>.<output>"}},
		{"self", "demo", "name: demo\nsteps:\n  - {id: a, kind: echo@1, in: {text: a.text}}\n", []string{"cannot read its own output"}},
		{"cycle", "demo", "name: demo\nsteps:\n  - {id: a, kind: echo@1, in: {text: b.text}}\n  - {id: b, kind: echo@1, in: {text: a.text}}\n", []string{"cycle: a → b"}},
		{"no steps", "demo", "name: demo\nsteps: []\n", []string{"at least one step"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Parse([]byte(tt.doc), tt.file)
			expectErrors(t, err, tt.want...)
		})
	}
}

// TestBundledTemplatesParse keeps control-plane/templates/pipelines in the current format.
func TestBundledTemplatesParse(t *testing.T) {
	entries, err := fs.ReadDir(templates.FS, Dir)
	if err != nil || len(entries) == 0 {
		t.Fatalf("no bundled pipelines: %v", err)
	}
	for _, en := range entries {
		name := strings.TrimSuffix(en.Name(), ".yaml")
		b, err := fs.ReadFile(templates.FS, Path(name))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := Parse(b, name); err != nil {
			t.Errorf("%s: %v", en.Name(), err)
		}
	}
}

// fakeKinds serves step kinds from memory.
type fakeKinds map[string]Kind

func (f fakeKinds) Lookup(_ context.Context, _ storage.Querier, name, version string) (Kind, bool, error) {
	k, ok := f[name+"@"+version]
	return k, ok, nil
}

func testKinds() fakeKinds {
	est := 1800.0
	return fakeKinds{
		"echo@1": {Name: "echo", Version: "1", VersionID: "ver_echo", Consumes: map[string]string{"text": "text"},
			Produces: map[string]string{"text": "text"},
			Params: json.RawMessage(`{"type":"object","properties":{"prefix":{"type":"string","default":"",
				"x-cadence":{"default":"","description":"d","source":"s","range":{"maxLength":8}}}}}`)},
		"tally@1": {Name: "tally", Version: "1", VersionID: "ver_tally", Consumes: map[string]string{"text": "text"},
			Produces: map[string]string{"tally": "tally"}, Resources: steps.Resources{GPU: true, GPUs: 1}, EstimateSeconds: &est,
			Params: json.RawMessage(`{"type":"object","required":["steps"],"properties":{
				"steps":{"type":"integer","minimum":1,"x-cadence":{"defaultRef":"training.steps","default":1,"description":"d","source":"s","range":{"min":1,"max":200000}}},
				"precision":{"type":"string","enum":["bf16","fp16","fp32"],"x-cadence":{"defaultRef":"training.precision","default":"bf16","description":"d","source":"s","range":{"values":["bf16","fp16","fp32"]}}}}}`)},
		"needs@1": {Name: "needs", Version: "1", Consumes: map[string]string{"tally": "tally"}, Produces: map[string]string{},
			Params: json.RawMessage(`{"type":"object","required":["must"],"properties":{"must":{"type":"string"},
				"ref":{"type":"string","x-cadence":{"defaultRef":"nowhere.at_all"}}}}`)},
	}
}

func testEngine() *Engine { return New(Options{Kinds: testKinds(), Defaults: defaults.Get}) }

const textHash = "b3:0000000000000000000000000000000000000000000000000000000000000000"

func TestPlanResolvesDefaultsAndDepartures(t *testing.T) {
	p, err := Parse([]byte(twoStep), "demo")
	if err != nil {
		t.Fatal(err)
	}
	plan, err := testEngine().Plan(t.Context(), nil, p, PlanInput{
		Inputs: map[string]steps.ArtifactRef{"text": {Hash: textHash, Type: "text"}},
		Params: map[string]map[string]any{"count": {"precision": "fp16"}, "first": {"prefix": ""}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Steps) != 2 || plan.Steps[0].Step != "first" || plan.Steps[1].Step != "count" {
		t.Fatalf("steps %+v", plan.Steps)
	}
	count := plan.Steps[1]
	if count.Params["steps"] != 500.0 || count.Params["precision"] != "fp16" {
		t.Errorf("resolved %v", count.Params)
	}
	deps := map[string]Departure{}
	for _, d := range count.Departures {
		deps[d.Param] = d
	}
	// defaultRef wins over x-cadence.default (training.steps is 3000 in defaults.yaml, not 1).
	if d := deps["steps"]; d.Value != 500.0 || d.Default != 3000.0 {
		t.Errorf("steps departure %+v", d)
	}
	if d := deps["precision"]; d.Value != "fp16" || d.Default != "bf16" {
		t.Errorf("precision departure %+v", d)
	}
	if len(plan.Steps[0].Departures) != 0 || plan.Steps[0].Params["prefix"] != "" {
		t.Errorf("a value equal to its default is no departure: %+v", plan.Steps[0])
	}
	if plan.Estimate.Known || plan.Estimate.Seconds == nil || *plan.Estimate.Seconds != 1800 || *plan.Estimate.GPUHours != 0.5 ||
		len(plan.Estimate.UnknownSteps) != 1 || plan.Estimate.UnknownSteps[0] != "first" {
		t.Errorf("estimate %+v", plan.Estimate)
	}
}

func TestPlanValidationErrors(t *testing.T) {
	text := map[string]steps.ArtifactRef{"text": {Hash: textHash, Type: "text"}}
	tests := []struct {
		name string
		doc  string
		in   PlanInput
		want []string
	}{
		{"unknown kind@version", "name: demo\ninputs: {text: text}\nsteps:\n  - {id: a, kind: echo@9, in: {text: $inputs.text}}\n",
			PlanInput{Inputs: text}, []string{"steps[0].kind: no runtime publishes step kind echo@9"}},
		{"type mismatch", "name: demo\ninputs: {text: text}\nsteps:\n  - {id: a, kind: tally@1, in: {text: $inputs.text}}\n  - {id: b, kind: echo@1, in: {text: a.tally}}\n",
			PlanInput{Inputs: text}, []string{"steps[1].in.text: echo@1 consumes a text artifact as \"text\", but a.tally is a tally"}},
		{"unknown output", "name: demo\ninputs: {text: text}\nsteps:\n  - {id: a, kind: echo@1, in: {text: $inputs.text}}\n  - {id: b, kind: echo@1, in: {text: a.nope}}\n",
			PlanInput{Inputs: text}, []string{"step \"a\" (echo@1) produces no \"nope\""}},
		{"unwired and extra inputs", "name: demo\ninputs: {text: text}\nsteps:\n  - {id: a, kind: echo@1, in: {other: $inputs.text}}\n",
			PlanInput{Inputs: text}, []string{"steps[0].in.text: echo@1 consumes \"text\"", "steps[0].in.other: echo@1 has no input \"other\""}},
		{"bad params", "name: demo\ninputs: {text: text}\nsteps:\n  - {id: a, kind: echo@1, in: {text: $inputs.text}, params: {prefix: 7, colour: red}}\n  - {id: b, kind: tally@1, in: {text: $inputs.text}, params: {steps: 0, precision: fp8}}\n",
			PlanInput{Inputs: text}, []string{"steps[0].params.colour: echo@1 has no parameter \"colour\"", "steps[1].params.steps: 0 is below the safe minimum 1", "steps[1].params.precision: fp8 is not one of"}},
		{"schema violation", "name: demo\ninputs: {text: text}\nsteps:\n  - {id: a, kind: echo@1, in: {text: $inputs.text}, params: {prefix: 7}}\n",
			PlanInput{Inputs: text}, []string{"steps[0].params: the parameters do not fit echo@1"}},
		{"required without default, unresolvable defaultRef", "name: demo\ninputs: {text: text}\nsteps:\n  - {id: a, kind: tally@1, in: {text: $inputs.text}}\n  - {id: b, kind: needs@1, in: {tally: a.tally}}\n",
			PlanInput{Inputs: text}, []string{"steps[1].params.must: needs@1 needs a value", "steps[1].params.ref: its defaultRef \"nowhere.at_all\" does not resolve"}},
		{"missing, mistyped and unknown pipeline inputs", "name: demo\ninputs: {text: text, more: text}\nsteps:\n  - {id: a, kind: echo@1, in: {text: $inputs.text}}\n",
			PlanInput{Inputs: map[string]steps.ArtifactRef{"text": {Hash: textHash, Type: "tally"}, "extra": {Hash: textHash, Type: "text"}}},
			[]string{"inputs.more: the pipeline needs input \"more\"", "inputs.text: input \"text\" must be a text artifact, not tally", "inputs.extra: the pipeline declares no input \"extra\""}},
		{"override of an unknown step", "name: demo\ninputs: {text: text}\nsteps:\n  - {id: a, kind: echo@1, in: {text: $inputs.text}}\n",
			PlanInput{Inputs: text, Params: map[string]map[string]any{"zz": {"x": 1}}}, []string{"params.zz: the pipeline has no step \"zz\""}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p, err := Parse([]byte(tt.doc), "demo")
			if err != nil {
				t.Fatal(err)
			}
			_, err = testEngine().Plan(t.Context(), nil, p, tt.in)
			expectErrors(t, err, tt.want...)
		})
	}
}

func TestInputHash(t *testing.T) {
	in := map[string]steps.ArtifactRef{"text": {Hash: textHash, Type: "text", Size: 3}}
	a, _ := InputHash("echo", "1", map[string]any{"prefix": "x", "n": 1.0}, in)
	b, _ := InputHash("echo", "1", map[string]any{"n": 1.0, "prefix": "x"}, map[string]steps.ArtifactRef{"text": {Hash: textHash, Type: "text"}})
	c, _ := InputHash("echo", "2", map[string]any{"prefix": "x", "n": 1.0}, in)
	d, _ := InputHash("echo", "1", map[string]any{"prefix": "y", "n": 1.0}, in)
	if a != b || !steps.ValidHash(a) {
		t.Errorf("the hash depends on key order or metadata: %s %s", a, b)
	}
	if a == c || a == d {
		t.Error("the hash ignores the version or the parameters")
	}
}

func TestDefaultsLookup(t *testing.T) {
	d := defaults.Get()
	if v, ok := d.Lookup("training.steps"); !ok || v != 3000 {
		t.Errorf("training.steps = %v %v", v, ok)
	}
	for _, ref := range []string{"training", "training.steps.value", "nope.x", ""} {
		if _, ok := d.Lookup(ref); ok {
			t.Errorf("%q resolved", ref)
		}
	}
}

func TestSeveralArtifactsPerInput(t *testing.T) {
	ok := "name: avg\ninputs: {a: checkpoint, b: checkpoint}\nsteps:\n  - {id: s, kind: x@1, in: {checkpoints.0: $inputs.a, checkpoints.1: $inputs.b}}\n"
	if _, err := Parse([]byte(ok), "avg"); err != nil {
		t.Fatalf("name.<n> inputs: %v", err)
	}
	for _, bad := range []string{"checkpoints.x", "checkpoints.01", "Checkpoints", "checkpoints.1.2"} {
		doc := "name: avg\ninputs: {a: checkpoint}\nsteps:\n  - {id: s, kind: x@1, in: {" + bad + ": $inputs.a}}\n"
		if _, err := Parse([]byte(doc), "avg"); err == nil {
			t.Errorf("%q accepted as an input name", bad)
		}
	}
	for in, want := range map[string]string{"checkpoints.3": "checkpoints", "data": "data", "x.10": "x"} {
		if got := ConsumedName(in); got != want {
			t.Errorf("ConsumedName(%q) = %q", in, got)
		}
	}
}

func TestAccepts(t *testing.T) {
	tests := []struct {
		want, got string
		ok        bool
	}{
		{"mix", "mix", true},
		{"base_model", "checkpoint", true}, // R44: a checkpoint starts a stage like a base model
		{"checkpoint", "base_model", false},
		{"dataset", "mix", false},
	}
	for _, tt := range tests {
		if Accepts(tt.want, tt.got) != tt.ok {
			t.Errorf("Accepts(%s, %s) != %v", tt.want, tt.got, tt.ok)
		}
	}
}

func TestOOMRetryScale(t *testing.T) {
	for _, tc := range []struct{ failed, want float64 }{
		{0, 0.75},      // the kind's own batch
		{1, 0.75},      // full batch named explicitly
		{0.5, 0.375},   // a manual retry at half batch shrinks further, never back to 0.75
		{0.75, 0.5625}, // an earlier OOM retry's scale
	} {
		if got := OOMRetryScale(tc.failed); got != tc.want {
			t.Errorf("OOMRetryScale(%v) = %v, want %v", tc.failed, got, tc.want)
		}
	}
}
