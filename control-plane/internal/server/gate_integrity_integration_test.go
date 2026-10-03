//go:build integration

package server

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/usunrise88/cadence/control-plane/internal/defaults"
	"github.com/usunrise88/cadence/control-plane/internal/pipelines/pipelinestest"
	"github.com/usunrise88/cadence/control-plane/internal/registry"
)

// setBaseModel makes base model ref (a collection or ver_…) project slug's default base model.
func (e *env) setBaseModel(slug, ref string) {
	e.t.Helper()
	ctx := context.Background()
	v, err := registry.Resolve(ctx, e.pool, "", registry.KindBaseModel, ref)
	if err != nil {
		e.t.Fatal(err)
	}
	if _, err := e.pool.Exec(ctx, "UPDATE projects SET base_model_version_id = $2 WHERE slug = $1", slug, v.ID); err != nil {
		e.t.Fatal(err)
	}
}

// TestGateIntegrity: the verdict holds only against the project's own baseline and only when every golden set
// gates.yaml names was scored; models.register reads the checkpoint's latest verdict (audit 2026-10-02).
func TestGateIntegrity(t *testing.T) {
	e := start(t)
	ctx := context.Background()
	if err := pipelinestest.RegisterTraining(ctx, e.pool); err != nil {
		t.Fatal(err)
	}
	if err := pipelinestest.RegisterEvaluation(ctx, e.pool); err != nil {
		t.Fatal(err)
	}
	_, ckp, _ := evalProject(t, e, "gate-int")
	newEval := func(body string) evalView {
		t.Helper()
		var v evalView
		e.ok(e.do("POST", "/api/projects/gate-int/evals", body, "Idempotency-Key", e.key()), 201, &v)
		return e.waitEval(v.ID, "done")
	}
	gate := func(id string) evalView {
		t.Helper()
		v := e.eval(id, "")
		e.ok(e.do("POST", "/api/evals/"+id+":gate", "", "Idempotency-Key", e.key(), "If-Match", fmt.Sprintf(`"%d"`, v.Rev)), 200, &v)
		return v
	}
	register := func(evalID string) (int, string) {
		resp := e.do("POST", "/api/projects/gate-int/models:register?dryRun=true", `{"checkpointId":"`+ckp+`","evalId":"`+evalID+`"}`, "Idempotency-Key", e.key())
		defer func() { _ = resp.Body.Close() }()
		var p struct{ Type, Detail string }
		_ = json.NewDecoder(resp.Body).Decode(&p)
		return resp.StatusCode, p.Type + " " + p.Detail
	}

	// Eval A on the gate's defaults (he target, ru replay) against the project's default base model: passed.
	a := newEval(`{"subject":{"checkpointId":"` + ckp + `"},"baseline":"` + pipelinestest.BaseModel + `"}`)
	if g := gate(a.ID).Gate; g == nil || g.Verdict != "passed" {
		t.Fatalf("eval A verdict %+v", g)
	}

	// gates.yaml names both sets; eval B leaves the replay set out (its cells are cached): the verdict fails on it.
	good := `{"config":{"target":{"goldenSets":["golden-set/fx-golden-he"]},"replay":{"goldenSets":["golden-set/fx-golden-ru"]}}}`
	e.ok(e.do("PATCH", "/api/projects/gate-int/gates", good, "Idempotency-Key", e.key(), "If-Match", `"defaults"`), 200, nil)
	b := newEval(`{"subject":{"checkpointId":"` + ckp + `"},"goldenSets":["golden-set/fx-golden-he"]}`)
	gb := gate(b.ID)
	if gb.Gate == nil || gb.Gate.Verdict != "failed" || !hasFailedCheck(gb, "replay", "golden-set/fx-golden-ru", "not in this eval") {
		t.Fatalf("eval B without the replay set %+v", gb.Gate)
	}

	// The older passed eval does not outvote the later failed one.
	code, d := register(a.ID)
	if code != 409 || !strings.Contains(d, "gate-not-passed") || !strings.Contains(d, "latest gated eval") {
		t.Fatalf("register with the older passed eval: %d %s", code, d)
	}

	// A baseline other than the project's: the default base model moves, eval A's baseline is no longer the
	// project's, and its verdict fails the baseline check.
	e.setBaseModel("gate-int", defaults.Get().Wizard.BaseModel.Value)
	ga := gate(a.ID).Gate
	if ga == nil || ga.Verdict != "failed" || ga.Checks[0].Kind != "baseline" || ga.Checks[0].State != "failed" ||
		!strings.Contains(ga.Checks[0].Message, "not the project's baseline") {
		t.Fatalf("eval A against another project baseline %+v", ga)
	}

	// Back to the fixture base: eval A, gated again under gates.yaml (both sets scored), is the latest and passes;
	// registration with it is allowed.
	e.setBaseModel("gate-int", pipelinestest.BaseModel)
	if g := gate(a.ID).Gate; g == nil || g.Verdict != "passed" {
		t.Fatalf("eval A regated %+v", g)
	}
	if code, d := register(a.ID); code != 200 {
		t.Fatalf("register with the latest passed eval: %d %s", code, d)
	}
}

// hasFailedCheck reports whether the verdict of v has a failed check of kind on goldenSet with a message containing msg.
func hasFailedCheck(v evalView, kind, goldenSet, msg string) bool {
	for _, c := range v.Gate.Checks {
		if c.Kind == kind && c.GoldenSet == goldenSet && c.State == "failed" && strings.Contains(c.Message, msg) {
			return true
		}
	}
	return false
}
