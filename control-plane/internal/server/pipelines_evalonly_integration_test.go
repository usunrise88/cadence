//go:build integration

package server

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/usunrise88/cadence/control-plane/internal/pipelines/pipelinestest"
	"github.com/usunrise88/cadence/control-plane/internal/steps"
)

// evalOnlyKind is a fixture step kind reading one input of artifact type in, with job kind jobKind.
func evalOnlyKind(name, in, jobKind string) map[string]any {
	return map[string]any{
		"name": name, "version": "1", "runtime": "test", "runtimeVersionId": "ver_test",
		"params":   map[string]any{"type": "object", "properties": map[string]any{}},
		"consumes": map[string]string{"data": in}, "produces": map[string]string{"out": "text"},
		"resources": map[string]any{"gpu": jobKind == "training", "jobKind": jobKind}, "help": "steps." + name,
	}
}

// TestPipelinesRunRefusesEvalOnlyDatasets: pipelines.run answers eval-only-dataset when a training step reads a
// dataset artifact of an eval-only version or a mix artifact referencing one; an eval step may read it.
func TestPipelinesRunRefusesEvalOnlyDatasets(t *testing.T) {
	e, store := startData(t)
	if err := pipelinestest.Register(context.Background(), e.pool,
		evalOnlyKind("fit", "dataset", "training"), evalOnlyKind("score", "dataset", "eval"),
		evalOnlyKind("fitmix", "mix", "training")); err != nil {
		t.Fatal(err)
	}
	e.newProject("hebrew")
	e.commitPipeline("hebrew", "fit", "name: fit\ninputs: {data: dataset}\nsteps:\n  - {id: fit, kind: fit@1, in: {data: $inputs.data}}\n")
	e.commitPipeline("hebrew", "score", "name: score\ninputs: {data: dataset}\nsteps:\n  - {id: score, kind: score@1, in: {data: $inputs.data}}\n")
	e.commitPipeline("hebrew", "fitmix", "name: fitmix\ninputs: {mix: mix}\nsteps:\n  - {id: fit, kind: fitmix@1, in: {data: $inputs.mix}}\n")

	// fleurs-he: from a source not cleared yet; replay-golden-he: registered for evaluation only.
	train := artifact(t, store, fleursHeader("fleurs-he"), heUtts)
	if err := e.runHook(train, "plr_1", `{}`); err != nil {
		t.Fatal(err)
	}
	golden := fleursHeader("replay-golden-he")
	golden.EvalOnly, golden.Tags, golden.SplitRule = true, []string{"golden"}, "all-test"
	gold := artifact(t, store, golden, []fixtureUtt{{"g1", "אחת.", "test", "he-IL", ""}, {"g2", "שתיים.", "test", "he-IL", ""}})
	if err := e.runHook(gold, "plr_2", `{}`); err != nil {
		t.Fatal(err)
	}
	goldID := e.datasetIn("dataset/replay-golden-he").ID

	ref := func(r steps.ArtifactRef) string { return fmt.Sprintf(`{"hash":%q,"type":%q}`, r.Hash, r.Type) }
	run := func(name string, dryRun bool) string {
		return fmt.Sprintf("/api/projects/hebrew/pipelines/%s:run?dryRun=%t", name, dryRun)
	}
	body := func(input, artifact string) string { return `{"inputs":{"` + input + `":` + artifact + `}}` }
	post := func(name, input, artifact string, dryRun bool) (int, string) {
		resp := e.do("POST", run(name, dryRun), body(input, artifact), "Idempotency-Key", e.key(), "If-Match", "*")
		defer func() { _ = resp.Body.Close() }()
		var p struct{ Type, Detail string }
		_ = json.NewDecoder(resp.Body).Decode(&p)
		return resp.StatusCode, p.Type + " " + p.Detail
	}
	refused := func(what string, code int, detail, want string) {
		t.Helper()
		if code != 422 || !strings.Contains(detail, "eval-only-dataset") || !strings.Contains(detail, want) {
			t.Errorf("%s: %d %s, want 422 eval-only-dataset (%s)", what, code, detail, want)
		}
	}

	// A training step reading the golden set or the uncleared corpus is refused, dry run or not; nothing starts.
	code, d := post("fit", "data", ref(gold), true)
	refused("fit on the golden set (dry run)", code, d, "evaluation only")
	code, d = post("fit", "data", ref(gold), false)
	refused("fit on the golden set", code, d, "evaluation only")
	code, d = post("fit", "data", ref(train), true)
	refused("fit on the uncleared corpus", code, d, "source fleurs is not cleared")
	if n := e.count("SELECT count(*) FROM pipeline_runs"); n != 0 {
		t.Fatalf("%d pipeline runs started", n)
	}

	// An eval step may read eval-only data.
	if code, d = post("score", "data", ref(gold), true); code != 200 {
		t.Errorf("score on the golden set: %d %s", code, d)
	}

	// A mix artifact referencing the golden set, by its metadata or by its content, is refused for training.
	metaMix := fmt.Sprintf(`{"hash":%q,"type":"mix","meta":{"datasets":[%q]}}`, "b3:"+strings.Repeat("7", 64), goldID)
	code, d = post("fitmix", "mix", metaMix, true)
	refused("fitmix on a mix naming the golden set in meta", code, d, "evaluation only")
	content, err := store.PutBytes([]byte(`{"name":"m","groups":[{"name":"target","weight":1,"datasets":["` + goldID + `"]}]}`))
	if err != nil {
		t.Fatal(err)
	}
	code, d = post("fitmix", "mix", fmt.Sprintf(`{"hash":%q,"type":"mix"}`, content), true)
	refused("fitmix on a mix rendering the golden set", code, d, "evaluation only")

	// A person clears fleurs: its corpus may be trained on; the golden set never.
	e.ok(e.do("PATCH", "/api/registry/sources/fleurs", `{"trainingCleared":true}`, "Idempotency-Key", e.key(), "If-Match", `"1"`), 200, nil)
	if code, d = post("fit", "data", ref(train), true); code != 200 {
		t.Errorf("fit on the cleared corpus: %d %s", code, d)
	}
	code, d = post("fit", "data", ref(gold), true)
	refused("fit on the golden set after clearing", code, d, "evaluation only")
}
