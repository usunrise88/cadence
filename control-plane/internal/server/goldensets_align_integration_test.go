//go:build integration

package server

import (
	"context"
	"fmt"
	"math"
	"slices"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/usunrise88/cadence/control-plane/internal/auth"
	"github.com/usunrise88/cadence/control-plane/internal/defaults"
	"github.com/usunrise88/cadence/control-plane/internal/pipelines/pipelinestest"
	"github.com/usunrise88/cadence/control-plane/internal/registry"
)

// alignerFixture is an aligning kind as the omni pack publishes align_reference: one dataset in, an alignment out, a
// card, and a registry-reference parameter naming the aligner auxiliary (the fake worker runs it as fx_align).
var alignerFixture = map[string]any{
	"name": pipelinestest.KindAlign, "version": "2", "runtime": "test", "runtimeVersionId": "ver_test",
	"params": map[string]any{"type": "object", "properties": map[string]any{
		"aligner": map[string]any{"type": "string", "default": "auxiliary/omniasr-ctc-1b", "x-cadence": map[string]any{
			"default": "auxiliary/omniasr-ctc-1b", "description": "The aligner", "source": "test", "range": "any",
			"registryRef": map[string]any{"kind": "auxiliary", "role": "align"},
		}},
	}},
	"consumes": map[string]string{"data": "dataset"}, "produces": map[string]string{"alignment": "alignment"},
	"resources": map[string]any{"gpu": true, "gpus": 1, "jobKind": "data"}, "help": "steps.fx-align",
}

type alignPlanView struct {
	Kind    string
	Aligner *struct {
		Name      string
		Languages []string
	}
	Sets []struct {
		GoldenSetVersionID, Name, Locale, Step string
		Hours, EstimateSeconds                 float64
	}
	Skipped []struct {
		GoldenSetVersionID, Name, Reason, Message, AlignmentID string
	}
	SecondsPerAudioHour float64
	Estimate            struct {
		Known        bool
		Seconds      *float64
		GPUHours     *float64
		UnknownSteps []string
	}
	PipelineRun *struct{ ID, Pipeline, Source string }
}

// goldenSets.align on the in-process fake worker (phase 4 tail): one pipeline run aligns every golden set the project
// adopted, one step per dataset artifact (two golden sets on one artifact share it), with a known estimate from
// defaults.yaml; a set whose language the aligner lacks is skipped, an aligner the project has not adopted refuses
// the request, and a second call finds everything aligned and starts nothing.
func TestGoldenSetsAlign(t *testing.T) {
	e := start(t)
	ctx := context.Background()
	if err := pipelinestest.Register(ctx, e.pool, alignerFixture); err != nil {
		t.Fatal(err)
	}
	p := e.newProject("gsal")
	utts := func(tag string, n int) []pipelinestest.GoldenUtterance {
		var out []pipelinestest.GoldenUtterance
		for i := range n {
			out = append(out, pipelinestest.GoldenUtterance{Audio: fmt.Sprintf("b3:%s%063d", tag, i), Speaker: "spk",
				Ref: fmt.Sprintf("word number %d of the golden set", i), DurationS: 3})
		}
		return out
	}
	he := utts("1", 4)
	golden := map[string]string{}
	for _, g := range []struct {
		name, locale string
		hours        float64
		utts         []pipelinestest.GoldenUtterance
	}{
		{"fx-gs-he", "he-IL", 0.5, he},
		{"fx-gs-he-copy", "he-IL", 0.5, he}, // the same dataset artifact
		{"fx-gs-sr", "sr-RS", 0.25, utts("2", 3)},
		{"fx-gs-ka", "ka-GE", 0.1, utts("3", 2)},
	} {
		id, err := pipelinestest.RegisterGoldenSet(ctx, e.pool, e.admin.CAS, g.name, g.locale, g.hours, g.utts)
		if err != nil {
			t.Fatal(err)
		}
		golden[g.name] = id
		if err := pgx.BeginFunc(ctx, e.pool, func(tx pgx.Tx) error {
			_, err := registry.AdoptQuietly(ctx, tx, p.ID, []string{id}, auth.DevActor())
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	const path = "/api/projects/gsal/golden-sets:align"

	// The aligner the kind names by default is not adopted yet: refused, nothing planned.
	pr := expectProblem(t, e.do("POST", path+"?dryRun=true", "{}", "Idempotency-Key", e.key()), 422, "validation-failed")
	if len(pr.Errors) != 1 || pr.Errors[0].Path != "/aligner" || !strings.Contains(pr.Errors[0].Message, "auxiliary/omniasr-ctc-1b") {
		t.Fatalf("unadopted aligner: %+v", pr)
	}
	e.adoptAuxiliary("gsal", e.auxiliary("auxiliary/omniasr-ctc-1b").ID, e.do)

	// The dry run: he and its copy share one step, sr has its own, ka is a language the aligner lacks.
	var dry alignPlanView
	e.ok(e.do("POST", path+"?dryRun=true", "{}", "Idempotency-Key", e.key()), 200, &dry)
	if dry.Kind != pipelinestest.KindAlign+"@2" || dry.Aligner == nil || dry.Aligner.Name != "auxiliary/omniasr-ctc-1b" ||
		!slices.Contains(dry.Aligner.Languages, "sr") || dry.PipelineRun != nil {
		t.Fatalf("dry run %+v", dry)
	}
	perHour, overhead := defaults.Get().Eval.AlignSecondsPerAudioHour.Value, defaults.Get().Eval.AlignStepOverheadS.Value
	est := func(h float64) float64 { return math.Round(overhead + h*perHour) }
	steps := map[string]string{}
	for _, s := range dry.Sets {
		steps[s.Name] = s.Step
		if s.EstimateSeconds != est(s.Hours) {
			t.Errorf("%s estimate %v for %v h", s.Name, s.EstimateSeconds, s.Hours)
		}
	}
	if len(dry.Sets) != 3 || steps["golden-set/fx-gs-he"] == "" || steps["golden-set/fx-gs-he"] != steps["golden-set/fx-gs-he-copy"] ||
		steps["golden-set/fx-gs-sr"] == steps["golden-set/fx-gs-he"] {
		t.Fatalf("sets %+v", dry.Sets)
	}
	if len(dry.Skipped) != 1 || dry.Skipped[0].Name != "golden-set/fx-gs-ka" || dry.Skipped[0].Reason != "language" ||
		!strings.Contains(dry.Skipped[0].Message, "does not cover ka-GE") {
		t.Fatalf("skipped %+v", dry.Skipped)
	}
	// Two steps (overhead + audio hours × seconds per audio hour each), on a card: the policy weighs a known estimate.
	if !dry.Estimate.Known || dry.Estimate.Seconds == nil || *dry.Estimate.Seconds != est(0.5)+est(0.25) ||
		dry.Estimate.GPUHours == nil || *dry.Estimate.GPUHours <= 0 || dry.SecondsPerAudioHour != perHour {
		t.Fatalf("estimate %+v (%v s per audio hour)", dry.Estimate, dry.SecondsPerAudioHour)
	}

	// The real call: one pipeline run aligns all three; each golden set then carries its alignment.
	var run alignPlanView
	e.ok(e.do("POST", path, "{}", "Idempotency-Key", e.key()), 201, &run)
	if run.PipelineRun == nil || run.PipelineRun.Pipeline != "align-golden-sets" || run.PipelineRun.Source != "inline" {
		t.Fatalf("run %+v", run)
	}
	r := e.waitPipelineRun(run.PipelineRun.ID, "done")
	if len(r.Steps) != 2 {
		t.Fatalf("steps %+v", r.Steps)
	}
	for _, name := range []string{"fx-gs-he", "fx-gs-he-copy", "fx-gs-sr"} {
		var v goldenSetAlignmentView
		e.ok(e.do("GET", "/api/registry/golden-sets/"+golden[name], ""), 200, &v)
		if v.Alignment == nil || v.Alignment.PipelineRunID != run.PipelineRun.ID {
			t.Fatalf("%s alignment %+v", name, v.Alignment)
		}
	}

	// Again: everything is aligned or out of the aligner's languages; nothing starts.
	var again alignPlanView
	e.ok(e.do("POST", path, "{}", "Idempotency-Key", e.key()), 200, &again)
	reasons := map[string]string{}
	for _, s := range again.Skipped {
		reasons[s.Name] = s.Reason
		if s.Reason == "aligned" && !strings.HasPrefix(s.AlignmentID, "aln_") {
			t.Errorf("skip %+v has no alignment id", s)
		}
	}
	if again.PipelineRun != nil || len(again.Sets) != 0 || len(reasons) != 4 || reasons["golden-set/fx-gs-sr"] != "aligned" ||
		reasons["golden-set/fx-gs-ka"] != "language" {
		t.Fatalf("again %+v", again)
	}

	// Named golden sets: a pattern over the adopted ones; a name that matches nothing is a field error.
	var named alignPlanView
	e.ok(e.do("POST", path+"?dryRun=true", `{"goldenSets":["golden-set/fx-gs-he*"]}`, "Idempotency-Key", e.key()), 200, &named)
	if len(named.Skipped) != 2 || len(named.Sets) != 0 {
		t.Fatalf("named %+v", named)
	}
	pr = expectProblem(t, e.do("POST", path+"?dryRun=true", `{"goldenSets":["golden-set/nope*"]}`, "Idempotency-Key", e.key()),
		422, "validation-failed")
	if len(pr.Errors) != 1 || pr.Errors[0].Path != "/goldenSets/0" {
		t.Fatalf("unknown golden set: %+v", pr)
	}
}
