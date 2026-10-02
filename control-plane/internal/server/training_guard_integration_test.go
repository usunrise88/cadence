//go:build integration

package server

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/usunrise88/cadence/control-plane/internal/artifacts"
	"github.com/usunrise88/cadence/control-plane/internal/cas"
	"github.com/usunrise88/cadence/control-plane/internal/data"
	"github.com/usunrise88/cadence/control-plane/internal/pipelines/pipelinestest"
	"github.com/usunrise88/cadence/control-plane/internal/steps"
)

// mixEntry is one dataset of a rendered mix: the version id and the artifact the worker reads.
type mixEntry struct{ dataset, artifact string }

// renderedMix puts a cadence.mix/1 rendering (one group of entries) into store and returns its hash.
func renderedMix(t *testing.T, store *cas.Store, entries ...mixEntry) string {
	t.Helper()
	var cfg []map[string]any
	for _, e := range entries {
		cfg = append(cfg, map[string]any{"type": "dataset", "dataset": e.dataset, "artifact": e.artifact, "hours": 1})
	}
	b, err := json.Marshal(map[string]any{"format": data.MixFormat, "mix": map[string]any{"id": "mix_x", "name": "m", "revision": 1},
		"input_cfg": []map[string]any{{"type": "group", "name": "target", "weight": 1, "probability": 1, "input_cfg": cfg}}})
	if err != nil {
		t.Fatal(err)
	}
	h, err := store.PutBytes(b)
	if err != nil {
		t.Fatal(err)
	}
	return h
}

// recordArtifact indexes directory artifact ref (with its meta) as a step output would, at its true size.
func (e *env) recordArtifact(store *cas.Store, ref steps.ArtifactRef) {
	e.t.Helper()
	m, err := store.ReadManifest(ref.Hash)
	if err != nil {
		e.t.Fatal(err)
	}
	ref.Size = 0
	for _, f := range m.Files {
		ref.Size += f.Size
	}
	if err := pgx.BeginFunc(context.Background(), e.pool, func(tx pgx.Tx) error {
		_, err := artifacts.Record(context.Background(), tx, store, ref, "", nil)
		return err
	}); err != nil {
		e.t.Fatal(err)
	}
}

// TestTrainingGuardTrustsNothingTheCallerSays: the training guard takes an input's type and meta from the artifact
// index, a mix's datasets from its content, refuses unregistered and augmented datasets, and checks the inputs a
// training step gets from other steps when it is queued (audit 2026-10-02, security High).
func TestTrainingGuardTrustsNothingTheCallerSays(t *testing.T) {
	e, store := startData(t)
	if err := pipelinestest.Register(context.Background(), e.pool,
		evalOnlyKind("fit", "dataset", "training"), evalOnlyKind("fitmix", "mix", "training"), pipelinestest.RelayFixture); err != nil {
		t.Fatal(err)
	}
	e.newProject("hebrew")
	e.commitPipeline("hebrew", "fit", "name: fit\ninputs: {data: dataset}\nsteps:\n  - {id: fit, kind: fit@1, in: {data: $inputs.data}}\n")
	e.commitPipeline("hebrew", "fitmix", "name: fitmix\ninputs: {mix: mix}\nsteps:\n  - {id: fit, kind: fitmix@1, in: {data: $inputs.mix}}\n")
	e.commitPipeline("hebrew", "relay", "name: relay\ninputs: {data: dataset}\nsteps:\n"+
		"  - {id: relay, kind: "+pipelinestest.KindRelay+"@1, in: {data: $inputs.data}}\n  - {id: fit, kind: fit@1, in: {data: relay.data}}\n")

	// A cleared, trainable corpus and a golden set.
	train := artifact(t, store, fleursHeader("fleurs-he"), heUtts)
	if err := e.runHook(train, "plr_1", `{}`); err != nil {
		t.Fatal(err)
	}
	e.ok(e.do("PATCH", "/api/registry/sources/fleurs", `{"trainingCleared":true}`, "Idempotency-Key", e.key(), "If-Match", `"1"`), 200, nil)
	trainID := e.datasetIn("dataset/fleurs-he").ID
	e.recordArtifact(store, train)
	golden := fleursHeader("replay-golden-he")
	golden.EvalOnly, golden.Tags, golden.SplitRule = true, []string{"golden"}, "all-test"
	gold := artifact(t, store, golden, []fixtureUtt{{"g1", "אחת.", "test", "he-IL", ""}, {"g2", "שתיים.", "test", "he-IL", ""}})
	if err := e.runHook(gold, "plr_2", `{}`); err != nil {
		t.Fatal(err)
	}
	goldID := e.datasetIn("dataset/replay-golden-he").ID
	e.recordArtifact(store, gold)

	post := func(name, input, artifact string, dryRun bool) (int, string, string) {
		resp := e.do("POST", fmt.Sprintf("/api/projects/hebrew/pipelines/%s:run?dryRun=%t", name, dryRun),
			`{"inputs":{"`+input+`":`+artifact+`}}`, "Idempotency-Key", e.key(), "If-Match", "*")
		defer func() { _ = resp.Body.Close() }()
		var p struct{ Type, Detail, ID string }
		_ = json.NewDecoder(resp.Body).Decode(&p)
		return resp.StatusCode, strings.TrimPrefix(p.Type, "https://cadence.local/help/errors/") + " " + p.Detail, p.ID
	}
	refused := func(what string, code int, detail, slug, want string) {
		t.Helper()
		if code != 422 || !strings.Contains(detail, slug) || !strings.Contains(detail, want) {
			t.Errorf("%s: %d %s, want 422 %s (%s)", what, code, detail, slug, want)
		}
	}
	ref := func(hash, typ string) string { return fmt.Sprintf(`{"hash":%q,"type":%q}`, hash, typ) }

	// The trainable corpus passes, directly and in a mix.
	if code, d, _ := post("fit", "data", ref(train.Hash, "dataset"), true); code != 200 {
		t.Errorf("fit on the cleared corpus: %d %s", code, d)
	}
	if code, d, _ := post("fitmix", "mix", ref(renderedMix(t, store, mixEntry{trainID, train.Hash}), "mix"), true); code != 200 {
		t.Errorf("fitmix on a mix of the cleared corpus: %d %s", code, d)
	}

	// 1. The golden set sent as a mix: the index says dataset.
	code, d, _ := post("fitmix", "mix", ref(gold.Hash, "mix"), true)
	refused("golden set typed mix", code, d, "validation-failed", "artifact index")

	// 2. A real mix of the golden set with forged meta naming only the clean corpus: the content decides.
	forged := fmt.Sprintf(`{"hash":%q,"type":"mix","meta":{"datasets":[%q]}}`, renderedMix(t, store, mixEntry{trainID, gold.Hash}), trainID)
	code, d, _ = post("fitmix", "mix", forged, true)
	refused("mix of the golden artifact with forged meta", code, d, "eval-only-dataset", "evaluation only")
	code, d, _ = post("fitmix", "mix", ref(renderedMix(t, store, mixEntry{goldID, train.Hash}), "mix"), true)
	refused("mix naming the golden version", code, d, "eval-only-dataset", "evaluation only")
	code, d, _ = post("fitmix", "mix", ref(e.putTextHash("not a mix"), "mix"), true)
	refused("text sent as a mix", code, d, "validation-failed", "not a cadence.mix/1")

	// 3. A dataset artifact no version registers.
	loose := artifact(t, store, fleursHeader("never-imported"), []fixtureUtt{{"l1", "שלוש.", "train", "he-IL", "s1"}})
	code, d, _ = post("fit", "data", ref(loose.Hash, "dataset"), true)
	refused("unregistered dataset", code, d, "eval-only-dataset", "not a registered dataset version")
	code, d, _ = post("fitmix", "mix", ref(renderedMix(t, store, mixEntry{"", loose.Hash}), "mix"), true)
	refused("mix of an unregistered dataset", code, d, "eval-only-dataset", "not a registered dataset version")

	// 4. An augment_dataset output: an augmented copy of a golden set, indexed with purpose augmented.
	aug := artifact(t, store, fleursHeader("augmented"), []fixtureUtt{{"a1", "אחת.", "test", "he-IL", ""}})
	aug.Meta = json.RawMessage(`{"purpose":"augmented","profile":"noisy"}`)
	e.recordArtifact(store, aug)
	code, d, _ = post("fit", "data", fmt.Sprintf(`{"hash":%q,"type":"dataset","meta":{}}`, aug.Hash), true)
	refused("augmented golden copy", code, d, "golden-set-leakage", "augmented copy")
	if n := e.count("SELECT count(*) FROM pipeline_runs"); n != 0 {
		t.Fatalf("%d pipeline runs started", n)
	}

	// 5. A data step relays the golden set to a training step: the dry run cannot see it, the queue does. The
	// training step is never queued and the run fails with the reason.
	code, d, id := post("relay", "data", ref(gold.Hash, "dataset"), false)
	if code != 201 {
		t.Fatalf("relay run: %d %s", code, d)
	}
	r := e.waitPipelineRun(id, "failed")
	if !strings.Contains(r.Error, "step fit failed (input)") || !strings.Contains(r.Error, "evaluation only") {
		t.Errorf("relay run error %q", r.Error)
	}
	if calls := e.leases.CallsOf("fit"); len(calls) != 0 {
		t.Errorf("the training step ran %d times", len(calls))
	}
	// The same pipeline relaying the trainable corpus trains (and the fixture kind, which is not a real trainer, fails
	// inside the step, past the guard).
	if code, d, id = post("relay", "data", ref(train.Hash, "dataset"), false); code != 201 {
		t.Fatalf("relay run of the corpus: %d %s", code, d)
	}
	e.waitPipelineRun(id, "failed")
	if calls := e.leases.CallsOf("fit"); len(calls) != 1 {
		t.Errorf("the training step on the corpus ran %d times, want 1", len(calls))
	}
}

// putTextHash puts text into the environment's store and returns its hash.
func (e *env) putTextHash(text string) string {
	e.t.Helper()
	h, err := e.admin.CAS.PutBytes([]byte(text))
	if err != nil {
		e.t.Fatal(err)
	}
	return h
}
